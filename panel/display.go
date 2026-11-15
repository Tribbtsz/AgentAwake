package main

// display.go — 手动关闭显示器
//
// Windows 没有「只关屏」的正式 API：标准做法是给所有顶层窗口广播
// WM_SYSCOMMAND / SC_MONITORPOWER(2)，由 shell 负责真正关屏。
// 任何键鼠输入都会立刻把屏幕唤回来——这个按钮只负责「关」，不负责「别亮」。
//
// 为什么需要它：系统自己的熄屏计时偶尔会被别的进程（播放器、Electron 应用）
// 挂着的「显示请求」压住，这时给用户一个立刻黑屏的出口。

import (
	"fmt"
)

const (
	wmSysCommand    = 0x0112
	scMonitorPower  = 0xF170
	monitorPowerOff = 2
	hwndBroadcast   = 0xFFFF
	smtoAbortIfHung = 0x0002
)

var procSendMessageTimeout = user32.NewProc("SendMessageTimeoutW")

// turnOffDisplay 广播关屏命令。返回 error 只在完全没人处理时才有值。
func turnOffDisplay() error {
	r, _, err := procSendMessageTimeout.Call(
		hwndBroadcast, wmSysCommand, scMonitorPower, monitorPowerOff,
		smtoAbortIfHung, 1000, 0)
	if r == 0 {
		// 广播没人响应时再试前台窗口（有些桌面外壳只在活动窗口上处理）
		if fg, _, _ := procGetForegroundWindow.Call(); fg != 0 {
			if r2, _, _ := procSendMessageTimeout.Call(
				fg, wmSysCommand, scMonitorPower, monitorPowerOff,
				smtoAbortIfHung, 1000, 0); r2 != 0 {
				return nil
			}
		}
		return fmt.Errorf("关闭显示器没有响应: %v", err)
	}
	return nil
}
