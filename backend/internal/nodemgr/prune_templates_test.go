package nodemgr

import (
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// §206.3 回归：enabled=false 的边设备不得让模板进入 manifest。
//
// 真机证据（2026-10-09）：构造 1 个 enabled=false 设备 + 1 个挂到通道的模板，
// enabled=true 的边设备数 = 0，而后端仍打
//
//	"ConfigManifest sent: device=30EDA0A9A808 ... 1 templates, 3 channels"
//
// 设备侧解析到的 edge_device 数 = 0
// ⇒ 设备收到 0 个从机却带 1 个模板 = 无引用的死数据，白占 MAX_TEMPLATES=16。
//
// 这个测试锁住 pruneUnreferencedTemplates 的行为。
func TestPruneUnreferencedTemplates(t *testing.T) {
	ch := models.Channel{ID: 81, NodeID: "n1", HardwareID: "UART1", TemplateIDs: "70"}
	tmpl := models.ConfigTemplate{ID: 70, NodeID: "n1", WriteData: "010300000002C40B", ReadLength: 9}

	t.Run("无启用设备时模板全剪", func(t *testing.T) {
		// loadManifestSnapshot 只会加载 enabled=true 的设备 ⇒ edgeDevices 为空
		snap := &manifestSnapshot{
			templates:      []models.ConfigTemplate{tmpl},
			channels:       []models.Channel{ch},
			edgeDevices:    nil,
			driverCommands: map[string][]drivers.CommandTemplate{},
		}
		pruneUnreferencedTemplates(snap)
		if len(snap.templates) != 0 {
			t.Fatalf("no enabled edge device => want 0 templates, got %d", len(snap.templates))
		}
	})

	t.Run("启用设备经 channel.template_ids 引用时保留", func(t *testing.T) {
		// legacy 单命令路径：设备靠通道的 template_ids 拿模板。
		// 该设备的驱动**不**提供命令模板（driverCommands 为空），
		// 因此唯一的引用来源就是 channel.template_ids。
		snap := &manifestSnapshot{
			templates:      []models.ConfigTemplate{tmpl},
			channels:       []models.Channel{ch},
			edgeDevices:    []models.EdgeDevice{{ID: 146, ChannelID: 81, Type: "legacy_type", Enabled: true}},
			driverCommands: map[string][]drivers.CommandTemplate{"legacy_type": nil},
		}
		pruneUnreferencedTemplates(snap)
		if len(snap.templates) != 1 || snap.templates[0].ID != 70 {
			t.Fatalf("template referenced via channel.template_ids must be kept, got %+v", snap.templates)
		}
	})

	t.Run("启用设备经驱动命令 write_data 引用时保留", func(t *testing.T) {
		// v2 多命令路径：设备靠驱动命令的 write_data 匹配模板。
		// 通道 template_ids 为空，唯一引用来源是驱动命令。
		chNoTmpl := models.Channel{ID: 81, NodeID: "n1", HardwareID: "UART1", TemplateIDs: ""}
		snap := &manifestSnapshot{
			templates:   []models.ConfigTemplate{tmpl},
			channels:    []models.Channel{chNoTmpl},
			edgeDevices: []models.EdgeDevice{{ID: 146, ChannelID: 81, Type: "prs3001", Enabled: true}},
			driverCommands: map[string][]drivers.CommandTemplate{
				"prs3001": {{ID: "read", WriteData: "010300000002C40B", ReadLength: 9, Schedulable: true, IntervalMs: 100}},
			},
		}
		pruneUnreferencedTemplates(snap)
		if len(snap.templates) != 1 || snap.templates[0].ID != 70 {
			t.Fatalf("template referenced via driver write_data must be kept, got %+v", snap.templates)
		}
	})

	t.Run("驱动命令 interval=0 不算引用（与编码器同口径）", func(t *testing.T) {
		chNoTmpl := models.Channel{ID: 81, NodeID: "n1", HardwareID: "UART1", TemplateIDs: ""}
		snap := &manifestSnapshot{
			templates:   []models.ConfigTemplate{tmpl},
			channels:    []models.Channel{chNoTmpl},
			edgeDevices: []models.EdgeDevice{{ID: 146, ChannelID: 81, Type: "prs3001", Enabled: true}},
			driverCommands: map[string][]drivers.CommandTemplate{
				// interval=0 ⇒ 编码器不会编它 ⇒ 模板不该保留
				"prs3001": {{ID: "read", WriteData: "010300000002C40B", ReadLength: 9, Schedulable: true, IntervalMs: 0}},
			},
		}
		pruneUnreferencedTemplates(snap)
		if len(snap.templates) != 0 {
			t.Fatalf("interval=0 command must not keep a template, got %d", len(snap.templates))
		}
	})

	t.Run("无引用的多余模板被剪，被引用的保留", func(t *testing.T) {
		// 模拟 §206.3 的现场：1 个禁用设备留下的模板（无引用）+ 1 个在用的模板。
		orphan := models.ConfigTemplate{ID: 71, NodeID: "n1", WriteData: "DEADBEEF", ReadLength: 4}
		chNoTmpl := models.Channel{ID: 81, NodeID: "n1", HardwareID: "UART1", TemplateIDs: ""}
		snap := &manifestSnapshot{
			templates:   []models.ConfigTemplate{tmpl, orphan},
			channels:    []models.Channel{chNoTmpl},
			edgeDevices: []models.EdgeDevice{{ID: 146, ChannelID: 81, Type: "prs3001", Enabled: true}},
			driverCommands: map[string][]drivers.CommandTemplate{
				"prs3001": {{ID: "read", WriteData: "010300000002C40B", ReadLength: 9, Schedulable: true, IntervalMs: 100}},
			},
		}
		pruneUnreferencedTemplates(snap)
		if len(snap.templates) != 1 || snap.templates[0].ID != 70 {
			t.Fatalf("want only the referenced template 70 kept, got %+v", snap.templates)
		}
	})
}
