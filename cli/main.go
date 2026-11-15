package main

// main.go — AgentAwake 命令行（纯 Go，替代原 awake.ps1 / lib.ps1 / supervisor.ps1）
//
// 核心模型是「租约 + 单守护进程」：
//   · agent 申请一张租约 → 守护进程把系统钉住不睡，屏幕照常熄灭
//   · agent 主动释放，或绑定的进程退出，租约自动失效
//   · 所有租约都结束后才恢复原电源设置（多个 agent 互不干扰）
//   · 也可以手动指定固定时间（hold / extend）
//
// 用法：
//   awake hold -for 30m
//   awake hold -until 06:00
//   awake acquire -name nightly-build -followpid 1234
//   awake release -id 3f2a
//   awake status
//   awake stop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type options struct {
	name         string
	id           string
	forSpec      string
	until        string
	note         string
	followPID    int
	pidFile      string
	timeoutHours int
	ttlSeconds   int
	all          bool
	jsonOut      bool
	watchdog     bool
}

func main() {
	o, pos, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		os.Exit(2)
	}
	if o.watchdog {
		os.Exit(runWatchdog())
	}

	cmd := "status"
	if len(pos) > 0 {
		cmd = strings.ToLower(pos[0])
	}

	switch cmd {
	case "supervisor":
		os.Exit(runSupervisor())
	case "_probe":
		// 内部诊断：打印进程镜像路径与互斥体状态
		for _, p := range pos[1:] {
			n, _ := strconv.Atoi(p)
			img, ticks, ok := processImage(n)
			fmt.Printf("pid=%d ok=%v image=%q ticks=%d\n", n, ok, img, ticks)
		}
		fmt.Printf("self=%q supervisorRunning=%v\n", exePath(), supervisorRunning())
	case "help", "-h", "--help", "?", "/?":
		printHelp()
	case "acquire", "lease", "hold":
		os.Exit(cmdAcquire(o, cmd))
	case "release", "done", "finish":
		os.Exit(cmdRelease(o))
	case "extend", "shorten":
		os.Exit(cmdExtend(o))
	case "heartbeat":
		os.Exit(cmdHeartbeat(o))
	case "stop":
		os.Exit(cmdStop())
	case "status", "list":
		os.Exit(cmdStatus(o))
	default:
		fmt.Printf("未知命令: %s\n", cmd)
		fmt.Println("可用命令: acquire | release | extend | heartbeat | status | stop | hold")
		os.Exit(2)
	}
}

// ---------- 参数 ----------

// parseArgs 手写解析：大小写不敏感，兼容 -For 30m / -For=30m / --for 30m
func parseArgs(args []string) (*options, []string, error) {
	o := &options{}
	var pos []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		key := strings.TrimLeft(a, "-")
		val := ""
		hasVal := false
		if k, v, ok := strings.Cut(key, "="); ok {
			key, val, hasVal = k, v, true
		}

		need := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("参数 -%s 缺少值", key)
			}
			i++
			return args[i], nil
		}

		switch strings.ToLower(key) {
		case "all":
			o.all = true
		case "json":
			o.jsonOut = true
		case "watchdog":
			o.watchdog = true
		case "name":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.name = v
		case "id":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.id = v
		case "for":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.forSpec = v
		case "until":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.until = v
		case "note":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.note = v
		case "pidfile":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.pidFile = v
		case "followpid":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.followPID, _ = strconv.Atoi(strings.TrimSpace(v))
		case "timeouthours":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.timeoutHours, _ = strconv.Atoi(strings.TrimSpace(v))
		case "ttlseconds":
			v, err := need()
			if err != nil {
				return nil, nil, err
			}
			o.ttlSeconds, _ = strconv.Atoi(strings.TrimSpace(v))
		default:
			return nil, nil, fmt.Errorf("未知参数 -%s", key)
		}
	}
	return o, pos, nil
}

// ---------- 申请租约 ----------

func panelMaxHours() int {
	b, err := os.ReadFile(panelConfigPath())
	if err != nil {
		return 12
	}
	var cfg struct {
		MaxHours int `json:"maxHours"`
	}
	if json.Unmarshal(b, &cfg) != nil || cfg.MaxHours <= 0 {
		return 12
	}
	return cfg.MaxHours
}

func cmdAcquire(o *options, cmd string) int {
	now := time.Now()

	var deadline *time.Time
	if spec := firstNonEmpty(o.forSpec, o.until); spec != "" {
		t, err := parseSpec(spec, now)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		deadline = &t
	}
	// hold = 手动固定时长（没给时间默认 1 小时）
	// acquire/lease = agent 主动申请，等它调 release，只有兜底上限
	if cmd == "hold" && deadline == nil {
		t := now.Add(time.Hour)
		deadline = &t
	}

	limitHours := o.timeoutHours
	if limitHours <= 0 {
		limitHours = panelMaxHours()
	}

	fpid := o.followPID
	if fpid == 0 && o.pidFile != "" {
		if b, err := os.ReadFile(o.pidFile); err == nil {
			fpid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}

	name := o.name
	if name == "" {
		name = "unnamed"
		if cmd == "hold" {
			name = "manual"
		}
	}

	l := &Lease{
		ID:         newLeaseID(),
		Name:       name,
		Note:       o.note,
		TTLSeconds: o.ttlSeconds,
		CreatedAt:  stamp(now),
		LastBeat:   stamp(now),
		CreatedBy:  currentUser(),
	}
	if deadline != nil {
		l.Mode = "deadline"
		l.Deadline = stamp(*deadline)
		l.MaxUntil = stamp(deadline.Add(time.Hour))
	} else {
		l.Mode = "release"
		l.MaxUntil = stamp(now.Add(time.Duration(limitHours) * time.Hour))
	}

	if fpid != 0 {
		img, ticks, ok := processImage(fpid)
		if !ok {
			fmt.Fprintf(os.Stderr, "进程 %d 不存在，无法绑定\n", fpid)
			return 1
		}
		l.PID = fpid
		l.PIDStart = ticks
		l.ProcessName = strings.TrimSuffix(filepath.Base(img), ".exe")
	}

	if err := saveLease(l); err != nil {
		fmt.Fprintln(os.Stderr, "写入租约失败:", err)
		return 1
	}

	removeLegacyTasks()
	registerWatchdogTask()
	sup := startSupervisor()
	logf("awake", "新建租约 %s [%s] 模式=%s 绑定进程=%d", l.ID, l.Name, l.Mode, l.PID)

	if o.jsonOut {
		printJSON(map[string]any{
			"id":            l.ID,
			"name":          l.Name,
			"mode":          l.Mode,
			"deadline":      nilIfEmpty(l.Deadline),
			"maxUntil":      l.MaxUntil,
			"pid":           nilIfZero(l.PID),
			"supervisorPid": sup,
		})
		return 0
	}

	fmt.Printf("%s  %s\n", l.ID, l.Name)
	if l.Mode == "deadline" {
		fmt.Printf("模式: 定时到 %s\n", humanTime(l.Deadline))
	} else {
		fmt.Println("模式: 等调用方释放（或绑定进程退出）")
	}
	fmt.Printf("硬上限: %s\n", humanTime(l.MaxUntil))
	fmt.Printf("守护进程: PID %d\n", sup)
	return 0
}

// ---------- 释放 ----------

func cmdRelease(o *options) int {
	targets, err := findLeases(o, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		removeLease(t.ID)
		ids = append(ids, t.ID)
		logf("awake", "主动释放租约 %s [%s]", t.ID, t.Name)
	}
	left := len(loadLeases())

	if o.jsonOut {
		printJSON(map[string]any{"released": ids, "remaining": left})
		return 0
	}
	fmt.Printf("已释放 %d 个租约，剩余 %d 个\n", len(targets), left)
	if left == 0 {
		fmt.Println("没有租约了，守护进程会在几秒内恢复原来的电源设置。")
	} else {
		fmt.Println("仍有租约在保持中。")
	}
	return 0
}

// ---------- 调整时间 / 心跳 ----------

func cmdExtend(o *options) int {
	targets, err := findLeases(o, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	spec := firstNonEmpty(o.forSpec, o.until)
	if spec == "" {
		fmt.Fprintln(os.Stderr, "需要 -For 或 -Until")
		return 1
	}
	now := time.Now()
	deadline, err := parseSpec(spec, now)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ids := make([]string, 0, len(targets))
	for i := range targets {
		t := targets[i]
		t.Mode = "deadline"
		t.Deadline = stamp(deadline)
		t.MaxUntil = stamp(deadline.Add(time.Hour))
		t.LastBeat = stamp(now)
		_ = saveLease(&t)
		ids = append(ids, t.ID)
		logf("awake", "调整租约 %s [%s] 到 %s", t.ID, t.Name, deadline.Format("2006-01-02 15:04"))
	}

	if o.jsonOut {
		printJSON(map[string]any{"updated": ids, "deadline": stamp(deadline)})
		return 0
	}
	fmt.Printf("已调整 %d 个租约，到期时间 %s\n", len(targets), deadline.Format("2006-01-02 15:04"))
	return 0
}

func cmdHeartbeat(o *options) int {
	targets, err := findLeases(o, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	now := stamp(time.Now())
	ids := make([]string, 0, len(targets))
	for i := range targets {
		t := targets[i]
		t.LastBeat = now
		_ = saveLease(&t)
		ids = append(ids, t.ID)
	}
	if o.jsonOut {
		printJSON(map[string]any{"beat": ids})
		return 0
	}
	fmt.Printf("已心跳 %d 个租约\n", len(targets))
	return 0
}

// ---------- 全部停止 ----------

func cmdStop() int {
	all := loadLeases()
	for _, l := range all {
		removeLease(l.ID)
	}
	if len(all) > 0 {
		logf("awake", "stop：释放全部 %d 个租约", len(all))
	}

	// 等守护进程恢复；它要是没动静就自己恢复
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		rt := readRuntime()
		if rt == nil || rt.Mode != "keep" {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if rt := readRuntime(); rt != nil && rt.Mode == "keep" {
		restoreSnapshot(rt.Snapshot)
		logf("awake", "stop：守护进程未及时恢复，已手动恢复")
	}
	fmt.Println("已停止保持，电源设置恢复原值。")
	return 0
}

// ---------- 状态 ----------

type leaseRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Deadline  string `json:"deadline"`
	PID       int    `json:"pid"`
	Process   string `json:"process"`
	Remaining string `json:"remaining"`
	Active    bool   `json:"active"`
	State     string `json:"state"`
}

func cmdStatus(o *options) int {
	now := time.Now()
	leases := loadLeases()
	power := powerStateNow()
	supPID := supervisorPID()
	rt := readRuntime()

	rows := make([]leaseRow, 0, len(leases))
	activeCount := 0
	for _, l := range leases {
		reason := inactiveReason(l, now)
		row := leaseRow{
			ID:       l.ID,
			Name:     l.Name,
			Mode:     l.Mode,
			Deadline: l.Deadline,
			PID:      l.PID,
			Process:  l.ProcessName,
			Active:   reason == "",
			State:    "active",
		}
		if reason != "" {
			row.State = reason
		}
		if l.Mode == "deadline" {
			if t, err := parseStamp(l.Deadline); err == nil {
				row.Remaining = formatSpan(t.Sub(now))
			}
		}
		if row.Active {
			activeCount++
		}
		rows = append(rows, row)
	}

	holding := activeCount > 0 || (rt != nil && rt.Mode == "keep")

	if o.jsonOut {
		printJSON(map[string]any{
			"holding":       holding,
			"leases":        rows,
			"power":         power,
			"supervisorPid": nilIfZero(supPID),
			"watchdogTask":  taskExists(),
			"runtimeMode":   nilIfEmpty(runtimeMode(rt)),
			"lastHeal":      nilIfEmpty(runtimeLastHeal(rt)),
		})
		return 0
	}

	if holding {
		fmt.Println("保持状态: 保持中（不会睡眠）")
	} else {
		fmt.Println("保持状态: 未启用（按原设置睡眠）")
	}
	fmt.Printf("睡眠超时: %s    休眠超时: %s    熄屏超时: %s（不改）\n",
		formatPowerValue(power["sleep"].AC), formatPowerValue(power["hibernate"].AC), formatPowerValue(power["display"].AC))
	supText := "未运行"
	if supPID != 0 {
		supText = fmt.Sprintf("运行中 (PID %d)", supPID)
	}
	taskText := "无"
	if taskExists() {
		taskText = "已注册"
	}
	fmt.Printf("守护进程: %s    巡检任务: %s\n", supText, taskText)
	fmt.Println()

	if len(rows) == 0 {
		fmt.Println("租约: 无")
		return 0
	}
	fmt.Printf("租约: %d 个（%d 个生效）\n", len(rows), activeCount)
	for i, l := range leases {
		fmt.Println(leaseLine(l, rows[i], now))
	}
	return 0
}

// leaseLine 单行租约摘要（列宽按显示宽度对齐，中文算 2 格）
func leaseLine(l Lease, row leaseRow, now time.Time) string {
	mode := "等完成"
	if l.Mode == "deadline" {
		mode = "定时"
	}
	pidText := "不绑定进程"
	if l.PID != 0 {
		pidText = fmt.Sprintf("进程 %d", l.PID)
		if l.ProcessName != "" {
			pidText += " (" + l.ProcessName + ")"
		}
	}
	when := "等待释放"
	if l.Mode == "deadline" {
		if t, err := parseStamp(l.Deadline); err == nil {
			when = fmt.Sprintf("到期 %s（剩 %s）", t.Format("01-02 15:04"), formatSpan(t.Sub(now)))
		}
	}
	return "  " + padRight(l.ID, 12) + "  " + padRight(l.Name, 24) + " " +
		padRight(mode, 6) + " " + padRight(pidText, 28) + " " + when
}

// padRight 按终端显示宽度补空格（中日韩字符占 2 格）
func padRight(s string, width int) string {
	w := 0
	for _, r := range s {
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6)
}

// ---------- 小工具 ----------

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Println(string(b))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func runtimeMode(rt *Runtime) string {
	if rt == nil {
		return ""
	}
	return rt.Mode
}

func runtimeLastHeal(rt *Runtime) string {
	if rt == nil {
		return ""
	}
	return rt.LastHeal
}

// humanTime 把内部时间戳转成「2026-11-15 06:00」
func humanTime(s string) string {
	t, err := parseStamp(s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02 15:04")
}

func printHelp() {
	fmt.Print(`AgentAwake — 让电脑在需要时不进入睡眠（屏幕照常熄灭）

用法:
  awake hold -for 30m                 手动保持 30 分钟
  awake hold -until 06:00             保持到今天/明天 06:00
  awake acquire -name build -followpid 1234
                                      申请租约并绑定进程；进程退出自动释放
  awake release -id 3f2a              释放租约（前缀匹配）
  awake release -name build           按名字释放
  awake release -all                  释放全部
  awake extend -for 2h -all           延长到期时间
  awake heartbeat -id 3f2a            刷新心跳（不绑定进程时用）
  awake status [-json]                查看状态
  awake stop                          立刻停止全部保持

时间格式: 90s / 30m / 1.5h / 2d / 30分钟 / 2小时 / 06:00 / 2026-11-15T06:00
`)
}
