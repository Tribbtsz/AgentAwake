package main

// icon.go — 内嵌品牌图标（由生图流水线产出，assets/ 下按尺寸导出）
//
// 托盘用 32px（Windows 通知区按 16pt@2x），出错时用 16px 区分状态。
// 面板网页里的图标是 web/icon.png，不需要在这里解码。

import (
	_ "embed"
)

//go:embed assets/icon-32.png
var iconPNG32 []byte

//go:embed assets/icon-16.png
var iconPNG16 []byte
