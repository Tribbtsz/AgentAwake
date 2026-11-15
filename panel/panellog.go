package main

// panellog.go — 面板侧的日志：写进和命令行同一份 awake.log
//
// 同一个文件、同样的格式（时间 + 来源 + 内容）、同样的轮转（1 MiB → .old），
// 这样排查时只需要看一个地方：租约怎么来的、守护进程什么时候改的电源、
// 面板点了什么、胶囊有没有嵌进去，按时间排在一起。
//
// 三个进程（awake / 守护进程 / 面板）都往里追加，每次写完就关文件，一行是
// 一次 write，不会互相截断。轮转撞车时最坏的结果是某一行落进 .old，不影响排查。
//
// 关于租约密钥：日志里会写 key。它本来就在 leases\*.json 里（同目录、同权限），
// 日志没有扩大暴露面；而人要把它和面板上显示的那串对上号，只有 key 认得出。
// 但日志不要贴给 agent、也不要贴进 issue —— 那才是信任边界之外。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func logPath() string { return filepath.Join(dataDir(), "awake.log") }

// logf 追加一行日志，失败不影响主流程。
func logf(source, format string, args ...any) {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return
	}
	if st, err := os.Stat(logPath()); err == nil && st.Size() > 1<<20 {
		old := logPath() + ".old"
		_ = os.Remove(old)
		_ = os.Rename(logPath(), old)
	}
	line := fmt.Sprintf("%s  [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"),
		source, fmt.Sprintf(format, args...))
	f, err := os.OpenFile(logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// logAction 记一次面板发起的 CLI 动作。
// 成功时只记命令：acquire 的 stdout 里只有密钥，而命令行那边已经记过租约了；
// 失败时带上错误和输出摘要，否则界面上只闪一下提示、事后无从查起。
func logAction(args []string, err error, out string) {
	cmd := oneLine(strings.Join(args, " "), 200)
	if err == nil {
		logf("panel", "动作 %s", cmd)
		return
	}
	logf("panel", "动作 %s 失败: %v %s", cmd, err, oneLine(out, 200))
}

// oneLine 把可能带换行的文本压成一行，并截断到 max 个字符。
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// openLog 打开日志（托盘菜单「打开日志」）。
// 用记事本而不是系统关联：Windows 上 .log 经常没有文件关联，
// explorer.exe 打它只会弹出「选择打开方式」甚至什么都不发生。
func openLog() {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return
	}
	if _, err := os.Stat(logPath()); err != nil {
		// 还没有日志时先建个空文件，免得记事本弹「找不到」
		_ = os.WriteFile(logPath(), nil, 0o644)
	}
	_ = exec.Command("notepad.exe", logPath()).Start()
}
