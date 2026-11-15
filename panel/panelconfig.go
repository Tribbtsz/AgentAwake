package main

// panelconfig.go — 面板配置持久化
//
// 存 %LOCALAPPDATA%\AgentAwake\panel.json

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type panelConfig struct {
	OffsetX     int    `json:"offsetX"`   // 任务胶囊水平偏移（逻辑像素）
	ShowBar     bool   `json:"showBar"`   // 是否显示任务胶囊
	MaxHours    int    `json:"maxHours"`  // agent 租约的兜底上限（小时）
	Pulse       bool   `json:"pulse"`     // 保持中时轻微呼吸闪烁
	AutoStart   bool   `json:"autoStart"` // 开机自启（保留）
	PanelX      int    `json:"panelX"`    // 面板位置（用户拖动后记住）
	PanelY      int    `json:"panelY"`
	AgentPrompt string `json:"agentPrompt"` // 自定义的 agent 提示词；空 = 内置默认
}

func panelConfigPath() string {
	return filepath.Join(dataDir(), "panel.json")
}

func defaultConfig() panelConfig {
	return panelConfig{ShowBar: true, MaxHours: 12}
}

func loadPanelConfig() panelConfig {
	c := defaultConfig()
	b, err := os.ReadFile(panelConfigPath())
	if err == nil {
		json.Unmarshal(b, &c)
	}
	if c.MaxHours <= 0 {
		c.MaxHours = 12
	}
	return c
}

func savePanelConfig(c panelConfig) {
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := panelConfigPath() + ".tmp"
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	os.Rename(tmp, panelConfigPath())
}
