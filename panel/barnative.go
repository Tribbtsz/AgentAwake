package main

// barnative.go — 任务栏胶囊：mygo 原生 ui 绘制
//
// 为什么改：胶囊是常驻的，而网页版会一直养着 WebView2 的 browser / GPU /
// network / storage / crashpad 五个进程（实测约 150 MB 私有内存，即使面板
// 关着）。改成原生绘制后，桌面态不再拉起 WebView2，只有打开面板才有那棵树。
//
// 视觉与原来的 bar.html 对齐：26 高 / 圆角 13 / 内边距 12 / 间距 6 /
// 竖向渐变（白 0.12→0.065→0.04 叠在 #2b2f37 上；保持中换成绿色系）。
// 呼吸是一条余弦曲线（1.6s），跑在「只重绘、不重建」的绘制帧上（20fps）：
// 每帧只画一个半透明绿色圆角矩形，文字排版不重来。
//
// 线程：barUI 只在主线程读写；barView 由 mygo 在每一帧的主线程上调用。

import (
	"math"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	// 与旧 bar.html 一致：窗口 32 高，胶囊 26 高、两侧留 8、圆角 13、内边距 12、间距 6
	barHeight  = 32
	pillHeight = 26
	pillInset  = 8
	pillPadX   = 12
	pillGap    = 6

	// 原生文字比网页版的宽度估算略宽（网页版其实靠文字轻微溢出内边距凑合）。
	// 实测「空闲」「保持中」两种状态，原公式各差 20px，这里补齐，宁可多一点
	// 内边距也不能把字挤出去。
	barNativePad = 20

	// 呼吸：余弦曲线，周期 1.6s；用「只重绘不重建」的绘制帧跑（20fps），
	// 每次只是画一个绿色圆角矩形，比整棵 view 重建便宜一个数量级。
	barBreathPeriod = 1600 * time.Millisecond
	barGlowFPS      = 20

	// 呼吸的深浅：那一层绿色的有效不透明度（0 = 不叠，1 = 纯绿）。
	// barGlowLow 是静态绿（关掉呼吸时就是这个值），barGlowHigh 是峰值。
	// 两者的间距就是肉眼看到的颜色变化幅度，想更明显就拉开。
	barGlowLow  = 0.09
	barGlowHigh = 0.46
)

const (
	barIdle = iota
	barOn
	barErr
)

// barUI 是胶囊的界面状态，只在主线程读写。
type barUI struct {
	kind   int     // barIdle / barOn / barErr
	down   bool    // 指针正按在胶囊上（只需触发一次打开面板）
	appear float32 // 出现动画的进度 0→1（1 = 完全显示）
	anim   int     // 出现动画的序号：新的动画让旧的停下
	sig    string  // 当前文案签名（label|count），状态没变就不重建
	// still 是系统「减少动画」偏好，由每一帧从 Context 读回；
	// 开启时只保留静态绿色，不呼吸。
	still bool
	// clicks 记录点击次数，供测试断言（生产里没有意义）
	clicks int
}

var (
	barFont = `Segoe UI Variable Text, Segoe UI, Microsoft YaHei UI`

	barLabelColor = ui.Hex("#E8EAED")
	barCountColor = ui.Hex("#A9AFB7")
	barDotIdle    = ui.Hex("#9AA0A6")
	barDotOn      = ui.Hex("#7FC99B")
	barDotErr     = ui.Hex("#D4796F")

	barBaseIdle = ui.Hex("#2b2f37")
	barBaseOn   = ui.Hex("#24382b")
	barWhite    = ui.RGBA(255, 255, 255, 1)
	barGreen    = ui.RGBA(127, 201, 155, 1)
)

// barKind 把 BarState 的字符串状态换成绘制用的枚举。
func barKind(state string) int {
	switch state {
	case "on":
		return barOn
	case "err":
		return barErr
	}
	return barIdle
}

func barDotColor(kind int) ui.Color {
	switch kind {
	case barOn:
		return barDotOn
	case barErr:
		return barDotErr
	}
	return barDotIdle
}

// barStops 把 CSS 的两层合成成一个渐变（底色 + 绿色渐变叠层）。
// 呼吸那一层 rgba(127,201,155,.55)×opacity 不在这里，由 barView 的 Draw
// 每帧画在渐变之上、文字之下——那才能只重绘不重建。
func barStops(kind int) []ui.GradientStop {
	if kind != barOn {
		top := barWhite.Alpha(0.12).Over(barBaseIdle)
		mid := barWhite.Alpha(0.065).Over(barBaseIdle)
		bot := barWhite.Alpha(0.04).Over(barBaseIdle)
		return []ui.GradientStop{{Color: top, At: 0}, {Color: mid, At: 0.46}, {Color: bot, At: 1}}
	}
	layer := func(alpha, at float32) ui.GradientStop {
		return ui.GradientStop{Color: barGreen.Alpha(alpha).Over(barBaseOn), At: at}
	}
	return []ui.GradientStop{layer(0.35, 0), layer(0.22, 0.52), layer(0.16, 1)}
}

// barGlow 返回 0..1 的呼吸相位：余弦，首尾都平缓，没有折点。
func barGlow(p *ui.Painter) float32 {
	period := float64(barBreathPeriod) / float64(time.Second)
	t := float64(p.Now().UnixNano()) / float64(time.Second)
	return float32(0.5 - 0.5*math.Cos(2*math.Pi*t/period))
}

// barView 画一帧胶囊。窗口底色在叠加模式是透明的（真圆角），
// 嵌入模式才用采样的任务栏底色（子窗口没有逐像素透明）。
func (a *app) barView(c *ui.Context) {
	// 系统「减少动画」：开启时保持静态绿色
	a.barUI.still = c.Preferences().ReduceMotion

	outer := ui.Box(c).Fill().Center()
	if a.barBG != "" {
		outer.Background(ui.Hex(a.barBG))
	} else {
		// 真透明：Box 和根节点都要显式透明。根节点默认会画主题背景（深灰
		// #18181b），只改 Box 的话圆角外会留一块浅黑。
		outer.Background(ui.Transparent)
		c.Root().Background(ui.Transparent)
	}

	// 用「按下」而不是「点击完成」触发：窗口不抢焦点，第一次按下会同时
	// 激活窗口，松开事件可能被系统吃掉——等 Click 的话要点两下才响。
	pressed := outer.Pressed()
	if pressed && !a.barUI.down {
		a.barUI.down = true
		a.barUI.clicks++
		dbg("胶囊被按下（第 %d 次）", a.barUI.clicks)
		if mygo.App.IsReady() {
			// 在 build 过程里直接建窗口不安全，排到主线程下一轮
			time.AfterFunc(time.Millisecond, func() { mygo.RunOnMain(a.showPanel) })
		}
	}
	if !pressed {
		a.barUI.down = false
	}

	outer.Children(func() {
		pill := ui.Box(c).
			Width(float32(a.barW - pillInset*2)).
			Height(pillHeight).
			Radius(pillHeight / 2).
			LinearGradient(ui.LinearGradient{Angle: 180, Stops: barStops(a.barUI.kind)}).
			Draw(a.barGlowFrame).
			Opacity(a.barUI.appear).
			Center()

		pill.Children(func() {
			row := ui.Row(c).Center().Gap(pillGap).Padding(0, pillPadX)
			row.Children(func() {
				// Shrink(0)：文字保持完整宽度，宁可略微溢出也压成省略号
				ui.Text(c, "●").Font(barFont).FontSize(8).Shrink(0).SingleLine().
					TextColor(barDotColor(a.barUI.kind))
				ui.Text(c, a.statusLabel()).Font(barFont).FontSize(12.5).FontWeight(500).
					Shrink(0).SingleLine().TextColor(barLabelColor)
				ui.Text(c, leaseCountText(activeCount(a.snap.Leases))).Font(barFont).FontSize(12.5).
					Shrink(0).SingleLine().TextColor(barCountColor)
			})
		})
	})
}

// barGlowFrame 画保持中的呼吸叠层（在渐变之上、文字之下）。
// 只画一个半透明绿色圆角矩形，然后用 AnimationFrameRate 只要绘制帧，
// 不重建 view——文字排版不会每帧重来。
func (a *app) barGlowFrame(p *ui.Painter, r ui.Rect) {
	if a.barUI.kind != barOn {
		return
	}
	glow := float32(0) // 0 = 低位静态绿
	if a.cfg.Pulse && !a.barUI.still {
		glow = barGlow(p)
		p.AnimationFrameRate(barGlowFPS)
	}
	alpha := barGlowLow + (barGlowHigh-barGlowLow)*glow
	p.Fill(r, barGreen.Alpha(alpha), pillHeight/2)
}

// updateBarState 刷新胶囊的状态色与文案（主线程）。
// 返回是否发生了变化，调用方据此决定要不要重排宽度。
func (a *app) updateBarState() bool {
	st := Api{a: a}.BarState()
	kind := barKind(st.State)
	sig := st.Label + "|" + st.Count
	if a.barUI.kind == kind && a.barUI.sig == sig {
		return false
	}
	a.barUI.kind, a.barUI.sig = kind, sig
	a.invalidate()
	return true
}

// showBar 显示胶囊并淡入。任务栏自动隐藏、全屏应用结束、explorer 重启之后
// 胶囊重新出现时，硬切一下有点生硬，改成 ~200ms 的淡入。
// 系统要求减少动画时直接显示。
//
// 点击行为：窗口用 ShowInactive 显示（不抢焦点），所以第一次按下会顺带激活
// 窗口、松开事件可能被系统吃掉；胶囊因此用「按下」而不是 Click 触发。
// 不要给它加 WS_EX_NOACTIVATE —— 实测加上之后鼠标消息完全收不到。
func (a *app) showBar() {
	if a.bar == nil {
		return
	}
	// 已经显示着就别再淡入一次（重新嵌入、重复调用会闪）
	if wasVisible := isWindowVisible(a.bar.NativeHandle()); wasVisible {
		a.bar.ShowInactive()
		a.barUI.appear = 1
		a.invalidate()
		return
	}
	a.bar.ShowInactive()
	if a.barUI.still {
		a.barUI.appear = 1
		a.invalidate()
		return
	}
	a.barUI.anim++
	token := a.barUI.anim
	a.barUI.appear = 0
	a.barUI.down = false // 隐藏时若还按着，松开事件可能收不到，别让下一次点击被吞
	a.invalidate()

	go func() {
		const frames = 12 // ~16ms 一帧，约 200ms
		for i := 1; i <= frames; i++ {
			time.Sleep(16 * time.Millisecond)
			k := float32(i) / frames
			eased := 1 - (1-k)*(1-k)*(1-k) // ease-out：起步快、收尾稳
			mygo.RunOnMain(func() {
				if a.bar == nil || a.barUI.anim != token {
					return // 窗口没了，或又出现了一次
				}
				a.barUI.appear = eased
				a.bar.Update(func() {})
			})
		}
	}()
}
