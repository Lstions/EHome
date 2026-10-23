package datasource

import (
	"testing"

	"ehome/backend/internal/drivers"
)

// deriveFieldMeta 的纯函数测试（零 DB）。
//
// 这三层降级是本功能的全部逻辑，必须逐层验证：
//   驱动声明 > 字段名模式 > 不派生（留空）

func TestDeriveFieldMeta_FromDriverDeclaration(t *testing.T) {
	defs := []drivers.SensorData{
		{Name: "rsoc", Unit: "%", LabelZh: "剩余电量百分比", ValueType: "number"},
	}
	got := deriveFieldMeta(defs, []string{"rsoc"})
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].LabelZh != "剩余电量百分比" {
		t.Errorf("LabelZh = %q，驱动声明未被采用", got[0].LabelZh)
	}
	if got[0].ValueType != "number" {
		t.Errorf("ValueType = %q", got[0].ValueType)
	}
}

func TestDeriveFieldMeta_DynamicCellVoltage(t *testing.T) {
	// cell_voltage_N 是解析器按位序生成的，不在静态声明里
	got := deriveFieldMeta(nil, []string{"cell_voltage_7", "cell_voltage_16"})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].LabelZh != "第 7 节电芯电压" {
		t.Errorf("LabelZh = %q", got[0].LabelZh)
	}
	if got[1].LabelZh != "第 16 节电芯电压" {
		t.Errorf("LabelZh = %q", got[1].LabelZh)
	}
	if got[0].Unit != "V" || got[0].ValueType != "number" {
		t.Errorf("unit/type = %q/%q", got[0].Unit, got[0].ValueType)
	}
}

func TestDeriveFieldMeta_DynamicAlarm(t *testing.T) {
	got := deriveFieldMeta(nil, []string{"alarm_battery_low"})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ValueType != "boolean" {
		t.Errorf("告警位应为 boolean，got %q", got[0].ValueType)
	}
	if got[0].LabelZh == "" {
		t.Error("告警位应有描述")
	}
}

func TestDeriveFieldMeta_DynamicPV(t *testing.T) {
	got := deriveFieldMeta(nil, []string{"pv1_voltage", "pv2_current", "pv1_power"})
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].LabelZh != "光伏输入 1 电压" {
		t.Errorf("pv1_voltage label = %q", got[0].LabelZh)
	}
	if got[1].Unit != "A" {
		t.Errorf("pv2_current unit = %q", got[1].Unit)
	}
	if got[2].Unit != "W" {
		t.Errorf("pv1_power unit = %q", got[2].Unit)
	}
}

// TestDeriveFieldMeta_UnknownStaysEmpty 验证**不编造** —— 未知字段不派生。
//
// 错误的中文描述比没有描述更危险：用户会按描述理解数据，错描述导致误判。
func TestDeriveFieldMeta_UnknownStaysEmpty(t *testing.T) {
	got := deriveFieldMeta(nil, []string{"some_unknown_metric_xyz"})
	if len(got) != 0 {
		t.Fatalf("未知字段不应派生，got %+v", got)
	}
}

// TestDeriveFieldMeta_DriverBeatsPattern 验证驱动声明优先于模式匹配。
func TestDeriveFieldMeta_DriverBeatsPattern(t *testing.T) {
	defs := []drivers.SensorData{
		// 假设某驱动对 cell_voltage_1 给了更准确的名字
		{Name: "cell_voltage_1", Unit: "mV", LabelZh: "第一节电压（毫伏）", ValueType: "number"},
	}
	got := deriveFieldMeta(defs, []string{"cell_voltage_1"})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].LabelZh != "第一节电压（毫伏）" {
		t.Errorf("驱动声明应优先于模式匹配，got %q", got[0].LabelZh)
	}
	if got[0].Unit != "mV" {
		t.Errorf("unit 应取驱动声明，got %q", got[0].Unit)
	}
}
