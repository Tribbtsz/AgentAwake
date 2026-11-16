package main

// history_test.go — 租约历史账本的写入、修剪与展示规则

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	ensureDirs()
	return dir
}

func TestAppendAndLoadHistory(t *testing.T) {
	historyTestDir(t)

	start := time.Now().Add(-30 * time.Minute)
	l := Lease{ID: "a1", Key: "ABC234", Name: "job", Mode: "release",
		CreatedAt: stamp(start), PID: 42, ProcessName: "pwsh"}
	recordLeaseEnd(l, time.Now(), "released")

	all := loadHistory()
	if len(all) != 1 {
		t.Fatalf("应记录 1 条，得到 %d", len(all))
	}
	e := all[0]
	if e.ID != "a1" || e.Key != "ABC234" || e.EndReason != "released" {
		t.Errorf("字段不对：%+v", e)
	}
	if e.DurationSec < 29*60 || e.DurationSec > 31*60 {
		t.Errorf("持续时长应在 30 分附近，得到 %d 秒", e.DurationSec)
	}
}

// 定时租约的结束时刻用真实到期点，不是发现时刻
func TestLeaseEndTimeUsesDeadline(t *testing.T) {
	now := time.Now()
	deadline := now.Add(-10 * time.Minute)
	l := Lease{ID: "d1", Mode: "deadline", Deadline: stamp(deadline),
		MaxUntil: stamp(deadline.Add(time.Hour)), CreatedAt: stamp(now.Add(-time.Hour))}

	got := leaseEndTime(l, now, "deadline")
	if !got.Equal(deadline) {
		t.Errorf("deadline 的结束时刻应为到期点，差了 %v", got.Sub(deadline))
	}

	got = leaseEndTime(l, now, "max-lifetime")
	if !got.Equal(deadline.Add(time.Hour)) {
		t.Errorf("max-lifetime 的结束时刻应为 maxUntil")
	}

	got = leaseEndTime(l, now, "process-exited")
	if !got.Equal(now) {
		t.Errorf("进程退出的结束时刻应为发现时刻")
	}
}

// 没有合法申请时间的记录直接跳过，不写废行
func TestRecordLeaseEndSkipsBadStart(t *testing.T) {
	historyTestDir(t)
	l := Lease{ID: "bad", CreatedAt: "not-a-time"}
	recordLeaseEnd(l, time.Now(), "released")
	if len(loadHistory()) != 0 {
		t.Error("CreatedAt 解析失败时不应记录")
	}
}

// 超过上限时修剪最旧的
func TestHistoryTrim(t *testing.T) {
	historyTestDir(t)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < historyCap+30; i++ {
		l := Lease{ID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Name: "x",
			CreatedAt: stamp(base.Add(time.Duration(i) * time.Second))}
		recordLeaseEnd(l, base.Add(time.Duration(i)*time.Second+time.Minute), "released")
	}
	all := loadHistory()
	if len(all) != historyCap {
		t.Fatalf("修剪后应剩 %d 条，得到 %d", historyCap, len(all))
	}
	// 留下的应该是最新的
	if all[len(all)-1].ID == all[0].ID && all[0].CreatedAt > all[len(all)-1].CreatedAt {
		t.Error("修剪应保留最新记录")
	}
}

// 文件确实是 JSON Lines：每行独立可解析
func TestHistoryIsJSONLines(t *testing.T) {
	historyTestDir(t)
	for i := 0; i < 3; i++ {
		l := Lease{ID: fmt.Sprintf("id%d", i), Name: "n",
			CreatedAt: stamp(time.Now().Add(-time.Duration(i) * time.Minute))}
		recordLeaseEnd(l, time.Now(), "released")
	}
	b, err := os.ReadFile(filepath.Join(dataDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 行，得到 %d", len(lines))
	}
	for i, line := range lines {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			t.Errorf("第 %d 行不是合法 JSON：%s", i+1, line)
		}
	}
}

func TestClearHistory(t *testing.T) {
	historyTestDir(t)
	l := Lease{ID: "x", CreatedAt: stamp(time.Now())}
	recordLeaseEnd(l, time.Now(), "released")
	clearHistory()
	if len(loadHistory()) != 0 {
		t.Error("清空后应为 0 条")
	}
}

func TestFormatHistoryDuration(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{30, "30 秒"},
		{10 * 60, "10 分"},
		{95 * 60, "1 小时 35 分"},
		{50 * 3600, "2.1 天"},
	}
	for _, c := range cases {
		if got := formatHistoryDuration(c.sec); got != c.want {
			t.Errorf("formatHistoryDuration(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestHistoryReasonText(t *testing.T) {
	if got := historyReasonText("max-lifetime"); got != "达到上限" {
		t.Errorf("got %q", got)
	}
	if got := historyReasonText("released"); got != "已释放" {
		t.Errorf("got %q", got)
	}
}

// 绑定 PID 时 TTL 也要生效：进程活着但卡死时靠心跳超时兜底
func TestTTLWithPIDBound(t *testing.T) {
	now := time.Now()
	l := Lease{
		Mode: "release", MaxUntil: stamp(now.Add(time.Hour)),
		PID: os.Getpid(), PIDStart: 0, // 0 = 不校验创建时间，测试里用
		TTLSeconds: 60, LastBeat: stamp(now.Add(-2 * time.Minute)),
		CreatedAt: stamp(now.Add(-10 * time.Minute)),
	}
	if r := inactiveReason(l, now); r != "heartbeat-timeout" {
		t.Errorf("PID 活着但心跳超时应失效，得到 %q", r)
	}
	// 心跳新鲜时仍有效
	l.LastBeat = stamp(now)
	if r := inactiveReason(l, now); r != "" {
		t.Errorf("心跳新鲜时不应失效，得到 %q", r)
	}
}
