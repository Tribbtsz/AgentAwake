package main

// view_test.go — 状态与视图契约测试（界面现在由 HTML 渲染，这里守住数据契约）

import (
	"strings"
	"testing"
	"time"
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
