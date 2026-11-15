package main

// win.go — Windows 底层：路径解析、隐藏启动子进程、进程信息、单实例互斥体
//
// 约定：本程序不依赖任何硬编码盘符。可执行文件在哪、数据在哪，都从系统取。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow     = 0x08000000
	detachedProcess    = 0x00000008
	createNewProcGroup = 0x00000200
	waitObject0        = 0x00000000
	waitAbandoned      = 0x00000080
	waitTimeout        = 0x00000102

	// 只要求「系统别睡」，不要求「屏幕别关」——屏幕该黑就黑
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

// exePath 当前可执行文件的真实路径（解析符号链接后的绝对路径）
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "awake.exe"
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// exeDir 可执行文件所在目录；所有相对资源都相对它，不受当前工作目录影响
func exeDir() string { return filepath.Dir(exePath()) }

// runHidden 执行外部命令，绝不弹窗口（powercfg / schtasks）
func runHidden(name string, args ...string) (string, error) {
	return runHiddenTimeout(15*time.Second, name, args...)
}

// runHiddenTimeout 同上，但带超时：系统命令卡住时不能把守护进程一起拖死
func runHiddenTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// exeCommand 构造本程序的子进程：完全脱离控制台、不弹窗、不继承调用方的输出句柄
// （旧 PowerShell 版把输出重定向时之所以会挂住，就是子进程继承了那个管道）
func exeCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(exePath(), args...)
	cmd.Dir = exeDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcGroup,
	}
	// Stdin/Stdout/Stderr 留 nil → 系统自动接到 NUL
	return cmd
}

// processImage 取进程镜像路径与创建时间（100ns ticks，与 PowerShell 的 StartTime.Ticks 一致）
func processImage(pid int) (image string, startTicks int64, ok bool) {
	if pid <= 0 {
		return "", 0, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", 0, false
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 4096)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", 0, false
	}
	// 不能按 size 截断：系统返回的长度可能带 NUL 或多余字符，
	// 直接整体转换（缓冲区零初始化，转换会在第一个 NUL 停住）。
	image = windows.UTF16ToString(buf)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err == nil {
		startTicks = creation.Nanoseconds() / 100
	}
	return image, startTicks, true
}

// processAlive 进程是否还活着；给了 startTicks 还会校验创建时间，避免 PID 被复用
func processAlive(pid int, startTicks int64) bool {
	_, ticks, ok := processImage(pid)
	if !ok {
		return false
	}
	if startTicks != 0 && ticks != 0 && ticks != startTicks {
		return false
	}
	return true
}

// supervisorRunning 守护进程是否活着：它整个生命周期都持有这个互斥体，
// 所以我们只要能「拿到」互斥体，就说明没有守护进程。比比对进程路径可靠。
func supervisorRunning() bool {
	p, err := windows.UTF16PtrFromString(supervisorMutexName)
	if err != nil {
		return false
	}
	// 注意：对象已存在时 CreateMutex 会带着 err=ERROR_ALREADY_EXISTS 返回有效句柄，
	// 不能把这个 err 当成失败。
	h, err := windows.CreateMutex(nil, false, p)
	if h == 0 {
		return false
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return false
	}
	defer windows.CloseHandle(h)

	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	switch ev {
	case waitTimeout:
		return true // 别人持有 → 守护进程在跑
	case waitObject0, waitAbandoned:
		_ = windows.ReleaseMutex(h) // 我们自己拿到了 → 没人持有
		return false
	}
	return false
}

// acquireMutex 拿一个本地单实例互斥体；拿到返回句柄，已被占用返回 false
func acquireMutex(name string) (windows.Handle, bool) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, false
	}
	h, err := windows.CreateMutex(nil, false, p)
	if h == 0 {
		return 0, false
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return 0, false
	}
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil || (ev != waitObject0 && ev != waitAbandoned) {
		windows.CloseHandle(h)
		return 0, false
	}
	return h, true
}

// setExecutionState 声明线程的电源需求（必须在固定线程上调用）
func setExecutionState(flags uintptr) {
	procSetThreadExecutionState.Call(flags)
}
