package main

// panelhistory.go — 面板侧的租约历史读取（只读 cli 写的 history.jsonl）
//
// 面板与 cli 是两个 Go 模块，这里只定义读取所需的字段（与 cli/history.go 的
// HistoryEntry 对齐），格式化成展示文本也在这边做（与 leaseDetailText 同约定：
// Go 算好、JS 只渲染）。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// historyEntry 与 cli 的 HistoryEntry 字段对齐
type historyEntry struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	PID         int    `json:"pid"`
	Process     string `json:"process"`
	CreatedAt   string `json:"createdAt"`
	EndedAt     string `json:"endedAt"`
	EndReason   string `json:"endReason"`
	DurationSec int64  `json:"durationSec"`
}

// historyPath 历史账本位置（cli 写、面板读/清）
func historyPath() string { return filepath.Join(dataDir(), "history.jsonl") }

// clearHistoryLocal 清空历史（面板「清空」按钮用；与 cli 包的 clearHistory 同义，
// 但两个模块各自编译，名字不冲突——这里加 Local 后缀区分）
func clearHistoryLocal() {
	_ = os.Remove(historyPath())
}

// readHistory 读全部历史（最旧在前）；坏行跳过。文件不存在 = 没有历史。
func readHistory() []historyEntry {
	f, err := os.Open(historyPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []historyEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e historyEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// historyReasonText 结束原因的中文文案（与 cli 侧保持一致）
func historyReasonText(reason string) string {
	switch reason {
	case "released":
		return "已释放"
	case "stopped":
		return "手动停止"
	case "deadline":
		return "到期结束"
	case "max-lifetime":
		return "达到上限"
	case "process-exited":
		return "进程退出"
	case "heartbeat-timeout":
		return "心跳超时"
	}
	return reason
}

// historyReasonKind 结束原因的展示类别：中性 / warn（达到上限、心跳超时值得留意）
func historyReasonKind(reason string) string {
	switch reason {
	case "max-lifetime", "heartbeat-timeout":
		return "warn"
	}
	return ""
}

// historyTimeText 历史列表的时间显示：当天只留时刻，跨天带日期
func historyTimeText(s string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// historyDurationText 人类可读的持续时长（口径与 cli 一致）
func historyDurationText(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d.Hours() >= 48:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	case d.Hours() >= 1:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	case d.Minutes() >= 1:
		return fmt.Sprintf("%d 分", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
}
