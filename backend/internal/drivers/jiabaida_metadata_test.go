package drivers

import "testing"

// TestJiabaidaAutomationMetadata 钉住 2026-11-14 的动作元数据：
//   - set_mos_policy 提供 4 个一键预设（充电/放电 × 开/关），参数固定 priority=user；
//   - 风险动作带可读 risk_reason，前端不再只能显示光秃秃的"风险"。
func TestJiabaidaAutomationMetadata(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	byID := map[string]ControlAction{}
	for _, action := range d.ControlActions() {
		byID[action.ID] = action
	}

	mos, ok := byID["set_mos_policy"]
	if !ok {
		t.Fatal("set_mos_policy missing")
	}
	if mos.RiskReason == "" {
		t.Error("set_mos_policy must carry risk_reason")
	}
	if len(mos.Presets) != 4 {
		t.Fatalf("set_mos_policy presets = %d, want 4", len(mos.Presets))
	}
	seen := map[string]bool{}
	for _, preset := range mos.Presets {
		id, _ := preset["id"].(string)
		label, _ := preset["label"].(string)
		params, _ := preset["params"].(map[string]any)
		if id == "" || label == "" || params == nil {
			t.Fatalf("preset missing id/label/params: %+v", preset)
		}
		seen[id] = true
		if params["priority"] != "user" {
			t.Errorf("preset %s priority = %v, want user", id, params["priority"])
		}
		if _, ok := params["charge_software_closed"].(bool); !ok {
			t.Errorf("preset %s missing charge_software_closed bool", id)
		}
		if _, ok := params["discharge_software_closed"].(bool); !ok {
			t.Errorf("preset %s missing discharge_software_closed bool", id)
		}
	}
	for _, id := range []string{"charge_off", "charge_on", "discharge_off", "discharge_on"} {
		if !seen[id] {
			t.Errorf("missing preset %s", id)
		}
	}

	for _, id := range []string{"test_charge_mos", "test_discharge_mos", "bms_restart", "clear_alarm", "read_sn"} {
		if byID[id].RiskReason == "" {
			t.Errorf("%s must carry risk_reason", id)
		}
	}
}
