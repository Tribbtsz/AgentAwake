package main

// webapi.go — 面板的 Go 侧：内嵌前端 + 暴露给 JS 的接口
//
// 界面是 HTML/CSS（web/），通过 window.mygo.call("Api.Xxx") 调用这里。
// 托盘、任务栏任务胶囊、状态轮询仍然是原生 Go。
//
// 线程模型：绑定方法由 mygo 放在各自的 goroutine 上执行，而 app 状态
// （snap/cfg/page/win/bar）只在主线程读写。所有会碰这些状态的入口都经过
// onUI 回到主线程，避免数据竞争和偶发的界面错乱。

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

//go:embed all:web
var webFS embed.FS

func initFrontend() {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return
	}
	mygo.SetFrontend(sub)
}

// onUI 在 UI 线程上执行并取回结果。已经在主线程时直接执行。
// 测试或启动早期还没有 UI 循环，此时直接执行，避免死等。
func onUI[T any](fn func() T) T {
	if !mygo.App.IsReady() {
		return fn()
	}
	var v T
	mygo.RunOnMain(func() { v = fn() })
	return v
}

// ---------- 给前端的 DTO ----------

type leaseDTO struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Active bool   `json:"active"`
	Detail string `json:"detail"`
}

type stateDTO struct {
	Holding      bool       `json:"holding"`
	Error        string     `json:"error"`
	Label        string     `json:"label"`
	Sub          string     `json:"sub"`
	Sleep        string     `json:"sleep"`
	Hibernate    string     `json:"hibernate"`
	Display      string     `json:"display"`
	Leases       []leaseDTO `json:"leases"`
	OffsetX      int        `json:"offsetX"`
	ShowBar      bool       `json:"showBar"`
	MaxHours     int        `json:"maxHours"`
	Pulse        bool       `json:"pulse"`
	WantSettings bool       `json:"wantSettings"`
	Build        string     `json:"build"`
}

// Api 暴露给界面
type Api struct{ a *app }

func (api Api) State() stateDTO {
	if api.a == nil {
		return stateDTO{Leases: []leaseDTO{}}
	}
	return onUI(func() stateDTO {
		a := api.a
		s := a.snap
		dto := stateDTO{
			Holding:      s.Holding,
			Error:        s.Err,
			Sleep:        secs(s.Power.SleepAC),
			Hibernate:    secs(s.Power.HibAC),
			Display:      secs(s.Power.DisplayAC),
			OffsetX:      a.cfg.OffsetX,
			ShowBar:      a.cfg.ShowBar,
			MaxHours:     a.cfg.MaxHours,
			Pulse:        a.cfg.Pulse,
			WantSettings: a.pendingSettings,
			Build:        buildStamp,
			Leases:       make([]leaseDTO, 0, len(s.Leases)),
		}
		a.pendingSettings = false
		if dto.MaxHours <= 0 {
			dto.MaxHours = 12
		}

		switch {
		case s.Err != "" && !s.Holding:
			dto.Label = "出错"
			dto.Sub = s.Err
		case s.Holding:
			dto.Label = "保持中"
			dto.Sub = fmt.Sprintf("%d 个租约生效 · 守护进程 %s", activeCount(s.Leases), pidText(s.SupervisorPID))
		default:
			dto.Label = "空闲"
			dto.Sub = "没有租约，按系统原设置睡眠"
		}

		for _, l := range s.Leases {
			dto.Leases = append(dto.Leases, leaseDTO{
				ID:     l.ID,
				Key:    l.Key,
				Name:   l.Name,
				Mode:   l.Mode,
				Active: l.Active,
				Detail: leaseDetailText(l),
			})
		}
		return dto
	})
}

// InitialPage 启动时显示哪一页（--settings 直接进设置）。
// 只生效一次：之后从托盘打开面板都回到状态页。
func (api Api) InitialPage() string {
	if api.a == nil {
		return "status"
	}
	return onUI(func() string {
		page := api.a.startPage
		api.a.startPage = ""
		if page == "" {
			return "status"
		}
		return page
	})
}

// BarState 任务栏小条的状态
type barStateDTO struct {
	State   string `json:"state"` // idle | on | err
	Label   string `json:"label"`
	Count   string `json:"count"`
	Holding bool   `json:"holding"`
	Pulse   bool   `json:"pulse"`
}

func (api Api) BarState() barStateDTO {
	if api.a == nil {
		return barStateDTO{State: "idle", Label: "空闲", Count: "租约 0"}
	}
	return onUI(func() barStateDTO {
		a := api.a
		state := "idle"
		if a.snap.Err != "" && !a.snap.Holding {
			state = "err"
		} else if a.snap.Holding {
			state = "on"
		}
		return barStateDTO{
			State:   state,
			Label:   a.statusLabel(),
			Count:   leaseCountText(activeCount(a.snap.Leases)),
			Holding: a.snap.Holding,
			Pulse:   a.cfg.Pulse,
		}
	})
}

// barStateEvent Go 主动把小条状态推给页面（不依赖页面定时器，避免被节流）
var barStateEvent = mygo.NewEvent[barStateDTO]("bar-state")

// pushBarState 把当前状态推给任务胶囊页面（主线程调用）
func (a *app) pushBarState() {
	if a.bar == nil {
		return
	}
	_ = barStateEvent.Emit(a.bar, Api{a: a}.BarState())
}

// BarDebug 前端调试输出（排查小条状态是否送达）
func (api Api) BarDebug(msg string) bool {
	dbg("%s", "bar: "+msg)
	return true
}

// TaskbarColor 任务栏底色（小条页面用它当背景，消除圆角外的边角）
func (api Api) TaskbarColor() string {
	return sampleTaskbarColor()
}

// OpenPanel 小条被点击时打开面板
func (api Api) OpenPanel() bool {
	if api.a == nil {
		return false
	}
	return onUI(func() bool {
		api.a.showPanel()
		return true
	})
}

// InitialToast 仅在设置环境变量时返回演示文案，用于视觉验收
func (api Api) InitialToast() string {
	if api.a == nil {
		return ""
	}
	return onUI(func() string { return api.a.debugToast })
}

// defaultAgentPromptTemplate 标题栏「复制提示词」的默认内容（纯英文，给 agent 读）。
// %d 是 agent 租约的兜底上限（小时）。
const defaultAgentPromptTemplate = `AgentAwake keeps this Windows PC awake while you work. It only prevents sleep and hibernation; the display still turns off normally.

Declare a lease before long-running work:

    awake acquire -name "<short-task-name>" -followpid <YOUR_PID> -json

The JSON result contains "id" (the full lease id) and "key" (a short key for this lease). Keep the key with your task.

When the work ends, release only your own lease, in both success and failure paths:

    awake release -key <KEY>

You can also release by the full id:

    awake release -id <ID>

Never use "awake release -all" or "awake release -name ...": they can release other agents' leases. Do not edit or delete lease files by hand.

A lease also ends automatically when the bound process exits, or when the maximum lifetime expires (currently %d hours; adjustable in AgentAwake settings). Check state with:

    awake status -json

awake.exe ships next to the AgentAwake panel; add its folder to PATH or call it by its full path.
`

// defaultAgentPrompt 按当前设置生成默认提示词
func defaultAgentPrompt(maxHours int) string {
	if maxHours <= 0 {
		maxHours = 12
	}
	return fmt.Sprintf(defaultAgentPromptTemplate, maxHours)
}

// Prompt 返回当前提示词：用户改过就用用户的，否则用内置默认
func (api Api) Prompt() string {
	if api.a == nil {
		return defaultAgentPrompt(12)
	}
	return onUI(func() string {
		if p := strings.TrimSpace(api.a.cfg.AgentPrompt); p != "" {
			return p
		}
		return defaultAgentPrompt(api.a.cfg.MaxHours)
	})
}

// SetPrompt 保存用户编辑过的提示词（空字符串表示恢复默认）
func (api Api) SetPrompt(text string) bool {
	if api.a == nil {
		return false
	}
	text = strings.TrimSpace(text)
	return onUI(func() bool {
		api.a.cfg.AgentPrompt = text
		savePanelConfig(api.a.cfg)
		return true
	})
}

// DefaultPrompt 返回内置默认提示词（只读取，不修改已保存内容）
func (api Api) DefaultPrompt() string {
	if api.a == nil {
		return defaultAgentPrompt(12)
	}
	return onUI(func() string { return defaultAgentPrompt(api.a.cfg.MaxHours) })
}

// CopyText 写入系统剪贴板（WebView 里 navigator.clipboard 不一定可用）
func (api Api) CopyText(text string) bool {
	if text == "" {
		return false
	}
	return onUI(func() bool {
		mygo.Clipboard.WriteText(text)
		return true
	})
}

const (
	panelWidth     = 420
	panelMinHeight = 240
	// 首页和设置页共用的最大高度（DIP）：取原来首页上限 544 与设置页上限 800 的中间值。
	// 首页随租约增多最多长到这里，设置页也最多长到这里，再长就整页滚动。
	panelMaxHeight = 672
	// settingsHeight 是设置页的首帧高度；页面量好真实内容高度后会调用
	// FitHeight 修正，避免窗口打开瞬间底部被裁掉。
	settingsHeight = 512
)

// SetPage 切换页面时同步调整窗口高度（状态页按租约数，设置页按内容）
func (api Api) SetPage(name string) bool {
	if api.a == nil {
		return false
	}
	return onUI(func() bool {
		if api.a.win == nil {
			return false
		}
		if name == "settings" {
			api.a.page = pageSettings
			h := api.a.settingsH
			if h <= 0 {
				h = settingsHeight
			}
			api.a.win.SetSize(panelWidth, h)
		} else {
			api.a.page = pageStatus
			api.a.win.SetSize(panelWidth, api.a.statusHeight())
		}
		return true
	})
}

// FitHeight 由设置页测量内容高度后调用：窗口贴合内容，底部不再被裁掉。
// 只在设置页生效，并限制在屏幕工作区内，避免页面量出异常值时窗口失控。
func (api Api) FitHeight(h int) bool {
	if api.a == nil {
		return false
	}
	return onUI(func() bool {
		if api.a.win == nil || api.a.page != pageSettings {
			return false
		}
		h = clampInt(h, panelMinHeight, panelMaxHeight)
		if d := mygo.Screen.PrimaryDisplay(); d.WorkArea.Height > 0 {
			if maxH := d.WorkArea.Height - 8; h > maxH {
				h = maxH
			}
		}
		api.a.settingsH = h
		api.a.win.SetSize(panelWidth, h)
		return true
	})
}

// statusHeight 状态页高度 = 固定区 + 租约行数，和设置页共用 panelMaxHeight。
// 操作进行中用 reservedRows 先占一行，窗口提前长高，结果出现时不再跳。
func (a *app) statusHeight() int {
	n := len(a.snap.Leases) + a.reservedRows
	h := 352 + n*64
	if h > panelMaxHeight {
		h = panelMaxHeight
	}
	return h
}

// fitPanel 状态变化后按内容调整窗口高度
func (a *app) fitPanel() {
	if a.win == nil || a.page == pageSettings {
		return
	}
	a.win.SetSize(panelWidth, a.statusHeight())
}

// ---------- 动作（同步执行，错误回传给页面） ----------

func (api Api) HoldMinutes(m int) error {
	if m <= 0 {
		m = 30
	}
	return api.hold("hold", "-For", fmt.Sprintf("%dm", m), "-Name", "manual")
}

func (api Api) HoldUntil(hhmm string) error {
	if hhmm == "" {
		hhmm = "06:00"
	}
	return api.hold("hold", "-Until", hhmm, "-Name", "manual")
}

func (api Api) Release(id string) error {
	return api.run("release", "-Id", id)
}

func (api Api) Stop() error {
	return api.run("stop")
}

// hold 先预占一行让窗口长高，再执行 CLI；结果返回时用真实租约数替换预占，
// 所以“保持中”的窗口高度在任务出现前就已经到位，不会再卡一下。
func (api Api) hold(args ...string) error {
	api.a.update(func() {
		api.a.reservedRows++
		api.a.fitPanel()
	})
	out, err := runCLI(args...)
	return api.finish(out, err, true)
}

// run 执行一次 CLI 动作（释放/停止），完成后立即按最新租约数调整高度。
func (api Api) run(args ...string) error {
	out, err := runCLI(args...)
	return api.finish(out, err, false)
}

// finish 刷新快照、吃掉预留行，并把失败时 CLI 的输出交给页面显示。
// 增高：预占行已经先把窗口长高，这里只需替换成真实租约，不再跳。
// 缩短：等页面把行删掉（约 80ms）再收窗口，避免先缩窗口留下空白。
func (api Api) finish(out string, err error, reserved bool) error {
	snap := readSnapshot()
	grew := reserved && err == nil
	api.a.update(func() {
		if reserved && api.a.reservedRows > 0 {
			api.a.reservedRows--
		}
		api.a.snap = snap
		if grew {
			api.a.fitPanel()
		}
	})
	if !grew {
		time.AfterFunc(80*time.Millisecond, func() {
			api.a.update(api.a.fitPanel)
		})
	}
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(out); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return err
}

func (api Api) SetOffset(px int) bool {
	return onUI(func() bool {
		api.a.cfg.OffsetX = clampInt(px, -1400, 1400)
		savePanelConfig(api.a.cfg)
		api.a.applyPosition()
		return true
	})
}

func (api Api) SetShowBar(v bool) bool {
	return onUI(func() bool {
		api.a.cfg.ShowBar = v
		savePanelConfig(api.a.cfg)
		api.a.applyBarVisibility()
		return true
	})
}

func (api Api) SetPulse(v bool) bool {
	return onUI(func() bool {
		api.a.cfg.Pulse = v
		savePanelConfig(api.a.cfg)
		return true
	})
}

func (api Api) SetMaxHours(h int) bool {
	return onUI(func() bool {
		api.a.cfg.MaxHours = clampInt(h, 1, 24)
		savePanelConfig(api.a.cfg)
		return true
	})
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- 供界面/原生共用的辅助 ----------

func pidText(pid int) string {
	if pid == 0 {
		return "未运行"
	}
	return fmt.Sprintf("PID %d", pid)
}

func leaseDetailText(l leaseView) string {
	parts := ""
	if l.Key != "" {
		parts = "密钥 " + l.Key
	}
	if l.Mode == "deadline" && l.Active {
		if parts != "" {
			parts += " · "
		}
		return parts + "剩 " + spanText(l.Remaining)
	}
	if l.Mode == "release" {
		if l.PID != 0 {
			if parts != "" {
				parts += " · "
			}
			parts += fmt.Sprintf("进程 %d（%s）", l.PID, l.ProcessName)
		}
		if l.MaxUntil != "" {
			if tm, err := time.Parse(time.RFC3339Nano, l.MaxUntil); err == nil {
				if parts != "" {
					parts += " · "
				}
				parts += "最长到 " + tm.Format("01-02 15:04")
			}
		}
	}
	return parts
}

// applyPosition 把位置改动立刻反映到任务胶囊（主线程调用）
func (a *app) applyPosition() {
	if a.bar == nil {
		return
	}
	switch {
	case a.embedded:
		a.positionInTaskbar()
	case a.overTaskbar:
		a.positionOverTaskbar()
	default:
		a.placeTrafficBar()
	}
}

// applyBarVisibility 显示/隐藏任务胶囊（主线程调用）
func (a *app) applyBarVisibility() {
	if a.bar == nil {
		return
	}
	if a.cfg.ShowBar {
		a.barSuppressed = false
		a.applyPosition()
		a.bar.ShowInactive()
	} else {
		a.bar.Hide()
	}
}
