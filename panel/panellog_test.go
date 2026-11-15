package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// 日志落在 LOCALAPPDATA 下，测试里换成临时目录，别污染真实日志
func inTempDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	return dir
}

func TestLogfOneLine(t *testing.T) {
	inTempDataDir(t)
	logf("panel", "启动 %s", "1116-0230")

	b, err := os.ReadFile(logPath())
	if err != nil {
		t.Fatalf("日志没写出来: %v", err)
	}
	line := string(b)
	if !strings.HasPrefix(line, "20") || !strings.Contains(line, "[panel] 启动 1116-0230") {
		t.Fatalf("日志格式不对：%q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("应当只有一行：%q", line)
	}
}

func TestLogAction(t *testing.T) {
	inTempDataDir(t)

	logAction([]string{"acquire", "-For", "30m"}, nil, "")
	logAction([]string{"release", "-Id", "73abc"}, errors.New("exit status 1"), "line1\nline2  ")

	b, _ := os.ReadFile(logPath())
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("应有 2 行，得到 %d 行：%q", len(lines), string(b))
	}
	if !strings.Contains(lines[0], "动作 acquire -For 30m") {
		t.Errorf("成功动作应只记命令：%q", lines[0])
	}
	if !strings.Contains(lines[1], "失败: exit status 1 line1 line2") {
		t.Errorf("失败应带上错误与输出摘要：%q", lines[1])
	}
}

func TestLogfRotates(t *testing.T) {
	inTempDataDir(t)
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	// 先放一个超过 1 MiB 的文件，下一次写入应把它挪成 .old
	big := strings.Repeat("x", (1<<20)+1)
	if err := os.WriteFile(logPath(), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	logf("panel", "轮转之后")

	if st, err := os.Stat(logPath() + ".old"); err != nil || st.Size() != int64(len(big)) {
		t.Fatalf("旧日志没有被轮转：%v %v", err, st)
	}
	b, _ := os.ReadFile(logPath())
	if !strings.Contains(string(b), "轮转之后") || len(b) > 1024 {
		t.Fatalf("轮转后新文件不对：%q", string(b))
	}
}
