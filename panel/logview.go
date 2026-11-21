package main

// logview.go — 面板「日志」页：读 awake.log 尾部并解析成结构化行
//
// 面板历史页显示的是「租约生命周期摘要」，但排查问题时想看的是完整事件流：
// 谁申请、谁释放、手动停止、守护进程进出保持、巡检拉起、错误……这些本来就在
// awake.log 里（命令行 / 守护进程 / 面板三处同写一份），这里只做尾部读取 +
// 解析 + 警示分类，不引入第二份存储。
//
// 大文件不整读：只 seek 到末尾 128 KiB，日志再长也是常数开销。

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// panelLogMax 面板最多渲染的日志行数
const panelLogMax = 300

// logLineDTO 一行日志（Go 侧解析并格式化好，JS 只渲染）
type logLineDTO struct {
	Time   string `json:"time"`   // 当天 HH:MM:SS；跨天 MM-DD HH:MM:SS
	Source string `json:"source"` // awake | supervisor | panel | watchdog
	Text   string `json:"text"`
	Kind   string `json:"kind"`            // "" | warn
	Lease  string `json:"lease,omitempty"` // 12 位租约 id：这条日志属于哪张租约
	Event  string `json:"event,omitempty"` // start | end | mid
	Lane   int    `json:"lane"`            // 本行事件所在泳道（-1 = 不是租约事件）
	Lanes  []int  `json:"lanes"`           // 本行要画的竖线泳道（穿过这行的所有租约）
}

// awake.log 的行格式：2026-11-16 14:35:09  [panel] 面板启动 v0.2.0：胶囊=覆盖任务栏
var logLineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})  \[([a-z]+)\] (.*)$`)

// leaseEventRe 租约生命周期事件：新建 / 主动释放 / 手动停止 / 到期结束 / 调整。
// 只认这几种前缀，其余日志（面板启动、进入保持、巡检……）不挂到任何租约上。
var leaseEventRe = regexp.MustCompile(`^(新建租约|主动释放租约|手动停止租约|租约结束|调整租约) ([0-9a-f]{12})\b`)

// leaseEvent 返回该日志行属于哪张租约、是什么事件；不是租约事件则返回空。
func leaseEvent(text string) (id, ev string) {
	m := leaseEventRe.FindStringSubmatch(text)
	if m == nil {
		return "", ""
	}
	switch m[1] {
	case "新建租约":
		return m[2], "start"
	case "调整租约":
		return m[2], "mid"
	default: // 主动释放 / 手动停止 / 租约结束
		return m[2], "end"
	}
}

// readLogTail 读 awake.log 最后 n 行（最旧在前）。文件不存在/读失败返回 nil。
func readLogTail(n int) []string {
	f, err := os.Open(logPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const tailBytes = 128 * 1024
	off := st.Size() - tailBytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(buf), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // 截断处的半行丢掉
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// parseLogLine 解析一行；不认识的行原样保留（宁可显示原始内容也不丢信息）
func parseLogLine(line string, now time.Time) (logLineDTO, bool) {
	m := logLineRe.FindStringSubmatch(line)
	if m == nil {
		txt := strings.TrimSpace(line)
		if txt == "" {
			return logLineDTO{}, false
		}
		return logLineDTO{Text: txt}, true
	}
	dto := logLineDTO{Source: m[2], Text: m[3], Kind: logKind(m[3])}
	dto.Lease, dto.Event = leaseEvent(m[3])
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local); err == nil {
		if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
			dto.Time = t.Format("15:04:05")
		} else {
			dto.Time = t.Format("01-02 15:04:05")
		}
	} else {
		dto.Time = m[1]
	}
	return dto, true
}

// maxLeaseLanes 同时画几条泳道；并发的租约再多也只画这几列
const maxLeaseLanes = 3

// assignLanes 给每行标注泳道，让同一张租约的「新建→结束」落在同一条竖线上。
// rows 按「最新在前」排列：start（旧）在下方、end（新）在上方，连线向上收尾。
// 还没结束的租约线延伸到窗口顶端（= 现在）；开始早于窗口的租约线延伸到窗口底端。
func assignLanes(rows []logLineDTO) {
	type span struct {
		top, bottom      int // 较新/较旧的两端（行号，小 = 新）
		hasStart, hasEnd bool
	}
	spans := make(map[string]*span)
	for i := range rows {
		r := &rows[i]
		if r.Lease == "" || r.Event == "" {
			continue
		}
		s := spans[r.Lease]
		if s == nil {
			s = &span{top: i, bottom: i}
			spans[r.Lease] = s
		}
		if i < s.top {
			s.top = i
		}
		if i > s.bottom {
			s.bottom = i
		}
		switch r.Event {
		case "start":
			s.hasStart = true
		case "end":
			s.hasEnd = true
		}
	}
	for _, s := range spans {
		if s.hasStart && !s.hasEnd {
			s.top = 0 // 仍在保持中：线一直画到最新一行
		}
		if s.hasEnd && !s.hasStart {
			s.bottom = len(rows) - 1 // 开始早于日志窗口：线从最旧一行接进来
		}
	}

	// 贪心分配泳道：按 top 升序，占用区间完全在上方（laneEnd < top）的泳道可复用
	ids := make([]string, 0, len(spans))
	for id := range spans {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return spans[ids[a]].top < spans[ids[b]].top })
	laneEnd := make([]int, 0, maxLeaseLanes) // 每列已占到的最底行
	laneOf := make(map[string]int, len(spans))
	for _, id := range ids {
		s := spans[id]
		lane := -1
		for l := range laneEnd {
			if laneEnd[l] < s.top {
				lane = l
				break
			}
		}
		if lane < 0 {
			if len(laneEnd) < maxLeaseLanes {
				lane = len(laneEnd)
				laneEnd = append(laneEnd, s.bottom)
			} else {
				lane = maxLeaseLanes - 1 // 并发超限：挤在最后一列，视觉重叠但不画错
				if s.bottom > laneEnd[lane] {
					laneEnd[lane] = s.bottom
				}
			}
		} else {
			laneEnd[lane] = s.bottom
		}
		laneOf[id] = lane
	}

	for i := range rows {
		r := &rows[i]
		r.Lane = -1
		r.Lanes = r.Lanes[:0]
		for id, lane := range laneOf {
			s := spans[id]
			if i >= s.top && i <= s.bottom {
				r.Lanes = append(r.Lanes, lane)
			}
		}
		sort.Ints(r.Lanes)
		out := r.Lanes[:0]
		for _, l := range r.Lanes {
			if len(out) == 0 || out[len(out)-1] != l {
				out = append(out, l)
			}
		}
		r.Lanes = out
		if lane, ok := laneOf[r.Lease]; ok && r.Event != "" {
			r.Lane = lane
		}
	}
}
func logKind(msg string) string {
	for _, k := range []string{
		"失败", "错误", "异常", "未能", "警告",
		"原因=max-lifetime", "原因=heartbeat-timeout",
	} {
		if strings.Contains(msg, k) {
			return "warn"
		}
	}
	return ""
}

// Log 日志页数据：最新在前，最多 panelLogMax 行
func (api Api) Log() []logLineDTO {
	if api.a == nil {
		return []logLineDTO{}
	}
	return onUI(func() []logLineDTO {
		raw := readLogTail(panelLogMax)
		now := time.Now()
		out := make([]logLineDTO, 0, len(raw))
		for i := len(raw) - 1; i >= 0; i-- {
			if d, ok := parseLogLine(raw[i], now); ok {
				out = append(out, d)
			}
		}
		assignLanes(out)
		return out
	})
}

// ClearLog 清空日志：截断 awake.log 并删掉轮转文件。
// 三个进程都是「每次打开追加一行」，截断后新事件会写进新文件，安全。
func (api Api) ClearLog() bool {
	if api.a == nil {
		return false
	}
	return onUI(func() bool {
		_ = os.Remove(logPath() + ".old")
		err := os.Truncate(logPath(), 0)
		if os.IsNotExist(err) {
			err = nil
		}
		if err != nil {
			return false
		}
		logf("panel", "日志已清空")
		return true
	})
}
