package main

// supervisor.go — 守护进程（内部使用，别手动启动）
//
// 职责：
//   · 有活跃租约时：睡眠/休眠改成「从不」，并持续声明「系统别睡」
//   · 租约全部结束（释放 / 进程退出 / 到点 / 心跳超时）：恢复原电源设置
//   · 每 5 分钟自检：设置被别的程序改回去就改回来
//   · 空闲 90 秒且确实没有租约了：退出，不留常驻进程

import (
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

const (
	supervisorMutexName = `Local\AgentAwakeSupervisor`
	idleExitAfter       = 90 * time.Second
	healPeriod          = 5 * time.Minute
	loopPeriod          = time.Second
)

func runSupervisor() int {
	ensureDirs()

	mutex, ok := acquireMutex(supervisorMutexName)
	if !ok {
		logf("supervisor", "已有守护进程在运行，退出")
		return 0
	}
	defer func() {
		_ = windows.CloseHandle(mutex)
	}()

	// SetThreadExecutionState 是「按线程」生效的：把当前 goroutine 钉在一个 OS 线程上，
	// 之后所有调用都在同一个线程，声明的状态才不会丢。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setExecutionState(esContinuous | esSystemRequired)

	now := time.Now()
	rt := readRuntime()
	if rt == nil {
		rt = &Runtime{}
	}
	rt.SupervisorPID = os.Getpid()
	rt.StartedAt = stamp(now)
	if rt.Mode == "" {
		rt.Mode = "idle"
	}
	saveRuntime(rt)
	logf("supervisor", "守护进程启动 PID %d", rt.SupervisorPID)

	defer func() {
		// 兜底：异常退出也不要把系统永久钉在「从不」
		cur := readRuntime()
		if cur != nil && cur.Mode == "keep" && len(loadLeases()) == 0 {
			restoreSnapshot(cur.Snapshot)
			logf("supervisor", "守护进程退出，已恢复电源设置")
		}
		setExecutionState(esContinuous)
		cur = readRuntime()
		if cur != nil {
			cur.SupervisorPID = 0
			cur.Mode = "idle"
			saveRuntime(cur)
		}
	}()

	var lastHeal time.Time
	var idleSince time.Time
	var lastSaved string

	for {
		now = time.Now()

		// 清掉已失效的租约文件，并记下原因（历史账本里定时类用真实到期点）
		for _, l := range loadLeases() {
			if reason := inactiveReason(l, now); reason != "" {
				end := leaseEndTime(l, now, reason)
				recordLeaseEnd(l, end, reason)
				logf("supervisor", "租约结束 %s [%s] key=%s 原因=%s 持续=%s",
					l.ID, l.Name, l.Key, reason, leaseHoldDuration(l, end))
				removeLease(l.ID)
			}
		}

		active := activeLeases(now)

		if len(active) > 0 {
			idleSince = time.Time{}

			if rt.Mode != "keep" {
				// 第一次进入保持：先记原值，再改
				if rt.Snapshot == nil {
					rt.Snapshot = captureSnapshot()
				}
				applyKeep()
				rt.Mode = "keep"
				rt.LastHeal = stamp(now)
				lastHeal = now
				logf("supervisor", "进入保持模式（%d 个租约），已改为不休眠", len(active))
			} else {
				migrated := false
				if snap := rt.Snapshot; snap != nil && snap.Scheme != "" {
					if cur := activeScheme(); cur != "" && cur != snap.Scheme {
						// 用户中途换了电源方案：旧方案恢复原值，新方案重新记录并保持，
						// 否则会把恢复值写到错误的方案上。
						logf("supervisor", "电源方案切换 %s -> %s，迁移保持状态", snap.Scheme, cur)
						restoreSnapshot(snap)
						rt.Snapshot = captureSnapshot()
						applyKeep()
						rt.LastHeal = stamp(now)
						lastHeal = now
						migrated = true
					}
				}
				if !migrated && now.Sub(lastHeal) >= healPeriod {
					applyKeep()
					rt.LastHeal = stamp(now)
					lastHeal = now
				}
			}

			setExecutionState(esContinuous | esSystemRequired)
			rt.ActiveCount = len(active)
			rt.LeaseIDs = make([]string, 0, len(active))
			for _, l := range active {
				rt.LeaseIDs = append(rt.LeaseIDs, l.ID)
			}
		} else {
			if rt.Mode == "keep" {
				restoreSnapshot(rt.Snapshot)
				setExecutionState(esContinuous)
				logf("supervisor", "租约已全部结束，恢复原来的电源设置")
				rt.Mode = "idle"
				rt.Snapshot = nil
				rt.ActiveCount = 0
				rt.LeaseIDs = nil
			}

			if idleSince.IsZero() {
				idleSince = now
			}
			// 空闲够久、且确实没有租约了，就收摊
			// （空闲期不再逐 15 秒刷日志：进入空闲与空闲退出本就有记录，事件流要干净）
			if now.Sub(idleSince) >= idleExitAfter && len(loadLeases()) == 0 {
				logf("supervisor", "空闲 %.0f 秒后退出", now.Sub(idleSince).Seconds())
				removeWatchdogTask()
				break
			}
		}

		saveRuntimeIfChanged(rt, &lastSaved)
		time.Sleep(loopPeriod)
	}
	return 0
}
