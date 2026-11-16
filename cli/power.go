package main

// power.go — 电源设置快照 / 保持 / 恢复
//
// 读：直接读注册表（零子进程，微秒级），和面板读的是同一处。
// 写：只调用系统自带 powercfg.exe（隐藏窗口），语义和「设置 → 电源」完全一致。
// 熄屏超时全程不动：屏幕该黑就黑。

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
)

const powerSchemesKey = `SYSTEM\CurrentControlSet\Control\Power\User\PowerSchemes`

const (
	subSleep   = `238c9fa8-0aad-41ed-83f4-97be242c8f20`
	setSleep   = `29f6c1db-86da-48c5-9fdb-f2b67b1f44da`
	setHibern  = `9d7815a6-7ee4-497e-8888-515a05f02364`
	subVideo   = `7516b95f-f776-4464-8c53-06167f40cc99`
	setVideoNo = `3c0bc021-c8a8-4e07-a973-6b14cbcb2b7e`
)

type powerTarget struct {
	Key     string
	Name    string
	Sub     string
	Setting string
}

// keepTargets 保持期间要改成「从不」的项（不含熄屏）
var keepTargets = []powerTarget{
	{Key: "sleep", Name: "睡眠超时", Sub: subSleep, Setting: setSleep},
	{Key: "hibernate", Name: "休眠超时", Sub: subSleep, Setting: setHibern},
}

var displayTarget = powerTarget{Key: "display", Name: "熄屏超时", Sub: subVideo, Setting: setVideoNo}

// powerValues 一项设置的交流/电池取值（-1 表示读不到）
type powerValues struct {
	AC int
	DC int
}

// powerSnapshot 进入保持前记下的原值，退出时按它恢复
type powerSnapshot struct {
	Scheme     string                 `json:"scheme"`
	CapturedAt string                 `json:"capturedAt"`
	Settings   map[string]powerValues `json:"settings"`
}

func activeScheme() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, powerSchemesKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ActivePowerScheme")
	if err != nil {
		return ""
	}
	return v
}

// readPowerValue 读某一项设置（注册表里 ACSettingIndex / DCSettingIndex）。
// 读不到的项记 -1：调用方据此「不碰这一项」，避免改了恢复不了。
func readPowerValue(scheme string, t powerTarget) powerValues {
	path := powerSchemesKey + `\` + scheme + `\` + t.Sub + `\` + t.Setting
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return powerValues{AC: -1, DC: -1}
	}
	defer k.Close()
	v := powerValues{AC: -1, DC: -1}
	// 值不存在（未显式设置过）不等于 0（从不）：不能记成 0 再写回去，
	// 否则会把「跟随方案默认」永久改成「从不」。
	if ac, _, err := k.GetIntegerValue("ACSettingIndex"); err == nil {
		v.AC = int(ac)
	}
	if dc, _, err := k.GetIntegerValue("DCSettingIndex"); err == nil {
		v.DC = int(dc)
	}
	return v
}

// captureSnapshot 记录当前睡眠/休眠原值
func captureSnapshot() *powerSnapshot {
	scheme := activeScheme()
	snap := &powerSnapshot{
		Scheme:     scheme,
		CapturedAt: stamp(time.Now()),
		Settings:   map[string]powerValues{},
	}
	for _, t := range keepTargets {
		snap.Settings[t.Key] = readPowerValue(scheme, t)
	}
	return snap
}

func powercfgPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "powercfg.exe")
}

// setPowerValueFor 把某一项设置写到指定电源方案（空串 = 当前方案）。
// 恢复时必须指定当初记录的方案 GUID：用户中途换方案后，SCHEME_CURRENT
// 指向的已经不是修改过的那个方案了。
func setPowerValueFor(scheme string, t powerTarget, ac bool, value int) {
	_, _ = runHidden(powercfgPath(), powerValueArgs(scheme, t, ac, value)...)
}

// powerValueArgs 组装 powercfg 参数，单独出来便于测试
func powerValueArgs(scheme string, t powerTarget, ac bool, value int) []string {
	flag := "/setacvalueindex"
	if !ac {
		flag = "/setdcvalueindex"
	}
	if scheme == "" {
		scheme = "SCHEME_CURRENT"
	}
	return []string{flag, scheme, t.Sub, t.Setting, fmt.Sprint(value)}
}

func setPowerValue(t powerTarget, ac bool, value int) {
	setPowerValueFor("", t, ac, value)
}

// applyKeep 把睡眠/休眠改成「从不」，并让当前方案生效。
// 只改快照里读到的项：读不到原值（-1）的项改了也恢复不了，宁可不动。
func applyKeep(snap *powerSnapshot) {
	if snap == nil {
		return
	}
	changed := false
	for _, t := range keepTargets {
		saved, ok := snap.Settings[t.Key]
		if !ok {
			continue
		}
		if saved.AC >= 0 {
			setPowerValue(t, true, 0)
			changed = true
		}
		if saved.DC >= 0 {
			setPowerValue(t, false, 0)
			changed = true
		}
	}
	if changed {
		_, _ = runHidden(powercfgPath(), "/setactive", "SCHEME_CURRENT")
	}
}

// restoreSnapshot 按快照恢复原值（写回快照记录的那个方案）
func restoreSnapshot(snap *powerSnapshot) bool {
	if snap == nil || len(snap.Settings) == 0 {
		return false
	}
	for _, t := range keepTargets {
		saved, ok := snap.Settings[t.Key]
		if !ok {
			continue
		}
		if saved.AC >= 0 {
			setPowerValueFor(snap.Scheme, t, true, saved.AC)
		}
		if saved.DC >= 0 {
			setPowerValueFor(snap.Scheme, t, false, saved.DC)
		}
	}
	_, _ = runHidden(powercfgPath(), "/setactive", "SCHEME_CURRENT")
	return true
}

// powerStateNow 当前实际值（状态展示用）
func powerStateNow() map[string]powerValues {
	scheme := activeScheme()
	out := map[string]powerValues{}
	for _, t := range keepTargets {
		out[t.Key] = readPowerValue(scheme, t)
	}
	out["display"] = readPowerValue(scheme, displayTarget)
	return out
}

func formatPowerValue(v int) string {
	switch {
	case v < 0:
		return "-"
	case v == 0:
		return "从不"
	case v >= 60 && v%60 == 0:
		return fmt.Sprintf("%d 分", v/60)
	default:
		return fmt.Sprintf("%d 秒", v)
	}
}
