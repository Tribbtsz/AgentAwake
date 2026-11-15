package main

// main_test.go — 命令行核心逻辑的单元测试（时间解析、参数解析、租约判定）

import (
	"os"
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	cases := []struct {
		spec string
		want time.Time
	}{
		{"30m", now.Add(30 * time.Minute)},
		{"30分", now.Add(30 * time.Minute)},
		{"30分钟", now.Add(30 * time.Minute)},
		{"90s", now.Add(90 * time.Second)},
		{"1.5h", now.Add(90 * time.Minute)},
		{"2h", now.Add(2 * time.Hour)},
		{"2小时", now.Add(2 * time.Hour)},
		{"2d", now.Add(48 * time.Hour)},
		{"+45m", now.Add(45 * time.Minute)},
		{"18:00", time.Date(2026, 11, 15, 18, 0, 0, 0, time.Local)},
		{"06:00", time.Date(2026, 11, 16, 6, 0, 0, 0, time.Local)}, // 已过 → 明天
		{"2026-11-15T18:30", time.Date(2026, 11, 15, 18, 30, 0, 0, time.Local)},
	}
	for _, c := range cases {
		got, err := parseSpec(c.spec, now)
		if err != nil {
			t.Fatalf("parseSpec(%q) 报错: %v", c.spec, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSpec(%q) = %v，期望 %v", c.spec, got, c.want)
		}
	}

	for _, bad := range []string{"", "明天", "30x"} {
		if _, err := parseSpec(bad, now); err == nil {
			t.Errorf("parseSpec(%q) 应当报错", bad)
		}
	}
}

func TestParseArgs(t *testing.T) {
	o, pos, err := parseArgs([]string{"hold", "-For", "30m", "-Name", "build", "-Json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "hold" {
		t.Fatalf("命令解析错误: %v", pos)
	}
	if o.forSpec != "30m" || o.name != "build" || !o.jsonOut {
		t.Fatalf("参数解析错误: %+v", o)
	}

	// = 形式 + 大小写不敏感
	o, _, err = parseArgs([]string{"acquire", "-for=2h", "-FOLLOWPID=42"})
	if err != nil {
		t.Fatal(err)
	}
	if o.forSpec != "2h" || o.followPID != 42 {
		t.Fatalf("= 形式解析错误: %+v", o)
	}

	if _, _, err := parseArgs([]string{"status", "-nope"}); err == nil {
		t.Error("未知参数应当报错")
	}
}

func TestInactiveReason(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)

	live := Lease{Mode: "deadline", Deadline: stamp(now.Add(time.Hour)), MaxUntil: stamp(now.Add(2 * time.Hour))}
	if r := inactiveReason(live, now); r != "" {
		t.Errorf("有效租约被判为失效: %s", r)
	}

	expired := live
	expired.Deadline = stamp(now.Add(-time.Minute))
	if r := inactiveReason(expired, now); r != "deadline" {
		t.Errorf("到期租约判为 %q", r)
	}

	capped := live
	capped.MaxUntil = stamp(now.Add(-time.Second))
	if r := inactiveReason(capped, now); r != "max-lifetime" {
		t.Errorf("超过硬上限判为 %q", r)
	}

	released := live
	released.Released = true
	if r := inactiveReason(released, now); r != "released" {
		t.Errorf("已释放判为 %q", r)
	}

	// release 模式：绑定的进程已经不在
	gone := Lease{Mode: "release", MaxUntil: stamp(now.Add(time.Hour)), PID: 999999}
	if r := inactiveReason(gone, now); r != "process-exited" {
		t.Errorf("进程退出判为 %q", r)
	}

	// release 模式：绑定自己（一定活着）
	alive := Lease{Mode: "release", MaxUntil: stamp(now.Add(time.Hour)), PID: os.Getpid()}
	if r := inactiveReason(alive, now); r != "" {
		t.Errorf("存活进程租约判为 %q", r)
	}

	// 心跳超时
	heartbeat := Lease{
		Mode:       "release",
		MaxUntil:   stamp(now.Add(time.Hour)),
		TTLSeconds: 60,
		LastBeat:   stamp(now.Add(-2 * time.Minute)),
		CreatedAt:  stamp(now.Add(-10 * time.Minute)),
	}
	if r := inactiveReason(heartbeat, now); r != "heartbeat-timeout" {
		t.Errorf("心跳超时判为 %q", r)
	}
}

func TestFormatSpan(t *testing.T) {
	cases := map[time.Duration]string{
		-1 * time.Second:             "已到期",
		90 * time.Second:             "2 分",
		45 * time.Minute:             "45 分",
		2*time.Hour + 30*time.Minute: "2 小时 30 分",
		72 * time.Hour:               "3.0 天",
	}
	for d, want := range cases {
		if got := formatSpan(d); got != want {
			t.Errorf("formatSpan(%v) = %q，期望 %q", d, got, want)
		}
	}
}

func TestPadRight(t *testing.T) {
	if got := padRight("ab", 6); got != "ab    " {
		t.Errorf("padRight 英文字符错误: %q", got)
	}
	// 中文按 2 格算：2 个汉字 = 4 格，补 2 个空格
	if got := padRight("租约", 6); got != "租约  " {
		t.Errorf("padRight 中文字符错误: %q", got)
	}
	if got := padRight("abcdef", 3); got != "abcdef" {
		t.Errorf("超长不应截断: %q", got)
	}
}

func TestNewLeaseID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newLeaseID()
		if len(id) != 12 {
			t.Fatalf("租约 ID 长度应为 12，得到 %q", id)
		}
		if seen[id] {
			t.Fatalf("租约 ID 重复: %s", id)
		}
		seen[id] = true
	}
}
