package sensorfield

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// unitClass 是单位“类”（只做同类校验，不做换算；方案 §6.1 规则 3）。
//
// 为什么只比类不比值：本层刻意“不做单位换算”（§1.4 非目标），但必须防住
// 评审 H1 的最坏场景 —— @current 只解析到充电电流而漏掉放电电流（过流保护失效）。
type unitClass string

const (
	unitClassUnknown     unitClass = ""
	unitClassPercent     unitClass = "percent"
	unitClassVoltage     unitClass = "voltage"
	unitClassCurrent     unitClass = "current"
	unitClassPower       unitClass = "power"
	unitClassEnergy      unitClass = "energy"
	unitClassCharge      unitClass = "charge"
	unitClassTemperature unitClass = "temperature"
	unitClassFrequency   unitClass = "frequency"
	unitClassResistance  unitClass = "resistance"
	unitClassCount       unitClass = "count"
	unitClassTime        unitClass = "time"
	unitClassSignal      unitClass = "signal"

	// 数据语义规范 §3.1 其余受控白名单类别（用于别名单位类校验）。
	unitClassIlluminance   unitClass = "illuminance"
	unitClassLength        unitClass = "length"
	unitClassPressure      unitClass = "pressure"
	unitClassConcentration unitClass = "concentration"
	unitClassSpeed         unitClass = "speed"
	unitClassAngle         unitClass = "angle"
	unitClassBitrate       unitClass = "bitrate"
)

// normalizeUnit 归一单位串（不去符号，只 trim + 小写）。
func normalizeUnit(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// classOfUnit 把单位符号映射到单位类。无法识别的单位返回 unitClassUnknown（不做猜测）。
func classOfUnit(u string) unitClass {
	switch normalizeUnit(u) {
	case "%", "%rh", "pct", "percent", "百分比":
		return unitClassPercent
	case "v", "mv", "kv", "volt", "voltage", "伏":
		return unitClassVoltage
	case "a", "ma", "ka", "amp", "ampere", "current", "安":
		return unitClassCurrent
	case "w", "kw", "mw", "va", "kva", "watt", "power", "功率":
		return unitClassPower
	case "wh", "kwh", "mwh", "energy", "电能":
		return unitClassEnergy
	case "ah", "mah", "coulomb", "charge", "电量":
		return unitClassCharge
	case "°c", "℃", "c", "celsius", "°f", "f", "fahrenheit", "temperature", "温度":
		return unitClassTemperature
	case "hz", "khz", "mhz", "frequency", "频率":
		return unitClassFrequency
	case "ω", "ohm", "kω", "mω", "resistance", "电阻":
		return unitClassResistance
	case "次", "串", "个", "pcs", "count", "cycles":
		return unitClassCount
	case "s", "sec", "secs", "second", "ms", "min", "h", "hour", "day", "天", "time", "时间":
		return unitClassTime
	case "db", "dbm", "signal":
		return unitClassSignal
	case "lx", "lux", "illuminance", "照度":
		return unitClassIlluminance
	case "mm", "m", "cm", "km", "length", "长度":
		return unitClassLength
	case "pa", "hpa", "kpa", "pressure", "气压":
		return unitClassPressure
	case "ppm", "ppb", "concentration", "浓度":
		return unitClassConcentration
	case "m/s", "km/h", "speed", "速度":
		return unitClassSpeed
	case "°", "deg", "angle", "角度":
		return unitClassAngle
	case "bit/s", "bps", "bitrate":
		return unitClassBitrate
	}
	return unitClassUnknown
}

// isKnownUnitClass 判断 c 是否是已定义的单位类。
func isKnownUnitClass(c unitClass) bool {
	switch c {
	case unitClassPercent, unitClassVoltage, unitClassCurrent, unitClassPower,
		unitClassEnergy, unitClassCharge, unitClassTemperature, unitClassFrequency,
		unitClassResistance, unitClassCount, unitClassTime, unitClassSignal,
		unitClassIlluminance, unitClassLength, unitClassPressure, unitClassConcentration,
		unitClassSpeed, unitClassAngle, unitClassBitrate:
		return true
	}
	return false
}

// expectedClass 把 AliasRule.ExpectedUnit 归一为单位类。
// 既接受单位符号（"%"、"V"、"°C"），也接受类名（"percent"、"voltage"）；
// 识别不了 ⇒ unitClassUnknown（此时跳过单位校验，不猜测）。
func expectedClass(expected string) unitClass {
	if c := classOfUnit(expected); c != unitClassUnknown {
		return c
	}
	if c := unitClass(normalizeUnit(expected)); isKnownUnitClass(c) {
		return c
	}
	return unitClassUnknown
}

// aliasRule 是校验通过的别名规则（不可变）。
type aliasRule struct {
	semantic      string
	candidates    []string // 有序：解析按此顺序取第一个“该设备已知”的
	expectedUnit  string
	expectedClass unitClass
	allowOverlap  bool
}

// aliasTable 是别名快照（不可变；配置整体替换，不追加）。
type aliasTable struct {
	bySemantic map[string]*aliasRule
	order      []string            // 语义名配置顺序（确定性遍历用）
	physical   map[string][]string // 物理名 -> 语义名（配置顺序）
}

// newEmptyAliasTable 返回空别名表（New 的初始状态）。
func newEmptyAliasTable() *aliasTable {
	return &aliasTable{bySemantic: map[string]*aliasRule{}, physical: map[string][]string{}}
}

// lookup 返回语义名对应的规则；未定义返回 nil。
func (t *aliasTable) lookup(semantic string) *aliasRule {
	if t == nil {
		return nil
	}
	return t.bySemantic[semantic]
}

// semanticsOf 返回某物理名归属的语义名（配置顺序）。
func (t *aliasTable) semanticsOf(physical string) []string {
	if t == nil {
		return nil
	}
	list := t.physical[physical]
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// buildAliasTable 校验并构建别名表（方案 §6.1）。
//
// 规则（v0.2 定稿）：
//  1. 配置按 key 整体替换，不追加；同一语义名重复出现 ⇒ 拒绝
//  2. 同一物理名出现在多个语义名下 ⇒ 拒绝，除非**所有**相关项都显式 allow_overlap: true
//  3. 候选单位类与 ExpectedUnit 不符 ⇒ 拒绝（declared 为“物理名 -> 已知单位类集合”）
//
// declared 为空时跳过单位类校验（冷启动顺序：先声明后别名；见 Registry.Validate）。
func buildAliasTable(rules []AliasRule, declared map[string]map[unitClass]struct{}) (*aliasTable, error) {
	t := newEmptyAliasTable()
	type owner struct {
		semantic string
		allow    bool
	}
	owners := map[string][]owner{}

	for i, rl := range rules {
		sem := strings.TrimSpace(rl.Semantic)
		if sem == "" {
			return nil, fmt.Errorf("%w: alias rule #%d has empty semantic name", ErrAliasConflict, i)
		}
		if !isPhysicalName(sem) {
			return nil, fmt.Errorf("%w: invalid semantic name %q", ErrInvalidSyntax, sem)
		}
		if _, dup := t.bySemantic[sem]; dup {
			return nil, fmt.Errorf("%w: duplicate semantic %q", ErrAliasConflict, sem)
		}
		if len(rl.Candidates) == 0 {
			return nil, fmt.Errorf("%w: semantic %q has no candidates", ErrAliasConflict, sem)
		}
		seen := make(map[string]struct{}, len(rl.Candidates))
		cands := make([]string, 0, len(rl.Candidates))
		for _, raw := range rl.Candidates {
			cand := strings.TrimSpace(raw)
			if !isPhysicalName(cand) {
				return nil, fmt.Errorf("%w: semantic %q has invalid candidate %q", ErrAliasConflict, sem, cand)
			}
			if _, dup := seen[cand]; dup {
				return nil, fmt.Errorf("%w: semantic %q has duplicate candidate %q", ErrAliasConflict, sem, cand)
			}
			seen[cand] = struct{}{}
			cands = append(cands, cand)
			owners[cand] = append(owners[cand], owner{semantic: sem, allow: rl.AllowOverlap})
		}
		t.bySemantic[sem] = &aliasRule{
			semantic:      sem,
			candidates:    cands,
			expectedUnit:  rl.ExpectedUnit,
			expectedClass: expectedClass(rl.ExpectedUnit),
			allowOverlap:  rl.AllowOverlap,
		}
		t.order = append(t.order, sem)
	}

	// 规则 2：同一物理名多语义（所有相关项都必须 allow_overlap 才放行）。
	for physical, os := range owners {
		if len(os) <= 1 {
			continue
		}
		for _, o := range os {
			if !o.allow {
				return nil, fmt.Errorf("%w: physical %q belongs to multiple semantics and semantic %q does not set allow_overlap", ErrAliasConflict, physical, o.semantic)
			}
		}
	}

	// 规则 3：单位类校验。
	if err := validateAliasUnits(t, declared); err != nil {
		return nil, err
	}

	// 反查表（配置顺序）。
	for _, sem := range t.order {
		for _, cand := range t.bySemantic[sem].candidates {
			t.physical[cand] = append(t.physical[cand], sem)
		}
	}
	return t, nil
}

// validateAliasUnits 校验每个候选的已知单位类与 ExpectedUnit 同类（方案 §6.1 规则 3）。
func validateAliasUnits(t *aliasTable, declared map[string]map[unitClass]struct{}) error {
	if t == nil || len(declared) == 0 {
		return nil
	}
	var errs []error
	for _, sem := range t.order {
		rule := t.bySemantic[sem]
		if rule.expectedClass == unitClassUnknown {
			continue
		}
		for _, cand := range rule.candidates {
			for got := range declared[cand] {
				if got != rule.expectedClass {
					errs = append(errs, fmt.Errorf("%w: semantic %q candidate %q has unit class %q, expected %q", ErrAliasConflict, sem, cand, got, rule.expectedClass))
				}
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// deadCandidates 返回“不出来自任何声明/观测名字集合”的候选（评审 H1 死候选检测）。
func (t *aliasTable) deadCandidates(knownNames map[string]struct{}) []string {
	if t == nil {
		return nil
	}
	var dead []string
	seen := map[string]struct{}{}
	for _, sem := range t.order {
		for _, cand := range t.bySemantic[sem].candidates {
			if _, ok := knownNames[cand]; ok {
				continue
			}
			if _, dup := seen[cand]; dup {
				continue
			}
			seen[cand] = struct{}{}
			dead = append(dead, cand)
		}
	}
	sort.Strings(dead)
	return dead
}
