package main

// embed.go — 把任务胶囊小窗嵌入任务栏（Shell_TrayWnd 的子窗口）
//
// 原理与 TrafficMonitor 相同：SetParent 到 Shell_TrayWnd，
// SetWindowPos 摆到任务栏内空白区。Windows 11 的任务栏左侧是开始/搜索/
// 运行应用图标区，右侧是系统托盘区，中间是空白，我们把小窗放在中间偏右。
//
// 风险与兜底（都在 watchTaskbar 里处理）：
//   · explorer.exe 重启会重建任务栏 → 轮询发现父窗口变了/没了 → 重新嵌入
//   · 嵌入失败（系统改动、权限）→ 自动退回「任务栏上方」模式，绝不消失

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
)

var (
	user32                           = windows.NewLazySystemDLL("user32.dll")
	kernel32                         = windows.NewLazySystemDLL("kernel32.dll")
	gdi32                            = windows.NewLazySystemDLL("gdi32.dll")
	shell32                          = windows.NewLazySystemDLL("shell32.dll")
	procSHQueryUserNotificationState = shell32.NewProc("SHQueryUserNotificationState")
	procFindWindow                   = user32.NewProc("FindWindowW")
	procFindWindowEx                 = user32.NewProc("FindWindowExW")
	procSetParent                    = user32.NewProc("SetParent")
	procSetWindowPos                 = user32.NewProc("SetWindowPos")
	procGetWindowLongPtr             = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr             = user32.NewProc("SetWindowLongPtrW")
	procGetClassName                 = user32.NewProc("GetClassNameW")
	procGetWindowText                = user32.NewProc("GetWindowTextW")
	procGetWindowRect                = user32.NewProc("GetWindowRect")
	procGetClientRect                = user32.NewProc("GetClientRect")
	procIsWindowVisible              = user32.NewProc("IsWindowVisible")
	procWindowFromPoint              = user32.NewProc("WindowFromPoint")
	procSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop             = user32.NewProc("BringWindowToTop")
	procGetSystemMetrics             = user32.NewProc("GetSystemMetrics")
	procGetForegroundWindow          = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId     = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput            = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId           = kernel32.NewProc("GetCurrentThreadId")
	procGetAncestor                  = user32.NewProc("GetAncestor")
	procGetDC                        = user32.NewProc("GetDC")
	procReleaseDC                    = user32.NewProc("ReleaseDC")
	procGetPixel                     = gdi32.NewProc("GetPixel")
)

const gaRoot = 2 // GA_ROOT

const (
	// x64 上 HWND 是 64 位：TOPMOST/NOTOPMOST 必须写成 -1 / -2 的全 64 位形式，
	// 用 0xFFFFFFFF 会被 SetWindowPos 判为「无效窗口句柄」而直接失败。
	hwndTopmost    uintptr = ^uintptr(0) // HWND_TOPMOST (-1)
	hwndNotTopmost uintptr = ^uintptr(1) // HWND_NOTOPMOST (-2)
)

const (
	swPNoZorder      = 0x0004
	swPNoActivate    = 0x0010
	swPShowWindow    = 0x0040
	swPNoMove        = 0x0002
	swPNoSize        = 0x0001
	swPNoOwnerZorder = 0x0200
	swPFrameChanged  = 0x0020
	gwlStyle         = -16
	gwlExStyle       = -20
	wsChild          = 0x40000000
	wsPopUp          = 0x80000000
	wsExToolWindow   = 0x00000080
	hwndInsertAfter  = 0 // HWND_TOP
)

func findWindow(class string) uintptr {
	ptr, err := syscall.UTF16PtrFromString(class)
	if err != nil {
		return 0
	}
	h, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(ptr)), 0)
	return h
}

// findTrayHost 找任务栏主窗口。Win11 仍叫 Shell_TrayWnd。
func findTrayHost() uintptr {
	return findWindow("Shell_TrayWnd")
}

// taskbarRect 取任务栏窗口的屏幕矩形（物理像素）
func taskbarRect() (left, top, right, bottom int, ok bool) {
	h := findTrayHost()
	if h == 0 {
		return 0, 0, 0, 0, false
	}
	var rc struct{ L, T, R, B int32 }
	r, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc)))
	if r == 0 {
		return 0, 0, 0, 0, false
	}
	return int(rc.L), int(rc.T), int(rc.R), int(rc.B), true
}

// embedIntoTaskbar 把窗口挂进任务栏并摆位。成功返回 true。
func (a *app) embedIntoTaskbar() bool {
	if a.bar == nil {
		return false
	}
	tray := findTrayHost()
	if tray == 0 {
		return false
	}
	hwnd := a.bar.NativeHandle()
	if hwnd == 0 {
		return false
	}

	// 已经是它的子窗口就不用再 SetParent（explorer 重启后父窗口变化时再挂）
	if parent := getParent(hwnd); parent == tray {
		return a.positionInTaskbar()
	}

	// 先改成 child 风格再 SetParent，避免焦点/激活怪象
	style := getWindowLong(hwnd, gwlStyle)
	setWindowLong(hwnd, gwlStyle, (style&^wsPopUp)|wsChild)
	ok := setParent(hwnd, tray)
	if !ok {
		// 失败恢复弹出风格
		setWindowLong(hwnd, gwlStyle, style)
		return false
	}

	a.embedded = true
	return a.positionInTaskbar()
}

// positionInTaskbar 在任务栏内摆位。
// 关键：SetParent 之后窗口是任务栏的子窗口，SetWindowPos 的坐标是
// 相对父窗口客户区的（左上角为 0,0），不是屏幕坐标。
// 摆位 = 默认位置（居左，避开左侧按钮区） + 用户偏移（offsetX，滑条调节）。
func (a *app) positionInTaskbar() bool {
	if a.bar == nil {
		return false
	}
	tray := findTrayHost()
	if tray == 0 {
		return false
	}
	var rc struct{ L, T, R, B int32 }
	if r, _, _ := procGetClientRect.Call(tray, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return false
	}
	clientW := int(rc.R - rc.L)
	clientH := int(rc.B - rc.T)
	if clientH <= 0 {
		return false
	}

	barW, barH := a.currentBarSize()
	// 任务栏内容尺寸即物理像素；窗口尺寸和坐标按主屏 ScaleFactor 从 DIP 换算
	scale := 1.0
	if d := mygo.Screen.PrimaryDisplay(); d.ScaleFactor > 0 {
		scale = float64(d.ScaleFactor)
	}
	physW := int(float64(barW) * scale)
	physH := int(float64(barH) * scale)
	if physH > clientH {
		physH = clientH - 4
	}

	// 默认位置：左侧按钮区（开始+搜索+任务视图）约 220 逻辑像素，再留 8px 边距
	defaultX := int(220*scale) + int(8*scale)
	// 用户偏移也是逻辑像素，同样换算
	userX := int(float64(a.cfg.OffsetX) * scale)

	x := defaultX + userX
	// clamp 在任务栏客户区内
	if x < 0 {
		x = 0
	}
	if x+physW > clientW {
		x = clientW - physW
	}
	y := (clientH - physH) / 2
	if y < 0 {
		y = 0
	}

	r1, _, _ := procSetWindowPos.Call(
		a.bar.NativeHandle(), hwndInsertAfter,
		uintptr(x), uintptr(y),
		uintptr(physW), uintptr(physH),
		// 这里不能用 SWP_NOZORDER：那会让 HWND_TOP 失效，窗口被压在任务栏
		// 自己的 XAML 合成层下面，位置对但看不见。
		uintptr(swPNoActivate|swPShowWindow),
	)
	return r1 != 0
}

// keepOnTop 保持任务栏胶囊可见，但任务栏被自动隐藏或前台全屏时收起。
// 每 300ms 回到主线程检查一次（开销只有几个 Win32 调用）。
func (a *app) keepOnTop() {
	for {
		time.Sleep(300 * time.Millisecond)
		mygo.RunOnMain(a.syncBarPresence)
	}
}

// syncBarPresence 根据任务栏/前台状态显示或收起胶囊
func (a *app) syncBarPresence() {
	if a.bar == nil || !a.cfg.ShowBar || !a.overTaskbar {
		return
	}
	if a.barSuppressed {
		if barShouldShow() {
			a.barSuppressed = false
			a.applyPosition()
			a.showBar()
		}
		return
	}
	if !barShouldShow() {
		a.barSuppressed = true
		a.bar.Hide()
		return
	}
	h := a.bar.NativeHandle()
	if h == 0 || !isWindowVisible(h) {
		return
	}
	if isOccluded(h) {
		dbg("胶囊被遮挡，重申 topmost")
		procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0,
			uintptr(swPNoMove|swPNoSize|swPNoActivate))
	}
}

// barShouldShow 任务栏被自动隐藏（只剩几像素）、或前台是全屏应用/演示模式时，
// 胶囊不应该浮在上面。
func barShouldShow() bool {
	l, t, r, b, ok := taskbarRect()
	if !ok {
		return false
	}
	if r-l < 8 || b-t < 8 {
		return false
	}
	return !shellBusy()
}

// shellBusy 前台全屏 / 演示模式：Windows 自己的通知状态查询
func shellBusy() bool {
	var state int32
	r, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	if r != 0 {
		return false // 查询失败时按正常处理，不隐藏
	}
	switch state {
	case 2, 3, 4: // QUNS_BUSY / QUNS_RUNNING_D3D_FULL_SCREEN / QUNS_PRESENTATION_MODE
		return true
	}
	return false
}

// isOccluded 判断窗口中心点是否被别的窗口盖住。
// 注意 WindowFromPoint 返回的是盖在那里的**最子级**窗口（我们的胶囊是
// MyGoSurface 子窗口），所以要用 GetAncestor 取根窗口再比。
func isOccluded(h uintptr) bool {
	var rc struct{ L, T, R, B int32 }
	if r, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return false
	}
	at := windowFromPoint((rc.L+rc.R)/2, (rc.T+rc.B)/2)
	if at == 0 {
		return false
	}
	root, _, _ := procGetAncestor.Call(at, gaRoot)
	return root != h && at != h
}

// windowFromPoint 取屏幕点上的窗口。注意 POINT 是**按值**传的（x64 一个
// 寄存器，32 位下两个 DWORD）；传指针会让 API 读到垃圾坐标，结果永远判成
// 「被遮挡」。
func windowFromPoint(x, y int32) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		r, _, _ := procWindowFromPoint.Call(uintptr(uint32(x)) | uintptr(uint32(y))<<32)
		return r
	}
	r, _, _ := procWindowFromPoint.Call(uintptr(uint32(x)), uintptr(uint32(y)))
	return r
}

func isWindowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// positionOverTaskbar 覆盖模式：窗口保持顶层，直接贴在任务栏的空白区域上。
// 比 SetParent 稳妥：不改父子关系，MyGo 的 GPU 绘制面不会被破坏。
// 视觉上和「嵌在任务栏里」一样，因为它正好盖住任务栏的一条空白带。
func (a *app) positionOverTaskbar() bool {
	if a.bar == nil {
		return false
	}
	l, t, r, b, ok := taskbarRect()
	if !ok {
		return false
	}
	tbH := b - t
	tbW := r - l
	if tbH > tbW {
		return false // 垂直任务栏不适用
	}

	barW, barH := a.currentBarSize()
	scale := 1.0
	if d := mygo.Screen.PrimaryDisplay(); d.ScaleFactor > 0 {
		scale = float64(d.ScaleFactor)
	}
	physW := int(float64(barW) * scale)
	physH := int(float64(barH) * scale)
	// 页面自适应宽度时，用窗口真实尺寸摆放
	var wr struct{ L, T, R, B int32 }
	if rc, _, _ := procGetWindowRect.Call(a.bar.NativeHandle(), uintptr(unsafe.Pointer(&wr))); rc != 0 {
		if realW := int(wr.R - wr.L); realW > 0 {
			physW = realW
		}
		if realH := int(wr.B - wr.T); realH > 0 {
			physH = realH
		}
	}
	if physH > tbH {
		physH = tbH
	}

	// 顶层窗口用屏幕坐标（不是客户区坐标）
	defaultX := l + int(220*scale) + int(8*scale)
	x := defaultX + int(float64(a.cfg.OffsetX)*scale)
	if x < l {
		x = l
	}
	if x+physW > r {
		x = r - physW
	}
	y := t + (tbH-physH)/2
	if y < t {
		y = t
	}

	r1, _, lerr := procSetWindowPos.Call(
		a.bar.NativeHandle(), hwndTopmost,
		uintptr(x), uintptr(y),
		uintptr(physW), uintptr(physH),
		uintptr(swPNoActivate|swPShowWindow),
	)
	dbg("positionOverTaskbar hwnd=%d tb=%d,%d,%d,%d -> %d,%d %dx%d ok=%v err=%v",
		a.bar.NativeHandle(), l, t, r, b, x, y, physW, physH, r1 != 0, lerr)
	dbg("  bar rect after: %s", windowRectText(a.bar.NativeHandle()))
	return r1 != 0
}

// windowRectText 打印窗口矩形，便于排查摆位问题
func windowRectText(h uintptr) string {
	var rc struct{ L, T, R, B int32 }
	if r, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return "GetWindowRect failed"
	}
	return fmt.Sprintf("%d,%d-%d,%d (%dx%d)", rc.L, rc.T, rc.R, rc.B, rc.R-rc.L, rc.B-rc.T)
}

func getParent(hwnd uintptr) uintptr {
	p, _, _ := user32.NewProc("GetParent").Call(hwnd)
	return p
}

func setParent(child, parent uintptr) bool {
	r, _, _ := procSetParent.Call(child, parent)
	return r != 0
}

func getWindowLong(hwnd uintptr, index int) uintptr {
	r, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(index))
	return r
}

func setWindowLong(hwnd uintptr, index int, value uintptr) uintptr {
	r, _, _ := procSetWindowLongPtr.Call(hwnd, uintptr(index), value)
	return r
}

// watchTaskbar 后台守护：explorer 重启后重新嵌入；彻底失败则退回覆盖模式
func (a *app) watchTaskbar() {
	for {
		time.Sleep(3 * time.Second)
		mygo.RunOnMain(a.checkTaskbar)
	}
}

// checkTaskbar 检查嵌入关系是否还在（主线程）
func (a *app) checkTaskbar() {
	if a.bar == nil || !a.embedded {
		return
	}
	hwnd := a.bar.NativeHandle()
	if hwnd == 0 {
		return
	}
	tray := findTrayHost()
	if tray == 0 {
		return // explorer 还没起来，继续等
	}
	if getParent(hwnd) == tray {
		return
	}
	// 任务栏被重建：重新嵌入
	style := getWindowLong(hwnd, gwlStyle)
	setWindowLong(hwnd, gwlStyle, (style&^wsPopUp)|wsChild)
	if setParent(hwnd, tray) && a.positionInTaskbar() {
		return
	}
	// 重挂失败 → 退回覆盖模式（与默认模式一致）
	a.embedded = false
	a.overTaskbar = true
	setWindowLong(hwnd, gwlStyle, (style&^wsChild)|wsPopUp)
	a.positionOverTaskbar()
	if a.cfg.ShowBar && barShouldShow() {
		a.barSuppressed = false
		a.showBar()
	}
	fmt.Println("[AgentAwake] 任务栏嵌入失效，已退回覆盖模式")
	dbg("任务栏嵌入失效，已退回覆盖模式")
}
