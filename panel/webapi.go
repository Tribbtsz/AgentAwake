package main

// webapi.go — 面板的 Go 侧：内嵌前端 + 暴露给 JS 的接口
//
// 界面是 HTML/CSS（web/），通过 window.mygo.call("Api.Xxx") 调用这里。
// 托盘、任务栏任务胶囊、状态轮询仍然是原生 Go。

import (
	"embed"
	"fmt"
	"io/fs"
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

// ---------- 给前端的 DTO ----------

type leaseDTO struct {
	ID     string `json:"id"`
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
			Name:   l.Name,
			Mode:   l.Mode,
			Active: l.Active,
			Detail: leaseDetailText(l),
		})
	}
	return dto
}

// InitialPage 启动时显示哪一页（--settings 直接进设置）
func (api Api) InitialPage() string {
	if api.a != nil && api.a.startPage != "" {
		return api.a.startPage
	}
	return "status"
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
}

// barStateEvent Go 主动把小条状态推给页面（不依赖页面定时器，避免被节流）
var barStateEvent = mygo.NewEvent[barStateDTO]("bar-state")

// pushBarState 把当前状态推给任务胶囊页面
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
	api.a.showPanel()
	return true
}

// InitialToast 仅在设置环境变量时返回演示文案，用于视觉验收
func (api Api) InitialToast() string {
	if api.a == nil {
		return ""
	}
	return api.a.debugToast
}

const (
	panelWidth     = 420
	panelMinHeight = 240
	panelMaxHeight = 900
	// settingsHeight 是设置页的首帧高度；页面量好真实内容高度后会调用
	// FitHeight 修正，避免窗口打开瞬间底部被裁掉。
	settingsHeight = 512
)

// SetPage 切换页面时同步调整窗口高度（状态页按租约数，设置页按内容）
func (api Api) SetPage(name string) bool {
	if api.a == nil || api.a.win == nil {
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
}

// FitHeight 由设置页测量内容高度后调用：窗口贴合内容，底部不再被裁掉。
// 只在设置页生效，并限制在合理区间内，避免页面量出异常值时窗口失控。
func (api Api) FitHeight(h int) bool {
	if api.a == nil || api.a.win == nil || api.a.page != pageSettings {
		return false
	}
	h = clampInt(h, panelMinHeight, panelMaxHeight)
	api.a.settingsH = h
	api.a.win.SetSize(panelWidth, h)
	return true
}

// statusHeight 状态页高度 = 固定区 + 租约行数（最多算 3 行）
func (a *app) statusHeight() int {
	n := len(a.snap.Leases)
	if n > 3 {
		n = 3
	}
	return 352 + n*64
}

// fitPanel 状态变化后按内容调整窗口高度
func (a *app) fitPanel() {
	if a.win == nil || a.page == pageSettings {
		return
	}
	a.win.SetSize(panelWidth, a.statusHeight())
}

// ---------- 动作（异步执行，避免阻塞界面） ----------

func (api Api) HoldMinutes(m int) string {
	if m <= 0 {
		m = 30
	}
	api.a.runAction(fmt.Sprintf("%dm", m))
	return "ok"
}

func (api Api) HoldUntil(hhmm string) string {
	if hhmm == "" {
		hhmm = "06:00"
	}
	api.a.runActionUntil(hhmm)
	return "ok"
}

func (api Api) Release(id string) string {
	api.a.runRelease(id)
	return "ok"
}

func (api Api) Stop() string {
	api.a.runStop()
	return "ok"
}

func (api Api) SetOffset(px int) bool {
	api.a.cfg.OffsetX = clampInt(px, -1400, 1400)
	savePanelConfig(api.a.cfg)
	api.a.applyPosition()
	return true
}

func (api Api) SetShowBar(v bool) bool {
	api.a.cfg.ShowBar = v
	savePanelConfig(api.a.cfg)
	api.a.applyBarVisibility()
	return true
}

func (api Api) SetPulse(v bool) bool {
	api.a.cfg.Pulse = v
	savePanelConfig(api.a.cfg)
	return true
}

func (api Api) SetMaxHours(h int) bool {
	api.a.cfg.MaxHours = clampInt(h, 1, 24)
	savePanelConfig(api.a.cfg)
	return true
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

// runAction 执行 CLI 动作并刷新状态（后台线程）
func (a *app) runAction(forSpec string) {
	go func() {
		out, err := runCLI("hold", "-For", forSpec, "-Name", "manual")
		a.reportResult(out, err)
		a.update(func() { a.snap = readSnapshot() })
	}()
}

func (a *app) runActionUntil(hhmm string) {
	go func() {
		out, err := runCLI("hold", "-Until", hhmm, "-Name", "manual")
		a.reportResult(out, err)
		a.update(func() { a.snap = readSnapshot() })
	}()
}

func (a *app) runRelease(id string) {
	go func() {
		out, err := runCLI("release", "-Id", id)
		a.reportResult(out, err)
		a.update(func() { a.snap = readSnapshot() })
	}()
}

func (a *app) runStop() {
	go func() {
		out, err := runCLI("stop")
		a.reportResult(out, err)
		a.update(func() { a.snap = readSnapshot() })
	}()
}

func (a *app) reportResult(out string, err error) {
	a.update(func() {
		if err != nil {
			a.err = fmt.Sprintf("%v\n%s", err, out)
		} else {
			a.err = ""
		}
	})
}

// ---------- 供界面/原生共用的辅助 ----------

func pidText(pid int) string {
	if pid == 0 {
		return "未运行"
	}
	return fmt.Sprintf("PID %d", pid)
}

func leaseDetailText(l leaseView) string {
	if l.Mode == "deadline" && l.Active {
		return "剩 " + spanText(l.Remaining)
	}
	if l.Mode == "release" {
		parts := ""
		if l.PID != 0 {
			parts = fmt.Sprintf("进程 %d（%s）", l.PID, l.ProcessName)
		}
		if l.MaxUntil != "" {
			if tm, err := time.Parse(time.RFC3339Nano, l.MaxUntil); err == nil {
				if parts != "" {
					parts += " · "
				}
				parts += "最长到 " + tm.Format("01-02 15:04")
			}
		}
		return parts
	}
	return ""
}

// applyPosition 把位置改动立刻反映到任务胶囊
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

func (a *app) applyBarVisibility() {
	if a.bar == nil {
		return
	}
	if a.cfg.ShowBar {
		a.applyPosition()
		a.bar.ShowInactive()
	} else {
		a.bar.Hide()
	}
}
