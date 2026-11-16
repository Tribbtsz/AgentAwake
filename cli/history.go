package main

// history.go — 租约历史账本（history.jsonl，JSON Lines 追加写）
//
// 租约文件在结束时就删掉了，想知道「昨晚那个 build 从几点跑到几点」没有依据。
// 这里补一个只追加的账本：每次租约结束写一行完整记录（申请时间、结束时间、
// 持续时长、结束原因），新建不写——申请时刻本来就在租约文件里，结束才成其为
// 「历史」。
//
// 为什么独立于 awake.log：日志是四种来源混写的事件流（还有轮转），机器要从中
// 拼出一条租约的生命周期得跨行匹配；账本一行一条、永不轮转，CLI 与面板都直接读。
//
// 并发与 awake.log 同一模式：一行一次 write、O_APPEND，三个进程同时追加
// 不会互相截断。上限 500 条：写前修剪最旧的，文件最大几百 KB。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// historyCap 账本最多保留多少条；写前修剪最旧的
const historyCap = 500

// HistoryEntry 一条已结束租约的完整生命周期
type HistoryEntry struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Mode        string `json:"mode"` // deadline | release
	PID         int    `json:"pid,omitempty"`
	Process     string `json:"process,omitempty"`
	CreatedAt   string `json:"createdAt"`
	EndedAt     string `json:"endedAt"`
	EndReason   string `json:"endReason"` // released | stopped | deadline | max-lifetime | process-exited | heartbeat-timeout
	DurationSec int64  `json:"durationSec"`
}

func historyPath() string { return filepath.Join(dataDir(), "history.jsonl") }

// appendHistory 追加一条历史并修剪超限的旧记录。失败不影响主流程（账本是锦上添花）。
func appendHistory(e HistoryEntry) {
	ensureDirs()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()

	trimHistory()
}

// loadHistory 读全部历史（最旧在前）。坏行跳过，不中断。
func loadHistory() []HistoryEntry {
	f, err := os.Open(historyPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []HistoryEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e HistoryEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// trimHistory 超过上限时重写文件，只留最新的 historyCap 条。
// 只在 appendHistory 后调用；读写竞态最坏丢一条新增，可接受。
func trimHistory() {
	all := loadHistory()
	if len(all) <= historyCap {
		return
	}
	all = all[len(all)-historyCap:]
	b, err := json.Marshal(all[0])
	if err != nil {
		return
	}
	var sb strings.Builder
	sb.Write(b)
	sb.WriteByte('\n')
	for _, e := range all[1:] {
		if b, err := json.Marshal(e); err == nil {
			sb.Write(b)
			sb.WriteByte('\n')
		}
	}
	tmp := historyPath() + ".tmp"
	if os.WriteFile(tmp, []byte(sb.String()), 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, historyPath())
}

// clearHistory 清空账本（面板「清空」与 CLI -clear 用）
func clearHistory() {
	_ = os.Remove(historyPath())
}

// recordLeaseEnd 把一张结束的租约写进账本。
// endedAt 优先用调用方给的真实结束时刻（定时租约用到期点，比「发现它结束」
// 的时刻更诚实）；startedAt 解析失败时跳过——没有申请时间的记录没有意义。
func recordLeaseEnd(l Lease, endedAt time.Time, reason string) {
	start, err := parseStamp(l.CreatedAt)
	if err != nil {
		return
	}
	if endedAt.Before(start) {
		endedAt = time.Now()
	}
	appendHistory(HistoryEntry{
		ID:          l.ID,
		Key:         l.Key,
		Name:        l.Name,
		Mode:        l.Mode,
		PID:         l.PID,
		Process:     l.ProcessName,
		CreatedAt:   l.CreatedAt,
		EndedAt:     stamp(endedAt),
		EndReason:   reason,
		DurationSec: int64(endedAt.Sub(start).Seconds()),
	})
}

// leaseEndTime 结束时刻：定时类租约用记录里的到期点（deadline / maxUntil），
// 比「守护进程发现它结束」的时刻更诚实；其余就是发现时刻。
func leaseEndTime(l Lease, now time.Time, reason string) time.Time {
	switch reason {
	case "deadline":
		if t, err := parseStamp(l.Deadline); err == nil {
			return t
		}
	case "max-lifetime":
		if t, err := parseStamp(l.MaxUntil); err == nil {
			return t
		}
	}
	return now
}

// ---------- 展示 ----------

// formatHistoryDuration 人类可读的持续时长。
// durationSec 是写入时算好的精确值，直接展示，不再向上取整。
func formatHistoryDuration(sec int64) string {
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

// historyReasonText 结束原因的中文文案
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
