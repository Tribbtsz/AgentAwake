package main

// task_test.go — 巡检任务的 XML 解析与路径比较

import (
	"strings"
	"testing"
)

// Command 里的 & 是 XML 实体，解析后应与原始路径相等
func TestTaskCommandParsesEscapedPath(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Actions Context="Author">
    <Exec>
      <Command>C:\Tools\R&amp;D\awake.exe</Command>
      <Arguments>-watchdog</Arguments>
    </Exec>
  </Actions>
</Task>`
	cmd, ok := taskCommand(xml)
	if !ok || cmd != `C:\Tools\R&D\awake.exe` {
		t.Fatalf("解析结果不对：%q ok=%v", cmd, ok)
	}
}

func TestTaskCommandMissing(t *testing.T) {
	if _, ok := taskCommand("<Task></Task>"); ok {
		t.Error("没有 Command 时应返回 false")
	}
}

// 端到端：当前机器上的任务（若有）其 Command 应能解析且指向 awake.exe。
// 注意不能和 exePath() 比：go test 下 exePath() 是临时目录里的测试二进制。
func TestTaskCommandReal(t *testing.T) {
	if !taskExists() {
		t.Skip("本机没有巡检任务，跳过")
	}
	out, err := schtasks("/Query", "/TN", taskName, "/XML", "ONE")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	cmd, ok := taskCommand(out)
	if !ok {
		t.Fatal("任务 XML 里没解析出 Command")
	}
	if !strings.HasSuffix(strings.ToLower(cmd), `\awake.exe`) {
		t.Errorf("任务指向 %q，不像 awake.exe", cmd)
	}
	t.Logf("任务 Command = %q", cmd)
}
