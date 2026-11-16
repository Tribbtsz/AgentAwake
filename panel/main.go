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
	snap snapshot
	cfg  panelConfig

	win  *mygo.Window
	tray *mygo.Tray
	bar  *mygo.Window

	// 胶囊（原生 ui）：状态、窗口底色（嵌入模式才用）、模式文案
	barUI    barUI
	barBG    string
	barMode  string
	quitting bool

	embedded        bool      // 任务胶囊是否已嵌进任务栏（SetParent）
	overTaskbar     bool      // 覆盖模式：顶层窗口压在任务栏空白区
	barSuppressed   bool      // 任务栏被隐藏/全屏时临时收起胶囊
	barW            int       // 任务栏胶囊宽度（跟随内容）
	page            panelPage // 当前页：状态 / 设置
	reservedRows    int       // 操作进行中预占的租约行数（先长高，避免结果出现时跳一下）
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

func (a *app) invalidate() {
	if a.bar != nil {
		a.bar.Invalidate()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// update 在主线程改状态并重绘所有窗口。
// 调用方可能在任意 goroutine（CLI 动作、轮询），所以整段都放到主线程，
// 连读取 a.win / a.bar 也不例外。
func (a *app) update(fn func()) {
	mygo.RunOnMain(func() {
		fn()
		a.invalidate()
	})
}

func main() {
	cliOK = cliFound()

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

	// 单实例：面板、托盘、任务栏胶囊都只应该有一份。
	mygo.App.SetName("AgentAwake")
	if !mygo.App.RequestSingleInstanceLock() {
		return // 参数已经转交给正在运行的实例
	}
	mygo.App.OnSecondInstance(func(args []string, _ string) {
		mygo.RunOnMain(func() {
			if !mygo.App.IsReady() {
				return // 启动瞬间：别在 UI 循环就绪前建窗口
			}
			if wantsSettings(args) {
				a.openSettings()
			} else {
				a.showPanel()
			}
		})
	})

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
			dbg("托盘创建失败: %v", err)
		}
		a.tray = tray
		a.tray.OnClick(func() { a.showPanel() })
		a.tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开面板", Click: func(*mygo.MenuItem, *mygo.Window) { a.showPanel() }},
			{Label: "设置", Click: func(*mygo.MenuItem, *mygo.Window) { a.openSettings() }},
			{Label: "打开日志", Click: func(*mygo.MenuItem, *mygo.Window) { openLog() }},
			mygo.Separator(),
			{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.quitting = true
				logf("panel", "面板退出")
				mygo.App.Exit(0)
			}},
		}))

		a.updateBarState() // 先定好状态色再建窗口，避免首帧闪一下空闲灰
		// 系统「减少动画」：首帧也按它来，别先淡入一次再停
		a.barUI.still = mygo.Theme.Preferences().ReduceMotion
		a.ensureBar()
		logf("panel", "面板启动 %s：胶囊=%s", buildStamp(), a.barModeText())

		if openOnStart {
			a.showPanel()
		}

		go a.pollLoop()
	})

	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		dbg("App.Run 失败: %v", err)
		logf("panel", "UI 循环退出: %v", err)
		os.Exit(1)
	}
}

// wantsSettings 判断转交过来的命令行是否要求直接打开设置页
func wantsSettings(args []string) bool {
	for _, arg := range args {
		if arg == "--settings" || arg == "-settings" {
			return true
		}
	}
	return false
}

// cliOK 面板启动时 awake.exe 是否可见。找不到时在状态页提示，
// 而不是让按钮静默失败。
var cliOK bool

func cliFound() bool {
	p := cliPath()
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return true
	}
	_, err := exec.LookPath(p)
	return err == nil
}

// pollLoop 只读本地文件与注册表，开销是微秒级；2 秒一次足够
// version 版本号：发布构建用 -ldflags "-X main.version=v0.1.0" 注入；
// 本地 go build 不注入时就是 dev，面板右上角与托盘提示都显示它。
var version = "dev"

// buildStamp 返回展示用的版本标记：发布版是 v0.1.0，开发版退化成
// 可执行文件的写入时间（如 1116-1530），一样能对上「你看到的是哪一版」。
func buildStamp() string {
	if version != "dev" {
		return version
	}
	return stampFromExe()
}

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
	last := a.snap
	for {
		time.Sleep(2 * time.Second)
		snap := readSnapshot()
		if sameSnapshot(last, snap) {
			continue
		}
		last = snap
		a.update(func() {
			a.snap = snap
			if a.tray != nil {
				a.tray.SetIcon(trayIcon(snap), false)
				a.tray.SetToolTip(trayTip(snap))
			}
			a.updateBarState()
			a.syncBarSize()
			a.fitPanel()
		})
	}
}

// sameSnapshot 判断两次状态是否一致（只比会影响界面显示的部分）
func sameSnapshot(a, b snapshot) bool {
	if a.Holding != b.Holding || len(a.Leases) != len(b.Leases) || a.Err != b.Err {
		return false
	}
	if a.Power != b.Power || a.SupervisorPID != b.SupervisorPID {
		return false
	}
	for i := range a.Leases {
		x, y := a.Leases[i], b.Leases[i]
		// 比 leaseDetailText 用到的字段：详情文本变了必须让界面跟着刷新
		if x.ID != y.ID || x.Active != y.Active || x.Name != y.Name || x.Mode != y.Mode ||
			x.Key != y.Key || x.Remaining != y.Remaining ||
			x.PID != y.PID || x.ProcessName != y.ProcessName || x.MaxUntil != y.MaxUntil {
			return false
		}
		// 定时租约的剩余时间：平时按分钟比较，最后一分钟按秒，倒计时才动
		if x.Mode == "deadline" {
			if x.Remaining < 2*time.Minute || y.Remaining < 2*time.Minute {
				if int(x.Remaining.Seconds()) != int(y.Remaining.Seconds()) {
					return false
				}
			} else if int(x.Remaining.Minutes()) != int(y.Remaining.Minutes()) {
				return false
			}
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
		return fmt.Sprintf("AgentAwake %s：保持中（%d 个租约）", buildStamp(), activeCount(s.Leases))
	}
	return "AgentAwake " + buildStamp() + "：空闲"
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
	// 胶囊由 mygo 原生 ui 绘制（barnative.go）；只有面板是网页
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

// placePanel 首次打开时居中；之后沿用用户自己拖到的位置。
// 保存的位置可能来自已拔掉的显示器，此时退回居中，避免面板跑到屏幕外。
func (a *app) placePanel() {
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
	w := int(rc.R - rc.L)
	hh := int(rc.B - rc.T)

	x, y := 0, 0
	if (a.cfg.PanelX != 0 || a.cfg.PanelY != 0) && panelOnScreen(a.cfg.PanelX, a.cfg.PanelY, w, hh) {
		x, y = a.cfg.PanelX, a.cfg.PanelY
	} else {
		cw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
		ch, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
		x = (int(cw) - w) / 2
		y = (int(ch) - hh) / 2
		if y < 0 {
			y = 0
		}
	}
	procSetWindowPos.Call(h, hwndInsertAfter, uintptr(x), uintptr(y), 0, 0,
		uintptr(swPNoSize|swPNoActivate))
}

// panelOnScreen 判断窗口左上角是否还落在虚拟桌面里（至少留 120×80 可见）
func panelOnScreen(x, y, w, h int) bool {
	sm := func(i int) int {
		v, _, _ := procGetSystemMetrics.Call(uintptr(i))
		return int(int32(v))
	}
	vx, vy := sm(76), sm(77) // SM_XVIRTUALSCREEN / SM_YVIRTUALSCREEN
	vw, vh := sm(78), sm(79) // SM_CXVIRTUALSCREEN / SM_CYVIRTUALSCREEN
	if vw <= 0 || vh <= 0 {
		return true
	}
	if w > vw {
		w = vw
	}
	if h > vh {
		h = vh
	}
	return x+min(w, 120) > vx && x < vx+vw && y+min(h, 80) > vy && y < vy+vh
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

func (a *app) statusLabel() string {
	if a.snap.Err != "" && !a.snap.Holding {
		return "出错"
	}
	if a.snap.Holding {
		return "保持中"
	}
	return "空闲"
}
