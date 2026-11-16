package main

// traffic.go — 任务栏胶囊的窗口与摆位
//
// 胶囊本身由 mygo 原生 ui 绘制（见 barnative.go）：深色圆角胶囊 + 顶部渐变高光，
// 保持中时整块泛绿并呼吸。Go 只负责窗口尺寸、位置与状态。
//
// 窗口默认是透明的（真圆角）；只有实验性的嵌入模式用不透明 + 采样任务栏底色。

import (
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// placeTrafficBar 上方模式：主屏右下角、任务栏上方。
// mygo 的 WorkArea / SetSize / SetPosition 都是 DIP，全程不要混入物理像素。
func (a *app) placeTrafficBar() {
	d := mygo.Screen.PrimaryDisplay()
	w, h := a.currentBarSize()
	x := d.WorkArea.X + d.WorkArea.Width - w - 12 + a.cfg.OffsetX
	y := d.WorkArea.Y + d.WorkArea.Height - h - 4
	a.bar.SetPosition(x, y)
}

// barWidthFor 计算当前状态需要的胶囊宽度（中文约 12px/字，西文 7px）
//
// barNativePad 是原生文字比这套估算多出来的宽度：网页版 .pill 的实际内边距
// 是 12+12（估算只算了 16），加上原生中文字宽约 13 而不是 12。实测「空闲」
// 「保持中」正好差 20px，宁可多给一点内边距也不能让字挤出胶囊。
func (a *app) barWidthFor() int {
	label := a.statusLabel()
	count := leaseCountText(activeCount(a.snap.Leases))

	labelW := len([]rune(label)) * 12
	countW := 0
	for _, r := range count {
		if r < 128 {
			countW += 7
		} else {
			countW += 12
		}
	}
	w := 20 + labelW + countW + 16 + barNativePad // 圆点 + 间距 + 内边距 + 原生更正
	if w < 88 {
		w = 88
	}
	if w > 280 {
		w = 280
	}
	return w
}

// sampleTaskbarColor 取任务栏底色（多点取样取最暗，避免采到图标的浅色）
func sampleTaskbarColor() string {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return "#1c1c1c"
	}
	defer procReleaseDC.Call(0, hdc)

	cw, _, _ := procGetSystemMetrics.Call(0)
	ch, _, _ := procGetSystemMetrics.Call(1)

	best := -1
	bestSum := 1 << 30
	for _, frac := range []float64{0.38, 0.5, 0.62} {
		x := uintptr(int(float64(cw) * frac))
		y := uintptr(int(ch) - 8)
		v, _, _ := procGetPixel.Call(hdc, x, y)
		u := uint32(v)
		if u == 0xFFFFFFFF {
			continue
		}
		r := int(u & 0xFF)
		g := int((u >> 8) & 0xFF)
		b := int((u >> 16) & 0xFF)
		if s := r + g + b; s < bestSum {
			bestSum = s
			best = int(u)
		}
	}
	if best < 0 {
		return "#1c1c1c"
	}
	r := byte(best & 0xFF)
	g := byte((best >> 8) & 0xFF)
	b := byte((best >> 16) & 0xFF)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func (a *app) ensureBar() {
	if a.bar != nil {
		return
	}
	a.barW = a.barWidthFor()
	w, h := a.currentBarSize()

	// 嵌入模式（AGENTAWAKE_EMBED=1，实验性）：
	//   · 子窗口没有 DWM 逐像素透明，只能退回「采样任务栏底色 + 不透明」；
	//   · mygo 的 GPU 面在 SetParent 时会失效（画面全黑），必须先让窗口
	//     画出来，再挂进任务栏。
	embed := os.Getenv("AGENTAWAKE_EMBED") == "1" && findTrayHost() != 0
	a.barBG = ""
	a.barMode = "覆盖任务栏"
	if embed {
		a.barBG = sampleTaskbarColor()
		a.barMode = "嵌入任务栏"
	}

	a.bar = mygo.NewWindow(mygo.WindowOptions{
		Title:           "AgentAwake",
		X:               -32000,
		Y:               -32000,
		Width:           w,
		Height:          h,
		Frameless:       true,
		AlwaysOnTop:     true,
		SkipTaskbar:     true,
		DisableResize:   true,
		DisableShadow:   true,
		UseContentSize:  true,
		Transparent:     !embed,
		BackgroundColor: a.barBG,
		Content:         ui.View(a.barView),
	})
	a.bar.OnClosed(func() {
		if !a.quitting {
			logf("panel", "胶囊窗口被关闭（不是退出触发的）")
		}
		a.bar = nil
	})
	dbg("ensureBar hwnd=%d size=%dx%d embed=%v bg=%q", a.bar.NativeHandle(), w, h, embed, a.barBG)

	if embed {
		// 先画 0.7s 再挂：直接 SetParent 会让 GPU 面失效
		time.AfterFunc(700*time.Millisecond, func() {
			mygo.RunOnMain(func() {
				if a.bar == nil {
					return
				}
				if a.embedIntoTaskbar() {
					a.embedded = true
					logf("panel", "胶囊已嵌入任务栏")
				} else {
					a.embedded = false
					a.overTaskbar = true
					a.barMode = "覆盖任务栏"
					a.positionOverTaskbar()
					logf("panel", "嵌入任务栏失败，改用覆盖模式")
				}
			})
		})
	} else {
		a.embedded = false
		a.overTaskbar = true
		a.positionOverTaskbar()
	}
	if a.cfg.ShowBar {
		a.showBar()
	} else {
		a.bar.Hide()
	}
	// 首次显示时窗口尺寸可能还没生效，稍后再摆一次位置（保证垂直居中）。
	// applyPosition 会碰 a.bar/a.cfg 并调 Win32，必须回主线程——
	// 与 checkTaskbar/syncBarPresence 的写操作保持同一线程。
	go func() {
		time.Sleep(300 * time.Millisecond)
		mygo.RunOnMain(a.applyPosition)
	}()
	go a.watchTaskbar()
	go a.keepOnTop()
}

// barModeText 给日志用的胶囊模式文案
func (a *app) barModeText() string {
	if !a.cfg.ShowBar {
		return "隐藏"
	}
	if a.barMode == "" {
		return "未知"
	}
	return a.barMode
}

// currentBarSize 当前胶囊尺寸
func (a *app) currentBarSize() (int, int) {
	w := a.barW
	if w <= 0 {
		w = a.barWidthFor()
	}
	return w, 32
}

// syncBarSize 内容变化时调整宽度并重新定位
func (a *app) syncBarSize() {
	if a.bar == nil {
		return
	}
	w := a.barWidthFor()
	if w != a.barW {
		a.barW = w
		_, h := a.currentBarSize()
		a.bar.SetSize(w, h)
	}
	a.applyPosition()
}

// leaseCountText 胶囊上的租约计数文案
func leaseCountText(n int) string { return fmt.Sprintf("租约 %d", n) }
