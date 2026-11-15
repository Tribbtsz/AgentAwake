package main

// view_test.go — 状态与视图契约测试（界面现在由 HTML 渲染，这里守住数据契约）

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func newTestApp() *app {
	return &app{
		cfg: defaultConfig(),
		snap: snapshot{
			Holding: false,
			Leases:  nil,
			Power: powerValues{
				SleepAC:   300,
				SleepDC:   300,
				HibAC:     1200,
				HibDC:     1200,
				DisplayAC: 300,
				DisplayDC: 300,
			},
		},
	}
}

func TestBarStateIdle(t *testing.T) {
	a := newTestApp()
	s := Api{a: a}.BarState()
	if s.State != "idle" || s.Label != "空闲" {
		t.Fatalf("空闲时应为 idle/空闲，得到 %s/%s", s.State, s.Label)
	}
	if s.Count != "租约 0" {
		t.Fatalf("计数文案不对：%s", s.Count)
	}
}

func TestBarStateHolding(t *testing.T) {
	a := newTestApp()
	a.snap.Holding = true
	a.snap.Leases = []leaseView{
		{lease: lease{ID: "a1", Name: "job-1", Mode: "release"}, Active: true},
		{lease: lease{ID: "b2", Name: "job-2", Mode: "deadline"}, Active: true},
		{lease: lease{ID: "c3", Name: "job-3", Mode: "release"}, Active: false},
	}
	s := Api{a: a}.BarState()
	if s.State != "on" || s.Label != "保持中" {
		t.Fatalf("保持中应为 on/保持中，得到 %s/%s", s.State, s.Label)
	}
	if s.Count != "租约 2" {
		t.Fatalf("应只统计 2 个活跃租约，得到 %s", s.Count)
	}
}

func TestBarStateError(t *testing.T) {
	a := newTestApp()
	a.snap.Err = "cli failed"
	s := Api{a: a}.BarState()
	if s.State != "err" {
		t.Fatalf("有错误时应为 err，得到 %s", s.State)
	}
}

func TestBarWidthFitsContent(t *testing.T) {
	a := newTestApp()
	idle := a.barWidthFor()
	a.snap.Holding = true
	a.snap.Leases = []leaseView{{lease: lease{ID: "x", Name: "n", Mode: "release"}, Active: true}}
	holding := a.barWidthFor()
	if holding <= idle {
		t.Errorf("「保持中」比「空闲」宽，宽度应更大：idle=%d holding=%d", idle, holding)
	}
	if idle < 60 || holding > 280 {
		t.Errorf("宽度超出合理范围：%d / %d", idle, holding)
	}
}

// TestBarClickOpensPanel 胶囊是原生 ui 画的，点它要能打开面板。
// 用 headless Tester 走真实的事件路径（不需要窗口）。
func TestBarClickOpensPanel(t *testing.T) {
	a := newTestApp()
	a.barW = a.barWidthFor()
	tst := ui.NewTester(a.barView, a.barW, barHeight)
	tst.Frame()
	tst.ClickAt(float32(a.barW)/2, barHeight/2)
	if a.barUI.clicks != 1 {
		t.Fatalf("点击胶囊应触发一次打开面板，实际 %d 次", a.barUI.clicks)
	}
}

// TestBarViewStates 三种状态都能画出来，且状态切换会更新胶囊。
func TestBarViewStates(t *testing.T) {
	a := newTestApp()
	a.barW = a.barWidthFor()
	tst := ui.NewTester(a.barView, a.barW, barHeight)
	tst.Frame()

	if !a.updateBarState() {
		t.Fatal("首次刷新应报告状态变化")
	}
	for _, c := range []struct {
		name string
		set  func()
		want int
	}{
		{"空闲", func() {}, barIdle},
		{"保持中", func() { a.snap.Holding = true }, barOn},
		{"出错", func() {
			a.snap.Holding = false
			a.snap.Err = "cli failed"
		}, barErr},
	} {
		c.set()
		a.updateBarState()
		if a.barUI.kind != c.want {
			t.Errorf("%s 时应为 kind=%d，得到 %d", c.name, c.want, a.barUI.kind)
		}
		tst.Frame()
	}
}

func TestApiStateIdle(t *testing.T) {
	a := newTestApp()
	s := Api{a: a}.State()
	if s.Label != "空闲" {
		t.Errorf("空闲时 Label 应为「空闲」，得到 %q", s.Label)
	}
	if s.Leases == nil {
		t.Error("Leases 不应为 nil（前端会直接遍历）")
	}
	if s.MaxHours != 12 {
		t.Errorf("兜底上限默认应为 12 小时，得到 %d", s.MaxHours)
	}
}

func TestApiStateHolding(t *testing.T) {
	a := newTestApp()
	a.snap.Holding = true
	a.snap.SupervisorPID = 1234
	a.snap.Leases = []leaseView{
		{lease: lease{ID: "a1", Name: "job-1", Mode: "release", PID: 99, ProcessName: "pwsh"}, Active: true},
		{lease: lease{ID: "b2", Name: "job-2", Mode: "deadline"}, Active: false},
	}
	s := Api{a: a}.State()

	if s.Label != "保持中" {
		t.Errorf("有活跃租约时 Label 应为「保持中」，得到 %q", s.Label)
	}
	if len(s.Leases) != 2 {
		t.Fatalf("应返回 2 张租约，得到 %d", len(s.Leases))
	}
	if s.Leases[0].Mode != "release" || !s.Leases[0].Active {
		t.Error("agent 租约应标记为等完成且有效")
	}
	if s.Leases[1].Active {
		t.Error("过期租约应标记为失效")
	}
}

func TestLeaseCountText(t *testing.T) {
	cases := map[int]string{0: "租约 0", 1: "租约 1", 42: "租约 42"}
	for n, want := range cases {
		if got := leaseCountText(n); got != want {
			t.Errorf("leaseCountText(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestEvaluateLeaseRules(t *testing.T) {
	l := lease{Mode: "release", PID: 0, TTLSeconds: 0}
	if state, active, _ := evaluateLease(l, time.Now()); !active || state != "active" {
		t.Errorf("无 PID 无 TTL 的 release 租约应视为有效，得到 %s", state)
	}

	l2 := lease{Mode: "release", Released: true}
	if state, active, _ := evaluateLease(l2, time.Now()); active || state != "released" {
		t.Errorf("已释放的租约应失效，得到 %s", state)
	}
}

func TestStatusHeightReservedRows(t *testing.T) {
	a := newTestApp()
	if got := a.statusHeight(); got != 352 {
		t.Errorf("无租约时高度应为 352，得到 %d", got)
	}
	a.snap.Leases = []leaseView{{}, {}}
	if got := a.statusHeight(); got != 352+2*64 {
		t.Errorf("2 个租约时高度应为 %d，得到 %d", 352+2*64, got)
	}
	// 操作进行中先预占一行：任务还没出现，窗口已经长高
	a.reservedRows = 1
	if got := a.statusHeight(); got != 352+3*64 {
		t.Errorf("预占 1 行后高度应为 %d，得到 %d", 352+3*64, got)
	}
	// 首页和设置页共用上限，最多 672
	a.snap.Leases = make([]leaseView, 6)
	a.reservedRows = 0
	if got := a.statusHeight(); got != panelMaxHeight {
		t.Errorf("超过上限时应为 %d，得到 %d", panelMaxHeight, got)
	}
	if panelMaxHeight != 672 {
		t.Errorf("共用上限应为 672，得到 %d", panelMaxHeight)
	}
	a.reservedRows = 3
	if got := a.statusHeight(); got != panelMaxHeight {
		t.Errorf("预占行也不应突破上限，得到 %d", got)
	}
}

func TestPromptDefaultAndCustom(t *testing.T) {
	a := newTestApp()
	api := Api{a: a}

	p := api.Prompt()
	if !strings.Contains(p, "awake release -key") {
		t.Errorf("默认提示词应包含按密钥释放的说明，得到：\n%s", p)
	}
	if !strings.Contains(p, "awake acquire") {
		t.Errorf("默认提示词应包含申请租约的说明")
	}
	if !strings.Contains(p, "12 hours") {
		t.Errorf("默认提示词应写入当前兜底上限，得到：\n%s", p)
	}

	a.cfg.MaxHours = 7
	if p := api.Prompt(); !strings.Contains(p, "7 hours") {
		t.Errorf("修改上限后提示词应更新，得到：\n%s", p)
	}

	a.cfg.AgentPrompt = "  custom prompt  "
	if got := api.Prompt(); got != "custom prompt" {
		t.Errorf("应使用自定义提示词，得到 %q", got)
	}
}

func TestLeaseDetailShowsKey(t *testing.T) {
	l := leaseView{lease: lease{ID: "a1", Key: "ABC234", Mode: "release"}, Active: true}
	if got := leaseDetailText(l); !strings.Contains(got, "ABC234") {
		t.Errorf("租约详情应显示密钥，得到 %q", got)
	}
	d := leaseView{lease: lease{Key: "XYZ789", Mode: "deadline"}, Active: true, Remaining: 90 * time.Second}
	if got := leaseDetailText(d); !strings.Contains(got, "XYZ789") || !strings.Contains(got, "剩") {
		t.Errorf("定时租约详情应包含密钥与剩余时间，得到 %q", got)
	}
}
