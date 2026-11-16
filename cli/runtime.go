package main

// runtime.go — runtime.json（守护进程的自我状态，面板也读它）

import (
	"encoding/json"
	"os"
	"time"
)

type Runtime struct {
	SupervisorPID int            `json:"supervisorPid"`
	StartedAt     string         `json:"startedAt"`
	Mode          string         `json:"mode"` // idle | keep
	Snapshot      *powerSnapshot `json:"snapshot"`
	LastHeal      string         `json:"lastHeal"`
	ActiveCount   int            `json:"activeCount"`
	LeaseIDs      []string       `json:"leaseIds"`
}

func readRuntime() *Runtime {
	b, err := os.ReadFile(runtimePath())
	if err != nil {
		return nil
	}
	var rt Runtime
	if err := json.Unmarshal(b, &rt); err != nil {
		return nil
	}
	return &rt
}

func saveRuntime(rt *Runtime) {
	_, _ = writeRuntime(rt)
}

// writeRuntime 写入并返回序列化结果（给「内容没变就不写」用）。
// tmp 名唯一：supervisor 与 `awake stop` 可能同时写 runtime.json。
func writeRuntime(rt *Runtime) (string, error) {
	ensureDirs()
	b, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dataDir(), "runtime.*.tmp")
	if err != nil {
		return string(b), err
	}
	tmp := f.Name()
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return string(b), err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return string(b), err
	}
	if err := os.Rename(tmp, runtimePath()); err != nil {
		_ = os.Remove(tmp)
		return string(b), err
	}
	return string(b), nil
}

// saveRuntimeIfChanged 守护进程每秒循环，内容没变就不要反复写盘
func saveRuntimeIfChanged(rt *Runtime, last *string) {
	b, _ := json.MarshalIndent(rt, "", "  ")
	if string(b) == *last {
		return
	}
	if _, err := writeRuntime(rt); err == nil {
		*last = string(b)
	}
}

// supervisorPID 运行中的守护进程 PID（0 = 没有）
// 判定依据是「单实例互斥体是否被持有」，PID 只用来展示。
func supervisorPID() int {
	if !supervisorRunning() {
		return 0
	}
	rt := readRuntime()
	if rt == nil {
		return 0
	}
	return rt.SupervisorPID
}

// startSupervisor 以完全脱离的方式拉起守护进程（无窗口、不继承当前控制台）。
// 启动失败时会重试；仍不行就让巡检任务立刻跑一次，避免租约生效却没人保持。
func startSupervisor() int {
	if pid := supervisorPID(); pid != 0 {
		return pid
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := exeCommand("supervisor").Start(); err != nil {
			logf("awake", "守护进程启动失败(第 %d 次): %v", attempt, err)
			continue
		}
		if pid := waitSupervisor(2 * time.Second); pid != 0 {
			return pid
		}
		logf("awake", "守护进程未在 2 秒内就绪(第 %d 次)", attempt)
	}
	// 兜底：让巡检任务立即执行一次，它会在几秒内拉起守护进程
	if _, err := schtasks("/Run", "/TN", taskName); err != nil {
		logf("awake", "巡检任务立即执行失败: %v", err)
	}
	return waitSupervisor(5 * time.Second)
}

// waitSupervisor 等待守护进程拿到互斥体并写入 runtime.json
func waitSupervisor(d time.Duration) int {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if pid := supervisorPID(); pid != 0 {
			return pid
		}
	}
	return 0
}

// waitHolding 等待守护进程真正进入 keep（电源设置已改完）。
// acquire 返回时就应该已经生效，agent 不必猜时间。
func waitHolding(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if rt := readRuntime(); rt != nil && rt.Mode == "keep" {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
