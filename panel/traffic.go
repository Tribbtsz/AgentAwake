package main

// traffic.go — 任务栏胶囊（原生绘制，覆盖在任务栏空白区上）
//
// 深色胶囊 + 顶部 1px 高光，垂直居中；点击打开面板。

import (
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// barView 胶囊内容：原生绘制（透明窗口上画圆角矩形）
func (a *app) barView(c *ui.Context) {
	dotColor := ui.Hex("#9AA0A6")
	switch {
	case a.snap.Err != "" && !a.snap.Holding:
		dotColor = ui.Hex("#D4796F")
	case a.snap.Holding:
		dotColor = ui.Hex("#7FC99B")
	}
	n := activeCount(a.snap.Leases)

	ui.Box(c).Fill().Children(func() {
		if ui.ButtonBase(c).Clicked() {
			a.showPanel()
		}
		ui.Box(c).Fill().PaddingY(3).Radius(15).
			Background(ui.RGBA(48, 52, 60, 255)).Children(func() {
			ui.Row(c).Fill().Center().Gap(4).PaddingX(8).Children(func() {
				ui.Text(c, "●").FontSize(9).TextColor(dotColor).NoWrap()
				ui.Text(c, a.statusLabel()).FontSize(11).TextColor(ui.Hex("#E8EAED")).NoWrap()
				ui.Text(c, leaseCountText(n)).FontSize(11).TextColor(ui.Hex("#A9AFB7")).NoWrap()
			})
		})
	})
}

// barWidthFor 计算当前状态需要的胶囊宽度（中文约 12px/字，西文 7px）
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
	w := 20 + labelW + countW + 16 // 圆点 + 间距 + 左右内边距
	if w < 88 {
		w = 88
	}
	if w > 280 {
		w = 280
	}
	return w
}

// placeTrafficBar 上方模式：主屏右下角、任务栏上方
func (a *app) placeTrafficBar() {
	d := mygo.Screen.PrimaryDisplay()
	w, h := a.currentBarSize()
	scale := d.ScaleFactor
	if scale <= 0 {
		scale = 1
	}
	pw, ph := int(float64(w)*scale), int(float64(h)*scale)
	x := d.WorkArea.X + d.WorkArea.Width - pw - 12
	y := d.WorkArea.Y + d.WorkArea.Height - ph - 4
	x += int(float64(a.cfg.OffsetX) * scale)
	a.bar.SetPosition(x, y)
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
		BackgroundColor: sampleTaskbarColor(),
		URL:             "/bar.html",
	})
	a.bar.OnClosed(func() { a.bar = nil })
	dbg("ensureBar hwnd=%d size=%dx%d", a.bar.NativeHandle(), w, h)

	if os.Getenv("AGENTAWAKE_EMBED") == "1" && a.embedIntoTaskbar() {
		a.embedded = true
	} else {
		a.embedded = false
		a.overTaskbar = true
		a.positionOverTaskbar()
	}
	if a.cfg.ShowBar {
		a.bar.ShowInactive()
	} else {
		a.bar.Hide()
	}
	// 首次显示时窗口尺寸可能还没生效，稍后再摆一次位置（保证垂直居中）
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.applyPosition()
	}()
	go a.watchTaskbar()
	go a.keepOnTop()
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
