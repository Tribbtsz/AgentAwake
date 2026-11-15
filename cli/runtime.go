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

// writeRuntime 写入并返回序列化结果（给「内容没变就不写」用）
func writeRuntime(rt *Runtime) (string, error) {
	ensureDirs()
	b, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := runtimePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return string(b), err
	}
	return string(b), os.Rename(tmp, runtimePath())
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

// startSupervisor 以完全脱离的方式拉起守护进程（无窗口、不继承当前控制台）
func startSupervisor() int {
	if pid := supervisorPID(); pid != 0 {
		return pid
	}

	cmd := exeCommand("supervisor")
	if err := cmd.Start(); err != nil {
		logf("awake", "守护进程启动失败: %v", err)
		return 0
	}
	// 等它拿到互斥体并把自己写进 runtime.json
	for i := 0; i < 60; i++ {
		time.Sleep(100 * time.Millisecond)
		if pid := supervisorPID(); pid != 0 {
			return pid
		}
	}
	return 0
}
