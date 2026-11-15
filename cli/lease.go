package main

// lease.go — 租约存储与判定规则
//
// 租约是一个 JSON 文件，放在 %LOCALAPPDATA%\AgentAwake\leases 下。
// 只有守护进程改电源设置；命令行只读写租约文件，不改电源。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lease 与面板读取的字段名保持一致（%LOCALAPPDATA%\AgentAwake\leases\*.json）
type Lease struct {
	ID          string `json:"id"`
	Key         string `json:"key"` // 短密钥：agent 用它只释放自己的租约
	Name        string `json:"name"`
	Note        string `json:"note"`
	Mode        string `json:"mode"` // deadline | release
	Deadline    string `json:"deadline"`
	MaxUntil    string `json:"maxUntil"`
	PID         int    `json:"pid"`
	PIDStart    int64  `json:"pidStart"`
	TTLSeconds  int    `json:"ttlSeconds"`
	CreatedAt   string `json:"createdAt"`
	LastBeat    string `json:"lastBeat"`
	Released    bool   `json:"released"`
	ProcessName string `json:"processName"`
	CreatedBy   string `json:"createdBy"`
}

// ---------- 路径 ----------

func dataDir() string { return filepath.Join(os.Getenv("LOCALAPPDATA"), "AgentAwake") }

func leaseDir() string        { return filepath.Join(dataDir(), "leases") }
func runtimePath() string     { return filepath.Join(dataDir(), "runtime.json") }
func logPath() string         { return filepath.Join(dataDir(), "awake.log") }
func panelConfigPath() string { return filepath.Join(dataDir(), "panel.json") }

func ensureDirs() {
	_ = os.MkdirAll(leaseDir(), 0o755)
}

// logf 写入 awake.log（失败不影响主流程）。
// 超过 1 MiB 就轮转成 awake.log.old，避免长期使用后无限增长。
// 租约的 key 会进日志：它本来就在 leases\*.json 里（同目录同权限），日志不扩大
// 暴露面，而人要把它和面板/CLI 上看到的那串对上号，只有 key 认得出。日志仍属
// 敏感信息，不要贴给 agent 或贴进 issue。
func logf(source, format string, args ...any) {
	ensureDirs()
	if st, err := os.Stat(logPath()); err == nil && st.Size() > 1<<20 {
		old := logPath() + ".old"
		_ = os.Remove(old)
		_ = os.Rename(logPath(), old)
	}
	line := fmt.Sprintf("%s  [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), source, fmt.Sprintf(format, args...))
	f, err := os.OpenFile(logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// ---------- 时间 ----------

// stamp 统一的时间戳格式（RFC3339 带纳秒，Go 与面板都能解析）
func stamp(t time.Time) string { return t.Format(time.RFC3339Nano) }

func parseStamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

var (
	durationRe = regexp.MustCompile(`^\+?(\d+(?:\.\d+)?)\s*(秒|分|分钟|小时|天|secs|sec|s|mins|min|m|hrs|hr|h|d)$`)
	clockRe    = regexp.MustCompile(`^\d{1,2}:\d{2}(:\d{2})?$`)
)

// parseSpec 解析时间参数：
//
//	90s / 30m / 1.5h / 2d / 30分钟 / 2小时
//	06:00 / 06:00:30（当天，已过则顺延到明天）
//	2026-11-15T06:00 / 2026-11-15 06:00 / 2026-11-15
func parseSpec(spec string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return time.Time{}, fmt.Errorf("时间不能为空")
	}

	if m := durationRe.FindStringSubmatch(strings.ToLower(s)); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		switch strings.ToLower(m[2]) {
		case "秒", "s", "sec", "secs":
			return now.Add(time.Duration(n * float64(time.Second))), nil
		case "分", "分钟", "m", "min", "mins":
			return now.Add(time.Duration(n * float64(time.Minute))), nil
		case "小时", "h", "hr", "hrs":
			return now.Add(time.Duration(n * float64(time.Hour))), nil
		default:
			return now.Add(time.Duration(n * 24 * float64(time.Hour))), nil
		}
	}

	if clockRe.MatchString(s) {
		parts := strings.Split(s, ":")
		h, _ := strconv.Atoi(parts[0])
		mi, _ := strconv.Atoi(parts[1])
		se := 0
		if len(parts) >= 3 {
			se, _ = strconv.Atoi(parts[2])
		}
		if h > 23 || mi > 59 || se > 59 {
			return time.Time{}, fmt.Errorf("时间超出范围：%s", s)
		}
		cand := time.Date(now.Year(), now.Month(), now.Day(), h, mi, se, 0, now.Location())
		if !cand.After(now) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand, nil
	}

	layouts := []string{
		"2006-01-02T15:04:05", "2006-01-02T15:04",
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
		"2006/01/02 15:04:05", "2006/01/02 15:04", "2006/01/02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法识别的时间：%s（可用 06:00 / 2026-11-15T06:00 / +30m / 90s / 2h）", spec)
}

// formatSpan 人类可读的剩余时间
func formatSpan(d time.Duration) string {
	switch {
	case d <= 0:
		return "已到期"
	case d.Hours() >= 48:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	case d.Hours() >= 1:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	case d.Minutes() >= 1:
		return fmt.Sprintf("%d 分", ceilMinutes(d))
	default:
		return fmt.Sprintf("%d 秒", int(ceilSeconds(d)))
	}
}

func ceilMinutes(d time.Duration) int {
	m := int(d.Minutes())
	if d > time.Duration(m)*time.Minute {
		m++
	}
	return m
}

func ceilSeconds(d time.Duration) float64 {
	s := d.Seconds()
	if s != float64(int(s)) {
		return float64(int(s) + 1)
	}
	return s
}

// ---------- 租约读写 ----------

func newLeaseID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%012x", time.Now().UnixNano()&0xFFFFFFFFFFFF)
	}
	return hex.EncodeToString(b) // 12 位十六进制，和旧版长度一致
}

// keyAlphabet 去掉了容易看错的 0/O/1/I，长度刚好 32，取模无偏。
const keyAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// newLeaseKey 生成 6 位短密钥（约 10 亿种）
func newLeaseKey() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06X", time.Now().UnixNano()&0xFFFFFF)
	}
	for i := range b {
		b[i] = keyAlphabet[int(b[i])%len(keyAlphabet)]
	}
	return string(b)
}

// uniqueLeaseKey 避开现有租约，保证同一台机器上不重复
func uniqueLeaseKey() string {
	used := map[string]bool{}
	for _, l := range loadLeases() {
		if l.Key != "" {
			used[strings.ToUpper(l.Key)] = true
		}
	}
	for i := 0; i < 20; i++ {
		if k := newLeaseKey(); !used[k] {
			return k
		}
	}
	return newLeaseKey()
}

func loadLeases() []Lease {
	entries, err := os.ReadDir(leaseDir())
	if err != nil {
		return nil
	}
	out := make([]Lease, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		full := filepath.Join(leaseDir(), e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var l Lease
		if err := json.Unmarshal(b, &l); err != nil {
			_ = os.Remove(full) // 坏文件清掉，避免反复报错
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func saveLease(l *Lease) error {
	ensureDirs()
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(leaseDir(), l.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeLease(id string) {
	_ = os.Remove(filepath.Join(leaseDir(), id+".json"))
}

// findLeases 按 -All / -Id 前缀 / -Name 精确匹配挑租约
func findLeases(o *options, requireSelector bool) ([]Lease, error) {
	all := loadLeases()
	if o.all {
		return all, nil
	}
	if o.id != "" {
		var hits []Lease
		for _, l := range all {
			if strings.HasPrefix(l.ID, o.id) {
				hits = append(hits, l)
			}
		}
		if len(hits) == 0 {
			return nil, fmt.Errorf("找不到租约 %s", o.id)
		}
		return hits, nil
	}
	if o.key != "" {
		var hits []Lease
		for _, l := range all {
			if l.Key != "" && strings.EqualFold(l.Key, o.key) {
				hits = append(hits, l)
			}
		}
		if len(hits) == 0 {
			return nil, fmt.Errorf("找不到密钥为 %s 的租约", strings.ToUpper(o.key))
		}
		return hits, nil
	}
	if o.name != "" {
		var hits []Lease
		for _, l := range all {
			if l.Name == o.name {
				hits = append(hits, l)
			}
		}
		if len(hits) == 0 {
			return nil, fmt.Errorf("找不到名为 %s 的租约", o.name)
		}
		return hits, nil
	}
	if requireSelector {
		return nil, fmt.Errorf("需要指定 -Id、-Name 或 -All")
	}
	return nil, nil
}

// inactiveReason 返回空串表示仍然有效，否则是失效原因
func inactiveReason(l Lease, now time.Time) string {
	if l.Released {
		return "released"
	}
	if l.MaxUntil != "" {
		if t, err := parseStamp(l.MaxUntil); err == nil && !now.Before(t) {
			return "max-lifetime"
		}
	}
	if l.Mode == "deadline" {
		if l.Deadline == "" {
			return "no-deadline"
		}
		t, err := parseStamp(l.Deadline)
		if err != nil {
			return "bad-deadline"
		}
		if !now.Before(t) {
			return "deadline"
		}
		return ""
	}
	// mode = release：等调用方主动释放，或绑定进程退出，或心跳超时
	if l.PID != 0 {
		if !processAlive(l.PID, l.PIDStart) {
			return "process-exited"
		}
		return ""
	}
	if l.TTLSeconds > 0 {
		beat := l.LastBeat
		if beat == "" {
			beat = l.CreatedAt
		}
		if t, err := parseStamp(beat); err == nil {
			if now.Sub(t) >= time.Duration(l.TTLSeconds)*time.Second {
				return "heartbeat-timeout"
			}
		}
	}
	return ""
}

func activeLeases(now time.Time) []Lease {
	var out []Lease
	for _, l := range loadLeases() {
		if inactiveReason(l, now) == "" {
			out = append(out, l)
		}
	}
	return out
}
