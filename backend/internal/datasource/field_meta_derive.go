package datasource

// 字段展示元数据（sensor_field_meta）的自动派生。
//
// # 为什么需要它
//
// 生产实测（2026-10-23）：sensor_field_meta 是 **0 行** —— 它此前只有读写接口、
// 没有任何填充源。后果是前端"数据描述"列整列空白，用户以为功能没做。
//
// # 数据来源分三层，逐层降级
//
//  1. **驱动静态声明**（最准）：GetSensorDefinitions() 里的 LabelZh/ValueType。
//     驱动是唯一掌握"物理名 → 中文含义"的地方。
//  2. **字段名模式匹配**（覆盖动态字段）：cell_voltage_7 / temperature_2 / alarm_xxx
//     这类由解析器按位序生成的字段不在静态声明里，按前缀派生。
//  3. **不派生**：以上都不匹配时留空，前端回落显示物理名 —— 宁可显示英文原文，
//     也不要编一个可能错的中文名（错误描述比没有描述更危险）。
//
// # 为什么不覆盖人工登记
//
// 与 data_sources 投影同纪律：只写**不存在**的行，已存在的行一律不动。

import (
	"context"
	"fmt"
	"regexp"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// fieldMetaSuggestion 是一条待写入的展示元数据。
type fieldMetaSuggestion struct {
	SensorName string
	LabelZh    string
	Unit       string
	ValueType  string
}

// dynamicFieldPatterns 是动态字段名 → 中文描述的派生规则。
//
// 顺序有意义：更具体的模式必须在更宽的模式之前（cell_voltage_N 不能先被
// 其他 cell_ 规则吃掉）。当前规则集互不重叠，但顺序仍按"具体→宽泛"排列。
var dynamicFieldPatterns = []struct {
	re    *regexp.Regexp
	label func(m []string) string
	unit  string
	vType string
}{
	{
		re:    regexp.MustCompile(`^cell_voltage_(\d+)$`),
		label: func(m []string) string { return "第 " + m[1] + " 节电芯电压" },
		unit:  "V", vType: "number",
	},
	{
		re:    regexp.MustCompile(`^temperature_(\d+)$`),
		label: func(m []string) string { return "温度探头 " + m[1] },
		unit:  "°C", vType: "number",
	},
	{
		re:    regexp.MustCompile(`^temp_(\d+)$`),
		label: func(m []string) string { return "温度探头 " + m[1] },
		unit:  "°C", vType: "number",
	},
	{
		re:    regexp.MustCompile(`^alarm_(.+)$`),
		label: func(m []string) string { return "告警位：" + m[1] },
		vType: "boolean",
	},
	{
		re:    regexp.MustCompile(`^pv(\d+)_voltage$`),
		label: func(m []string) string { return "光伏输入 " + m[1] + " 电压" },
		unit:  "V", vType: "number",
	},
	{
		re:    regexp.MustCompile(`^pv(\d+)_current$`),
		label: func(m []string) string { return "光伏输入 " + m[1] + " 电流" },
		unit:  "A", vType: "number",
	},
	{
		re:    regexp.MustCompile(`^pv(\d+)_power$`),
		label: func(m []string) string { return "光伏输入 " + m[1] + " 功率" },
		unit:  "W", vType: "number",
	},
}

// deriveFieldMeta 从驱动声明与字段名模式派生展示元数据。
//
// 纯函数：无 DB、无时钟。可穷举单测。
func deriveFieldMeta(defs []drivers.SensorData, observedNames []string) []fieldMetaSuggestion {
	fromDriver := make(map[string]fieldMetaSuggestion, len(defs))
	for _, d := range defs {
		if d.LabelZh == "" && d.ValueType == "" {
			continue
		}
		fromDriver[d.Name] = fieldMetaSuggestion{
			SensorName: d.Name,
			LabelZh:    d.LabelZh,
			Unit:       d.Unit,
			ValueType:  d.ValueType,
		}
	}

	out := make([]fieldMetaSuggestion, 0, len(observedNames))
	for _, name := range observedNames {
		if s, ok := fromDriver[name]; ok {
			out = append(out, s)
			continue
		}
		for _, p := range dynamicFieldPatterns {
			m := p.re.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			out = append(out, fieldMetaSuggestion{
				SensorName: name,
				LabelZh:    p.label(m),
				Unit:       p.unit,
				ValueType:  p.vType,
			})
			break
		}
	}
	return out
}

// ReconcileFieldMeta 把派生结果写入 sensor_field_meta（只写缺失的行）。
func (s *Service) ReconcileFieldMeta(ctx context.Context, driverRegistry *drivers.Registry,
	observedNames []string) (created int, err error) {

	if s == nil || s.db == nil {
		return 0, fmt.Errorf("datasource: nil service or db")
	}
	if !s.db.Migrator().HasTable(&models.SensorFieldMeta{}) {
		return 0, nil
	}

	var defs []drivers.SensorData
	if driverRegistry != nil {
		for _, dt := range driverRegistry.List() {
			drv, gerr := driverRegistry.Get(dt)
			if gerr != nil {
				continue
			}
			defs = append(defs, drv.GetSensorDefinitions()...)
		}
	}

	suggestions := deriveFieldMeta(defs, observedNames)
	if len(suggestions) == 0 {
		return 0, nil
	}

	names := make([]string, 0, len(suggestions))
	for _, sug := range suggestions {
		names = append(names, sug.SensorName)
	}
	var existing []string
	if err := s.db.WithContext(ctx).Model(&models.SensorFieldMeta{}).
		Where("sensor_name IN ?", names).
		Pluck("sensor_name", &existing).Error; err != nil {
		return 0, fmt.Errorf("datasource: load existing field meta: %w", err)
	}
	exists := make(map[string]struct{}, len(existing))
	for _, n := range existing {
		exists[n] = struct{}{}
	}

	for _, sug := range suggestions {
		if _, ok := exists[sug.SensorName]; ok {
			continue
		}
		row := models.SensorFieldMeta{
			SensorName: sug.SensorName,
			LabelZh:    sug.LabelZh,
			Unit:       sug.Unit,
			ValueType:  sug.ValueType,
			Status:     "active",
		}
		if cerr := s.db.WithContext(ctx).Create(&row).Error; cerr != nil {
			logger.Warn("datasource: create field meta failed",
				"sensor_name", sug.SensorName, "error", cerr)
			continue
		}
		created++
	}
	return created, nil
}

// ObservedFieldNames 返回 sensor_field_obs 里出现过的全部字段名（去重）。
func (s *Service) ObservedFieldNames(ctx context.Context) ([]string, error) {
	var names []string
	if err := s.db.WithContext(ctx).Model(&models.SensorFieldObs{}).
		Distinct().Pluck("sensor_name", &names).Error; err != nil {
		return nil, err
	}
	return names, nil
}
