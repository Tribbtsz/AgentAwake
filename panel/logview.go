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
	Kind   string `json:"kind"` // "" | warn
}

// awake.log 的行格式：2026-11-16 14:35:09  [panel] 面板启动 v0.2.0：胶囊=覆盖任务栏
var logLineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})  \[([a-z]+)\] (.*)$`)

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

// logKind 警示行：失败类，以及两种「agent 没管好自己」的结束原因
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
