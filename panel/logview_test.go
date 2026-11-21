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

// ---------- 泳道（租约连线） ----------

// 行序约定：rows[0] 是最新一行。start（旧）在下方、end（新）在上方。
func TestLanesPairStartEnd(t *testing.T) {
	rows := []logLineDTO{
		{Text: "主动释放租约 bbbbbbbbbbbb [job] key=X 持续=2 分", Lease: "bbbbbbbbbbbb", Event: "end"}, // 0（最新）
		{Text: "面板启动"}, // 1
		{Text: "新建租约 bbbbbbbbbbbb [job] key=X", Lease: "bbbbbbbbbbbb", Event: "start"}, // 2（最旧）
	}
	assignLanes(rows)

	if rows[0].Lane != 0 || rows[0].Event != "end" {
		t.Errorf("结束行应在 0 号泳道，得到 lane=%d event=%s", rows[0].Lane, rows[0].Event)
	}
	if rows[2].Lane != 0 || rows[2].Event != "start" {
		t.Errorf("新建行应在 0 号泳道，得到 lane=%d event=%s", rows[2].Lane, rows[2].Event)
	}
	// 中间无关行也要画这条线（线穿过整段区间）
	if len(rows[1].Lanes) != 1 || rows[1].Lanes[0] != 0 {
		t.Errorf("中间行应穿过 0 号泳道，得到 %v", rows[1].Lanes)
	}
	if rows[1].Lane != -1 {
		t.Errorf("非租约事件行不应有 lane 点，得到 %d", rows[1].Lane)
	}
}

// 两段租约时间交错 → 两条竖线，不同泳道
func TestLanesOverlapDifferentLanes(t *testing.T) {
	rows := []logLineDTO{
		{Lease: "aaaaaaaaaaaa", Event: "end"},   // 0  A 结束
		{Lease: "bbbbbbbbbbbb", Event: "end"},   // 1  B 结束
		{Lease: "aaaaaaaaaaaa", Event: "start"}, // 2  A 开始
		{Lease: "bbbbbbbbbbbb", Event: "start"}, // 3  B 开始
	}
	assignLanes(rows)
	if rows[0].Lane == rows[1].Lane {
		t.Errorf("交错的租约应分到不同泳道：A=%d B=%d", rows[0].Lane, rows[1].Lane)
	}
	// A 的线穿过 1、2 行；B 的线穿过 2 行（B 的区间 1..3）
	if len(rows[2].Lanes) != 2 {
		t.Errorf("第 2 行应同时穿过两条线，得到 %v", rows[2].Lanes)
	}
}

// 未结束的租约：线一直画到最新行（窗口顶端）
func TestLanesActiveLeaseExtendsToTop(t *testing.T) {
	rows := []logLineDTO{
		{Text: "其他日志"},                          // 0 最新
		{Lease: "cccccccccccc", Event: "start"}, // 1 新建后再没结束
	}
	assignLanes(rows)
	if len(rows[0].Lanes) != 1 {
		t.Errorf("未结束租约的线应延伸到最新行，得到 %v", rows[0].Lanes)
	}
	if rows[1].Lane != 0 {
		t.Errorf("新建点应在 0 号泳道，得到 %d", rows[1].Lane)
	}
}

// 开始早于日志窗口（只见结束）：线从最旧行接进来
func TestLanesEndOnlyExtendsToBottom(t *testing.T) {
	rows := []logLineDTO{
		{Lease: "dddddddddddd", Event: "end"}, // 0
		{Text: "其他日志"},                        // 1 最旧
	}
	assignLanes(rows)
	if len(rows[1].Lanes) != 1 {
		t.Errorf("只见结束的租约线应延伸到最旧行，得到 %v", rows[1].Lanes)
	}
}

// 前后不重叠的租约复用同一条泳道（和 git 分支一样）
func TestLanesReuseAfterEnd(t *testing.T) {
	rows := []logLineDTO{
		{Lease: "eeeeeeeeeeee", Event: "end"},   // 0
		{Lease: "eeeeeeeeeeee", Event: "start"}, // 1
		{Lease: "ffffffffffff", Event: "end"},   // 2
		{Lease: "ffffffffffff", Event: "start"}, // 3
	}
	assignLanes(rows)
	if rows[0].Lane != rows[2].Lane {
		t.Errorf("不重叠的租约应复用泳道：%d vs %d", rows[0].Lane, rows[2].Lane)
	}
}

// 中途「调整」行有 mid 事件点，线仍是连续的
func TestLanesMidEvent(t *testing.T) {
	rows := []logLineDTO{
		{Lease: "gggggggggggg", Event: "end"},
		{Lease: "gggggggggggg", Event: "mid"},
		{Lease: "gggggggggggg", Event: "start"},
	}
	assignLanes(rows)
	if rows[1].Event != "mid" || rows[1].Lane != 0 {
		t.Errorf("中途调整应在 0 号泳道打点，得到 lane=%d event=%s", rows[1].Lane, rows[1].Event)
	}
	if len(rows[1].Lanes) != 1 {
		t.Errorf("中途调整行也要画线，得到 %v", rows[1].Lanes)
	}
}

func TestLeaseEventParse(t *testing.T) {
	cases := []struct {
		text, id, ev string
	}{
		{"新建租约 6df3a5207cf2 [job] key=ABC234 模式=release", "6df3a5207cf2", "start"},
		{"主动释放租约 6df3a5207cf2 [job] key=ABC234 持续=2 分", "6df3a5207cf2", "end"},
		{"手动停止租约 6df3a5207cf2 [job] key=ABC234 持续=2 分", "6df3a5207cf2", "end"},
		{"租约结束 6df3a5207cf2 [job] key=ABC234 原因=deadline 持续=30 分", "6df3a5207cf2", "end"},
		{"调整租约 6df3a5207cf2 [job] 到 2026-11-17 06:00", "6df3a5207cf2", "mid"},
		{"面板启动 v0.2.1：胶囊=覆盖任务栏", "", ""},
		{"租约已全部结束，恢复原来的电源设置", "", ""},
	}
	for _, c := range cases {
		id, ev := leaseEvent(c.text)
		if id != c.id || ev != c.ev {
			t.Errorf("leaseEvent(%q) = (%q,%q), want (%q,%q)", c.text, id, ev, c.id, c.ev)
		}
	}
}
