package main

// icon.go — 内嵌品牌图标（由生图流水线产出，assets/ 下按尺寸导出）
//
// 托盘用 32px（Windows 通知区按 16pt@2x），标题行用 48px。

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/icon-32.png
var iconPNG32 []byte

//go:embed assets/icon-32-bright.png
var iconPNG32Bright []byte

//go:embed assets/icon-48.png
var iconPNG48 []byte

//go:embed assets/icon-16.png
var iconPNG16 []byte

var (
	brandIcon48 = mustDecode(iconPNG48)
	brandIcon32 = mustDecode(iconPNG32)
)

func mustDecode(b []byte) ui.ImageSource {
	src, err := ui.DecodeImage(b)
	if err != nil {
		return nil
	}
	return src
}
