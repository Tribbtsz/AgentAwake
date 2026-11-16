package main

// power_safety_test.go — 用真实注册表验证「改得动就恢复得了」

import (
	"os"
	"testing"
)

// 快照里全 -1（读不到原值）时 applyKeep 必须什么都不改
func TestApplyKeepSkipsUnreadable(t *testing.T) {
	before := powerStateNow()
	snap := &powerSnapshot{Scheme: activeScheme(), Settings: map[string]powerValues{}}
	for _, tg := range keepTargets {
		snap.Settings[tg.Key] = powerValues{AC: -1, DC: -1}
	}
	applyKeep(snap)
	after := powerStateNow()
	if !samePower(before, after) {
		t.Errorf("读不到原值时不应改电源：before=%+v after=%+v", before, after)
	}
}

// 部分可读：只改可读项，恢复也只写回可读项
func TestApplyKeepRestoreRoundTrip(t *testing.T) {
	if os.Getenv("AGENTAWAKE_POWER_TEST") != "1" {
		t.Skip("会短暂改动本机电源设置；设 AGENTAWAKE_POWER_TEST=1 才跑")
	}
	before := powerStateNow()
	snap := captureSnapshot()

	applyKeep(snap)
	kept := powerStateNow()
	if kept["sleep"].AC != 0 || kept["hibernate"].AC != 0 {
		t.Fatalf("保持后睡眠/休眠应为 0（从不），得到 %+v", kept)
	}

	restoreSnapshot(snap)
	after := powerStateNow()
	if !samePower(before, after) {
		t.Fatalf("恢复后应与保持前一致：before=%+v after=%+v", before, after)
	}
}

// samePower 两个电源状态是否一致（powerValues 可比较，map 不行）
func samePower(a, b map[string]powerValues) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
