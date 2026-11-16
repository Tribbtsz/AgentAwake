package main

// status.go — 原生读取状态，不启动任何子进程
//
// 旧实现每 5 秒 spawn 两次 pwsh（status -Json + 菜单 refreshSync），
// 每次 PowerShell 冷启动 200-500ms，等于常驻 5-10% 单核 CPU。
// 现在直接读租约 JSON + runtime.json + 注册表，微秒级。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------- 路径 ----------

func dataDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "AgentAwake")
}

func leasesDir() string   { return filepath.Join(dataDir(), "leases") }
func runtimePath() string { return filepath.Join(dataDir(), "runtime.json") }

// ---------- 数据结构（与 cli 的租约 schema 对齐） ----------

type lease struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
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
}

type leaseView struct {
	lease
	Active    bool
	Remaining time.Duration
	StateText string // active | deadline | process-exited | released | ...
}

type powerValues struct {
	SleepAC    int
	SleepDC    int
	HibAC      int
	HibDC      int
	DisplayAC  int
	DisplayDC  int
	SchemeGUID string
}

type snapshot struct {
	Holding       bool
	Leases        []leaseView
	Power         powerValues
	SupervisorPID int
	WatchdogTask  bool
	Err           string
}

// ---------- 读取 ----------

func readLeases() ([]leaseView, error) {
	dir := leasesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now()
	out := make([]leaseView, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var l lease
		if err := json.Unmarshal(b, &l); err != nil {
			continue
		}
		lv := leaseView{lease: l}
		lv.StateText, lv.Active, lv.Remaining = evaluateLease(l, now)
		out = append(out, lv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// evaluateLease 与 lib.ps1 的 Get-LeaseInactiveReason 保持同一套规则
func evaluateLease(l lease, now time.Time) (state string, active bool, remaining time.Duration) {
	if l.Released {
		return "released", false, 0
	}
	if l.MaxUntil != "" {
		if t, err := time.Parse(time.RFC3339Nano, l.MaxUntil); err == nil && !now.Before(t) {
			return "max-lifetime", false, 0
		}
	}
	switch l.Mode {
	case "deadline":
		if l.Deadline == "" {
			return "no-deadline", false, 0
		}
		t, err := time.Parse(time.RFC3339Nano, l.Deadline)
		if err != nil {
			return "bad-deadline", false, 0
		}
		remaining = t.Sub(now)
		if remaining <= 0 {
			return "deadline", false, 0
		}
		return "active", true, remaining
	default: // release
		if l.PID != 0 && !processAlive(l.PID, l.PIDStart) {
			return "process-exited", false, 0
		}
		// TTL 与 PID 绑定可同时生效：进程活着但卡死时靠心跳超时兜底
		if l.TTLSeconds > 0 {
			beat := l.CreatedAt
			if l.LastBeat != "" {
				beat = l.LastBeat
			}
			if t, err := time.Parse(time.RFC3339Nano, beat); err == nil {
				if now.Sub(t).Seconds() >= float64(l.TTLSeconds) {
					return "heartbeat-timeout", false, 0
				}
			}
		}
		return "active", true, 0
	}
}

// processAlive 判断进程是否存活；pidStart 用来防 PID 复用
func processAlive(pid int, pidStart int64) bool {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	if pidStart != 0 {
		var creation, exit, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err == nil {
			// PowerShell 的 StartTime.ToUniversalTime().Ticks 是 100ns 单位
			ticks := creation.Nanoseconds() / 100
			if ticks != pidStart {
				return false
			}
		}
	}
	return true
}

func readRuntime() (pid int, mode string) {
	b, err := os.ReadFile(runtimePath())
	if err != nil {
		return 0, ""
	}
	var rt struct {
		SupervisorPID int    `json:"supervisorPid"`
		Mode          string `json:"mode"`
	}
	if json.Unmarshal(b, &rt) != nil {
		return 0, ""
	}
	return rt.SupervisorPID, rt.Mode
}

// 电源设置（注册表读，零开销）
const powerSchemesKey = `SYSTEM\CurrentControlSet\Control\Power\User\PowerSchemes`

const (
	subSleep     = `238c9fa8-0aad-41ed-83f4-97be242c8f20`
	setStandby   = `29f6c1db-86da-48c5-9fdb-f2b67b1f44da`
	setHibern    = `9d7815a6-7ee4-497e-8888-515a05f02364`
	subVideo     = `7516b95f-f776-4464-8c53-06167f40cc99`
	setVideoIdle = `3c0bc021-c8a8-4e07-a973-6b14cbcb2b7e`
)

func readPower() powerValues {
	var p powerValues
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, powerSchemesKey, registry.QUERY_VALUE)
	if err != nil {
		return p
	}
	scheme, _, err := k.GetStringValue("ActivePowerScheme")
	k.Close()
	if err != nil || scheme == "" {
		return p
	}
	p.SchemeGUID = scheme

	read := func(sub, setting string) (int, int) {
		path := powerSchemesKey + `\` + scheme + `\` + sub + `\` + setting
		kk, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err != nil {
			return -1, -1
		}
		defer kk.Close()
		// 值不存在（未显式设置过）记 -1 显示「—」，不能当成 0（从不）：
		// 「跟随方案默认」和「从不」是两回事。
		ac, dc := -1, -1
		if v, _, err := kk.GetIntegerValue("ACSettingIndex"); err == nil {
			ac = int(v)
		}
		if v, _, err := kk.GetIntegerValue("DCSettingIndex"); err == nil {
			dc = int(v)
		}
		return ac, dc
	}
	p.SleepAC, p.SleepDC = read(subSleep, setStandby)
	p.HibAC, p.HibDC = read(subSleep, setHibern)
	p.DisplayAC, p.DisplayDC = read(subVideo, setVideoIdle)
	return p
}

func readSnapshot() snapshot {
	var s snapshot
	leases, err := readLeases()
	if err != nil {
		s.Err = err.Error()
	}
	s.Leases = leases
	s.Power = readPower()
	pid, mode := readRuntime()
	if pid != 0 && supervisorRunning() {
		s.SupervisorPID = pid
	}
	// 守护进程说自己在保持，即使租约文件刚被删掉，电源也还没恢复
	if mode == "keep" {
		s.Holding = true
	}
	for _, l := range leases {
		if l.Active {
			s.Holding = true
			break
		}
	}
	if !cliOK && s.Err == "" {
		s.Err = "找不到 awake.exe：请把它与 panel.exe 放在同一目录，或加入 PATH"
	}
	return s
}

// supervisorRunning 守护进程整个生命周期都持有这个互斥体；
// 我们能拿到就说明它已经不在了（避免 runtime.json 里留下过期 PID）。
func supervisorRunning() bool {
	const mutexName = `Local\AgentAwakeSupervisor`
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return false
	}
	h, err := windows.CreateMutex(nil, false, name)
	if h == 0 {
		return false
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return false
	}
	defer windows.CloseHandle(h)

	ev, werr := windows.WaitForSingleObject(h, 0)
	if werr != nil {
		return false
	}
	switch ev {
	case 0x102: // WAIT_TIMEOUT：别人持有 → 在跑
		return true
	case 0, 0x80: // 我们自己拿到了 → 没人持有
		_ = windows.ReleaseMutex(h)
		return false
	}
	return false
}

func activeCount(leases []leaseView) int {
	n := 0
	for _, l := range leases {
		if l.Active {
			n++
		}
	}
	return n
}

// nowForTest 让测试不依赖真实时钟（仅测试文件使用）
func nowForTest() time.Time { return time.Now() }
