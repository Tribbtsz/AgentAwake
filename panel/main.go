package main

// main.go — AgentAwake 托盘应用
//
// 托盘图标只做最简单的事：显示状态色 + 左键打开面板。
// 所有设置（任务胶囊位置、显示开关、自带任务时长默认值……）都在面板 GUI 里。
//
// 性能：状态轮询直接读租约 JSON + 注册表，不启动任何子进程；
// 只有点击按钮（申请/释放/停止）时才调用 awake.exe CLI。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
)

// cliPath 命令行程序的位置：优先找和面板同目录的兄弟目录，其次 PATH。
// 不写死任何盘符——项目搬到哪都能用。
func cliPath() string {
	if p, err := os.Executable(); err == nil {
		dir := filepath.Dir(p)
		for _, cand := range []string{
			filepath.Join(dir, "awake.exe"),               // 同一目录
			filepath.Join(filepath.Dir(dir), "awake.exe"), // panel\ 上一级（项目根）
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	return "awake.exe" // 退化成 PATH 查找
}

// ---------- CLI（仅动作时调用） ----------

func runCLI(args ...string) (string, error) {
	cli := cliPath()
	dbg("runCLI %s %s", cli, strings.Join(args, " "))
	// 带超时：命令卡住时界面不会一直转圈
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	out, err := cmd.CombinedOutput()
	dbg("runCLI done err=%v out=%s", err, strings.TrimSpace(string(out)))
	return string(out), err
}

// ---------- 应用 ----------

type app struct {
	snap  snapshot
	cfg   panelConfig
	err   string
	toast string // 最近一次操作结果

	win  *mygo.Window
	tray *mygo.Tray
	bar  *mygo.Window

	embedded        bool      // 任务胶囊是否已嵌进任务栏（SetParent）
	overTaskbar     bool      // 覆盖模式：顶层窗口压在任务栏空白区
	barW            int       // 任务栏胶囊宽度（跟随内容）
	page            panelPage // 当前页：状态 / 设置
	settingsH       int       // 设置页量出的内容高度（0 表示还没量过）
	startPage       string    // 启动时打开的页面
	debugToast      string    // 视觉验收用：启动时弹一条 toast
	pendingSettings bool      // 已请求切到设置页，前端轮询时消费
	shownAt         time.Time // 面板打开时刻，用于过滤开窗瞬间的误触
}

type panelPage int

const (
	pageStatus panelPage = iota
	pageSettings
)

func (a *app) refresh() {
	a.snap = readSnapshot()
	a.invalidate()
}

func (a *app) invalidate() {
	if a.bar != nil {
		a.bar.Invalidate()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// update 在主线程改状态并重绘所有窗口
func (a *app) update(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
	} else {
		mygo.RunOnMain(fn)
	}
	if a.bar != nil {
		a.bar.Invalidate()
	}
}

func (a *app) act(args ...string) {
	go func() {
		out, err := runCLI(args...)
		a.update(func() {
			a.toast = strings.TrimSpace(out)
			if err != nil {
				a.err = fmt.Sprintf("%v\n%s", err, strings.TrimSpace(out))
			} else {
				a.err = ""
			}
		})
		a.update(func() { a.snap = readSnapshot() })
	}()
}

func main() {
	a := &app{}
	a.cfg = loadPanelConfig()
	a.snap = readSnapshot()

	// --panel：启动时直接打开面板（也方便快捷方式/命令行调用）
	openOnStart := false
	a.debugToast = os.Getenv("AGENTAWAKE_DEMO_TOAST")
	for _, arg := range os.Args[1:] {
		if arg == "--panel" || arg == "-panel" {
			openOnStart = true
		}
		if arg == "--settings" || arg == "-settings" {
			openOnStart = true
			a.startPage = "settings"
		}
	}

	mygo.App.WhenReady(func() {
		// 托盘应用：窗口全关时不退出进程，保持托盘与胶囊常驻
		mygo.App.OnWindowAllClosed(func() {})

		initFrontend() // 内嵌 HTML/CSS
		mygo.Bind(Api{a: a})

		tray, err := mygo.NewTray(mygo.TrayOptions{
			ID:      "agentawake",
			Icon:    trayIcon(a.snap),
			ToolTip: trayTip(a.snap),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "托盘创建失败:", err)
		}
		a.tray = tray
		a.tray.OnClick(func() { a.showPanel() })
		a.tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开面板", Click: func(*mygo.MenuItem, *mygo.Window) { a.showPanel() }},
			{Label: "设置", Click: func(*mygo.MenuItem, *mygo.Window) { a.openSettings() }},
			mygo.Separator(),
			{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Exit(0) }},
		}))

		a.ensureBar()

		if openOnStart {
			a.showPanel()
		}

		go a.pollLoop()
		go a.pulseLoop()
	})

	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// pollLoop 只读本地文件与注册表，开销是微秒级；2 秒一次足够
// buildStamp 构建标记：直接用可执行文件的写入时间，不需要任何 -ldflags 注入
var buildStamp = stampFromExe()

func stampFromExe() string {
	p, err := os.Executable()
	if err != nil {
		return "dev"
	}
	st, err := os.Stat(p)
	if err != nil {
		return "dev"
	}
	return st.ModTime().Format("0102-1504")
}

func (a *app) pollLoop() {
	for {
		time.Sleep(2 * time.Second)
		snap := readSnapshot()
		if sameSnapshot(a.snap, snap) {
			continue
		}
		a.update(func() {
			a.snap = snap
			if a.tray != nil {
				a.tray.SetIcon(trayIcon(snap), false)
				a.tray.SetToolTip(trayTip(snap))
			}
			a.pushBarState()
			a.syncBarSize()
			a.fitPanel()
		})
	}
}

// pulseLoop 已移除：用户要求不要托盘图标闪烁
func (a *app) pulseLoop() {}

// sameSnapshot 判断两次状态是否一致（只比会影响界面显示的部分）
func sameSnapshot(a, b snapshot) bool {
	if a.Holding != b.Holding || len(a.Leases) != len(b.Leases) || a.Err != b.Err {
		return false
	}
	for i := range a.Leases {
		x, y := a.Leases[i], b.Leases[i]
		if x.ID != y.ID || x.Active != y.Active || x.Name != y.Name || x.StateText != y.StateText {
			return false
		}
		// 定时租约的剩余时间按分钟取整比较，避免每秒都算「变了」
		if x.Mode == "deadline" && int(x.Remaining.Minutes()) != int(y.Remaining.Minutes()) {
			return false
		}
	}
	return true
}

func trayIcon(s snapshot) []byte {
	switch {
	case !s.Holding && s.Err != "":
		return iconPNG16
	default:
		return iconPNG32
	}
}

func trayTip(s snapshot) string {
	if s.Holding {
		return fmt.Sprintf("AgentAwake %s：保持中（%d 个租约）", buildStamp, activeCount(s.Leases))
	}
	return "AgentAwake " + buildStamp + "：空闲"
}

// ---------- 面板窗口 ----------

func (a *app) showPanel() {
	// 先把最新状态读进来，避免首次显示时高度按旧状态算、内容溢出出现滚动条
	a.snap = readSnapshot()

	if a.win != nil {
		a.fitPanel()
		a.win.Show()
		a.win.Focus()
		a.bringPanelFront()
		return
	}
	// 面板是网页（HTML/CSS）；托盘与任务栏任务胶囊仍是原生 Go
	startH := a.statusHeight()
	if a.startPage == "settings" {
		startH = settingsHeight
	}
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:           "AgentAwake",
		URL:             "/",
		Width:           panelWidth,
		Height:          startH,
		UseContentSize:  true,
		Frameless:       true,
		DisableResize:   true,
		AlwaysOnTop:     true, // 托盘打开的控制面板：置顶，避免被其他窗口盖住
		BackgroundColor: "#14161a",
	})
	a.win.OnClosed(func() { a.rememberPanelPos(); a.win = nil })
	a.shownAt = time.Now()
	a.win.Show()
	a.placePanel()
	a.win.Focus()
	a.bringPanelFront()

	// 网页窗口创建后句柄可能还没就绪，稍后补一次定位+前置
	go func() {
		time.Sleep(450 * time.Millisecond)
		mygo.RunOnMain(func() {
			a.placePanel()
			a.bringPanelFront()
		})
	}()
}

// placePanel 首次打开时居中；之后沿用用户自己拖到的位置
func (a *app) placePanel() {
	if a.win == nil {
		return
	}
	h := a.win.NativeHandle()
	if h == 0 {
		return
	}
	if a.cfg.PanelX != 0 || a.cfg.PanelY != 0 {
		procSetWindowPos.Call(h, hwndInsertAfter,
			uintptr(a.cfg.PanelX), uintptr(a.cfg.PanelY), 0, 0,
			uintptr(swPNoSize|swPNoActivate))
		return
	}
	cw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	ch, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	var rc struct{ L, T, R, B int32 }
	if r, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return
	}
	w := int(rc.R - rc.L)
	hh := int(rc.B - rc.T)
	x := (int(cw) - w) / 2
	y := (int(ch) - hh) / 2
	if y < 0 {
		y = 0
	}
	procSetWindowPos.Call(h, hwndInsertAfter, uintptr(x), uintptr(y), 0, 0,
		uintptr(swPNoSize|swPNoActivate))
}

// rememberPanelPos 关闭时记住用户放的位置
func (a *app) rememberPanelPos() {
	if a.win == nil {
		return
	}
	h := a.win.NativeHandle()
	if h == 0 {
		return
	}
	var rc struct{ L, T, R, B int32 }
	if r, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return
	}
	a.cfg.PanelX = int(rc.L)
	a.cfg.PanelY = int(rc.T)
	savePanelConfig(a.cfg)
}

// openSettings 打开面板并切到设置页
func (a *app) openSettings() {
	if a.win == nil {
		a.startPage = "settings"
		a.showPanel()
		return
	}
	a.pendingSettings = true // 前端下次轮询会切页
	a.win.Show()
	a.win.Focus()
	a.bringPanelFront()
}

// bringPanelFront 强制把面板提到前台。
// Windows 会阻止后台进程抢前台，经典解法：临时 AttachThreadInput 到前台线程再抢。
func (a *app) bringPanelFront() {
	if a.win == nil {
		return
	}
	h := a.win.NativeHandle()
	dbg("bringPanelFront hwnd=%d", h)
	if h == 0 {
		return
	}

	procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, uintptr(swPNoMove|swPNoSize))
	procSetWindowPos.Call(h, hwndInsertAfter, 0, 0, 0, 0, uintptr(swPNoMove|swPNoSize))
	procBringWindowToTop.Call(h)

	fg, _, _ := procGetForegroundWindow.Call()
	if fg != 0 && uintptr(fg) != h {
		var fgPid uint32
		fgThread, _, _ := procGetWindowThreadProcessId.Call(fg, uintptr(unsafe.Pointer(&fgPid)))
		selfThread, _, _ := procGetCurrentThreadId.Call()
		if fgThread != 0 && fgThread != selfThread {
			procAttachThreadInput.Call(selfThread, fgThread, 1)
			procSetForegroundWindow.Call(h)
			procAttachThreadInput.Call(selfThread, fgThread, 0)
		}
	} else {
		procSetForegroundWindow.Call(h)
	}
	dbg("foreground requested")
}

// dbg 调试日志（仅设置环境变量时写文件）
func dbg(format string, args ...any) {
	if os.Getenv("AGENTAWAKE_DEBUG") == "" {
		return
	}
	f, err := os.OpenFile(os.TempDir()+`\agentawake-panel.log`, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

func secs(v int) string {
	switch {
	case v < 0:
		return "—"
	case v == 0:
		return "从不"
	case v >= 3600 && v%3600 == 0:
		return fmt.Sprintf("%d 小时", v/3600)
	case v >= 60:
		return fmt.Sprintf("%d 分钟", v/60)
	default:
		return fmt.Sprintf("%d 秒", v)
	}
}

func spanText(d time.Duration) string {
	if d <= 0 {
		return "已到期"
	}
	if d >= 2*time.Hour {
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%d 分", int(d.Minutes())+1)
	}
	return fmt.Sprintf("%d 秒", int(d.Seconds())+1)
}

func (a *app) holding() bool { return a.snap.Holding }

func (a *app) statusLabel() string {
	if a.snap.Err != "" && !a.snap.Holding {
		return "出错"
	}
	if a.snap.Holding {
		return "保持中"
	}
	return "空闲"
}
