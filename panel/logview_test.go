package main

// logview_test.go — 日志页的尾部读取、行解析与警示分类

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadLogTail(t *testing.T) {
	inTempDataDir(t)
	// 写 5 行，只读最后 3 行
	for i := 0; i < 5; i++ {
		logf("panel", "第 %d 行", i)
	}
	lines := readLogTail(3)
	if len(lines) != 3 {
		t.Fatalf("应得 3 行，得到 %d", len(lines))
	}
	if !strings.Contains(lines[2], "第 4 行") {
		t.Errorf("最后一行应是第 4 行，得到 %q", lines[2])
	}
}

func TestReadLogTailMissingFile(t *testing.T) {
	inTempDataDir(t)
	if lines := readLogTail(10); lines != nil {
		t.Errorf("无日志时应为 nil，得到 %v", lines)
	}
}

func TestParseLogLine(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.Local)

	today := "2026-11-16 14:35:09  [supervisor] 进入保持模式（1 个租约），已改为不休眠"
	d, ok := parseLogLine(today, now)
	if !ok || d.Time != "14:35:09" || d.Source != "supervisor" || d.Kind != "" {
		t.Errorf("当天行解析不对：%+v ok=%v", d, ok)
	}

	old := "2026-11-15 22:00:01  [awake] 新建租约 abc [job] key=ABC234"
	d, _ = parseLogLine(old, now)
	if d.Time != "11-15 22:00:01" {
		t.Errorf("跨天应带日期，得到 %q", d.Time)
	}

	// 不认识的行原样保留
	d, ok = parseLogLine("随便一行没有格式的内容", now)
	if !ok || d.Text != "随便一行没有格式的内容" {
		t.Errorf("未知行应原样保留：%+v ok=%v", d, ok)
	}

	// 空行丢弃
	if _, ok := parseLogLine("   ", now); ok {
		t.Error("空行应被丢弃")
	}
}

func TestLogKindWarn(t *testing.T) {
	warns := []string{
		"注册巡检任务失败: exit status 1",
		"守护进程未能启动，巡检任务会在几分钟内重试",
		"租约结束 abc [job] key=X 原因=max-lifetime 持续=12 小时",
		"租约结束 abc [job] key=X 原因=heartbeat-timeout 持续=2 分",
	}
	for _, msg := range warns {
		if logKind(msg) != "warn" {
			t.Errorf("应为 warn：%q", msg)
		}
	}
	normal := []string{
		"新建租约 abc [job] key=X 模式=release 绑定进程=0",
		"主动释放租约 abc [job] key=X 持续=26 分",
		"进入保持模式（1 个租约），已改为不休眠",
	}
	for _, msg := range normal {
		if logKind(msg) != "" {
			t.Errorf("不应为 warn：%q", msg)
		}
	}
}

func TestApiLogReadsTailReverse(t *testing.T) {
	inTempDataDir(t)
	for i := 0; i < 3; i++ {
		logf("awake", "事件 %d", i)
	}
	a := newTestApp()
	rows := Api{a: a}.Log()
	if len(rows) != 3 {
		t.Fatalf("应得 3 行，得到 %d", len(rows))
	}
	if !strings.Contains(rows[0].Text, "事件 2") {
		t.Errorf("最新应在前，得到 %q", rows[0].Text)
	}
}

func TestClearLog(t *testing.T) {
	inTempDataDir(t)
	logf("panel", "旧日志")
	api := Api{a: newTestApp()}
	if !api.ClearLog() {
		t.Fatal("清空应成功")
	}
	b, err := os.ReadFile(filepath.Join(dataDir(), "awake.log"))
	if err != nil {
		// 清空后只写了一条「日志已清空」，文件应存在
		t.Fatalf("清空后应仍有日志文件: %v", err)
	}
	if strings.Contains(string(b), "旧日志") {
		t.Error("旧内容应被清掉")
	}
	if !strings.Contains(string(b), "日志已清空") {
		t.Error("清空动作本身应留一行记录")
	}
}
