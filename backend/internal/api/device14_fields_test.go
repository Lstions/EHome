package api

// device14_fields_test.go — device14 的真实字段集，被多个用例共用。
//
// 从 history_source_perf_test.go 抽出来（2026-11-08）：那个文件整体删掉了
// （它的比较基准是 unified_data，随 DROP 一起退场），但字段集本身是**生产事实**，
// 别的用例还要用。
//
// 来源：生产 unified_data 实测（device14 = jiabaida BMS，29 个物理量）。
// ⚠ 这 29 个字段与 telemetry_slot 里的 slot 定义必须一致，否则编解码对不上。

// bms29 是 device14 的真实字段集（29 个）。
var bms29 = []string{
	"rsoc", "current", "total_voltage", "remaining_capacity", "nominal_capacity",
	"cycle_count", "fet_status", "protection_status", "cell_voltage_1", "cell_voltage_2",
	"cell_voltage_3", "cell_voltage_4", "cell_voltage_5", "cell_voltage_6", "cell_voltage_7",
	"cell_voltage_8", "cell_voltage_9", "cell_voltage_10", "cell_voltage_11", "cell_voltage_12",
	"cell_voltage_13", "cell_voltage_14", "cell_voltage_15", "cell_voltage_16", "temperature_1",
	"temperature_2", "temperature_3", "temperature_4", "total_energy",
}
