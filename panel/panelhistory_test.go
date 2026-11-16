package main

// panelhistory_test.go — 历史读取与展示格式化

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadHistoryEmpty(t *testing.T) {
	inTempDataDir(t)
	if rows := readHistory(); len(rows) != 0 {
		t.Errorf("无文件应为 0 条，得到 %d", len(rows))
	}
}

func TestReadHistoryParsesJSONL(t *testing.T) {
	dir := inTempDataDir(t)
	content := `{"id":"a1","key":"ABC234","name":"job","mode":"release","createdAt":"2026-11-16T10:00:00+08:00","endedAt":"2026-11-16T10:30:00+08:00","endReason":"released","durationSec":1800}
坏行不是 JSON
{"id":"a2","name":"manual","mode":"deadline","createdAt":"2026-11-15T22:00:00+08:00","endedAt":"2026-11-15T22:30:00+08:00","endReason":"deadline","durationSec":1800}
`
	// dataDir 会追加 AgentAwake 段，账本在它下面（目录由被测代码的 ensure 逻辑建，
	// 这里手动建一份避免 WriteFile 失败）
	if err := os.MkdirAll(filepath.Join(dir, "AgentAwake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AgentAwake", "history.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := readHistory()
	if len(rows) != 2 {
		t.Fatalf("坏行应跳过，应得 2 条，得到 %d", len(rows))
	}
	if rows[0].ID != "a1" || rows[0].EndReason != "released" {
		t.Errorf("第一条不对：%+v", rows[0])
	}
}

func TestHistoryReasonTextAndKind(t *testing.T) {
	if got := historyReasonText("max-lifetime"); got != "达到上限" {
		t.Errorf("got %q", got)
	}
	if historyReasonKind("max-lifetime") != "warn" || historyReasonKind("heartbeat-timeout") != "warn" {
		t.Error("上限与心跳超时应为 warn")
	}
	if historyReasonKind("released") != "" {
		t.Error("释放应为中性")
	}
}

func TestHistoryTimeText(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.Local)
	today := time.Date(2026, 11, 16, 14, 35, 0, 0, time.Local)
	other := time.Date(2026, 11, 15, 22, 0, 0, 0, time.Local)

	if got := historyTimeText(stampTime(today), now); got != "14:35" {
		t.Errorf("当天应只显示时刻，得到 %q", got)
	}
	if got := historyTimeText(stampTime(other), now); got != "11-15 22:00" {
		t.Errorf("跨天应带日期，得到 %q", got)
	}
	if got := historyTimeText("garbage", now); got != "garbage" {
		t.Errorf("解析失败应原样返回，得到 %q", got)
	}
}

func TestHistoryDurationText(t *testing.T) {
	cases := map[int64]string{
		30:      "30 秒",
		600:     "10 分",
		5700:    "1 小时 35 分",
		180000:  "2.1 天",
	}
	for sec, want := range cases {
		if got := historyDurationText(sec); got != want {
			t.Errorf("historyDurationText(%d) = %q, want %q", sec, got, want)
		}
	}
}

// stampTime 测试辅助：time.Time → RFC3339Nano 字符串
func stampTime(t time.Time) string { return t.Format(time.RFC3339Nano) }
