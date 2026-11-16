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
	// 注意：这里不能无条件声明「系统别睡」。启动时还不知道有没有租约，
	// 无租约的空转期最长 90 秒——那段时间不该拦着系统睡觉。
	// 唤醒锁由下面的主循环在「有活跃租约」的分支里声明。

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
		// 兜底：异常退出也不能把系统永久钉在「从不」。
		// 快照是恢复原值的唯一依据：没有租约才恢复，恢复后必须把快照清掉——
		// 留着它，下次保持会沿用旧方案的恢复值（中途换过方案就会写到错误的方案上）。
		cur := readRuntime()
		if cur != nil {
			if cur.Mode == "keep" && len(loadLeases()) == 0 {
				restoreSnapshot(cur.Snapshot)
				cur.Snapshot = nil
				cur.Mode = "idle"
				cur.ActiveCount = 0
				cur.LeaseIDs = nil
				logf("supervisor", "守护进程退出，已恢复电源设置")
			}
			// 还有租约时保持 Mode=keep 与 Snapshot 原样：
			// 下一个守护进程据此继续保持，并能检测电源方案切换后迁移快照。
			setExecutionState(esContinuous)
			cur.SupervisorPID = 0
			saveRuntime(cur)
		}
	}()

	var lastHeal time.Time
	var idleSince time.Time
	var lastSaved string
	warnedMissingSnapshot := false

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

			// 无论当前是 idle 还是 keep，先校正「快照 vs 当前电源方案」：
			// 快照属于另一个方案时（用户中途换过方案，或旧版本异常退出留下
			// 的残留），先把旧方案恢复原值再丢弃它——否则恢复值会写到错误
			// 的方案上，新方案会永久停在「从不睡眠」。
			migrated := false
			if snap := rt.Snapshot; snap != nil && snap.Scheme != "" {
				if cur := activeScheme(); cur != "" && cur != snap.Scheme {
					logf("supervisor", "电源方案切换 %s -> %s，迁移保持状态", snap.Scheme, cur)
					restoreSnapshot(snap)
					rt.Snapshot = nil
					migrated = true
				}
			}

			// 快照缺失且没有发生迁移（旧版本残留或文件被改坏）：这次不碰电源设置。
			// 没有原值就改，等于改了恢复不了；唤醒锁 ES_SYSTEM_REQUIRED
			// 本身已经能阻止空闲睡眠，电源设置只是双保险。
			if !migrated && rt.Mode == "keep" && rt.Snapshot == nil {
				if !warnedMissingSnapshot {
					logf("supervisor", "保持状态缺快照，本轮只靠唤醒锁、不改电源设置")
					warnedMissingSnapshot = true
				}
			} else if rt.Mode != "keep" || migrated {
				// 首次保持，或换过方案后重建：先记原值，再改
				if rt.Snapshot == nil {
					rt.Snapshot = captureSnapshot()
				}
				applyKeep(rt.Snapshot)
				if rt.Mode != "keep" {
					logf("supervisor", "进入保持模式（%d 个租约），已改为不休眠", len(active))
				}
				rt.Mode = "keep"
				rt.LastHeal = stamp(now)
				lastHeal = now
			} else if now.Sub(lastHeal) >= healPeriod {
				applyKeep(rt.Snapshot)
				rt.LastHeal = stamp(now)
				lastHeal = now
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
