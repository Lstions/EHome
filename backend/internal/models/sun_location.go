package models

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// SunLocation 是一条日出日落规则求值所需的**已解析**地理配置。
type SunLocation struct {
	Latitude  float64
	Longitude float64
	Timezone  string         // IANA 名 (原样保留, 供缓存 key 与审计展示)
	Location  *time.Location // tz 解析结果
}

// 坐标与时区缺失/非法的错误 (handler 据此返回 400, 求值器据此不触发 + 审计)。
//
// 2026-11-13 用户裁决「不需要节点位置」：日出/日落规则**必须**自带坐标与时区，
// 不再有「留空 = 继承节点」这条路径。因此这里的错误措辞从
// "rule override and node both empty" 改为"规则未填写"——语义变了，文案必须跟着变，
// 否则用户会按旧文案去节点页找位置配置（那里已经没有入口）。
var (
	ErrSunLatitudeMissing  = errors.New("sun: 规则未填写 latitude（节点位置已废除，规则坐标必填）")
	ErrSunLongitudeMissing = errors.New("sun: 规则未填写 longitude（节点位置已废除，规则坐标必填）")
	ErrSunTimezoneMissing  = errors.New("sun: 规则未填写 timezone（节点位置已废除，规则时区必填）")
	ErrSunLatitudeRange    = errors.New("sun: latitude 越界 [-90, 90]")
	ErrSunLongitudeRange   = errors.New("sun: longitude 越界 [-180, 180]")
	ErrSunTimezoneInvalid  = errors.New("sun: timezone 不是合法的 IANA 名")
)

// ValidateSunCoordinates 校验坐标取值范围。
func ValidateSunCoordinates(lat, lon float64) error {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return fmt.Errorf("%w: %v", ErrSunLatitudeRange, lat)
	}
	if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return fmt.Errorf("%w: %v", ErrSunLongitudeRange, lon)
	}
	return nil
}

// ValidateTimezone 校验 IANA 时区名。
//
// 为什么必须用 time.LoadLocation 而不是查表: Go 标准库自带 time/tzdata,
// 时区数据可编译进二进制, 不依赖宿主 /usr/share/zoneinfo。LoadLocation 失败
// 即该名字在本进程内不可解析 —— 求值必然失败, 故保存时就该拒绝。
//
// **不得回落到 UTC**: 回落会让窗口静默偏移 8 小时 (选型文档 §6.3), 是安全相关错误。
func ValidateTimezone(name string) (*time.Location, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, ErrSunTimezoneMissing
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrSunTimezoneInvalid, name)
	}
	return loc, nil
}

// ResolveSunLocation 把规则行上的三项配置解析成日出日落求值所需的 SunLocation。
//
// 这是该解析的**唯一实现**（handler 保存校验与 evaluator 运行期求值共用），
// 避免保存期与运行期出现两套判据。
//
// 解析规则（2026-11-13 裁决，取代原 O-3 的"规则 → 节点 → 失败"）：
//
//	规则值 → 缺失即失败（fail-closed）。节点不再是坐标来源。
//
// 为什么废除节点级坐标：节点表的三列从来没有写入入口（无 API / 无 UI），
// 而 O-7 又要求"解析不出坐标就拒绝保存" ⇒ 留空路径实际必然 400，只保留
// 规则级坐标才是可用的语义。节点列暂时保留（不读不写），留给后续物理清理。
//
// 返回的 SunLocation 保证: 坐标在合法范围内且 Location 已成功解析 —— 任何
// 不满足的情况都返回错误, 绝不返回"看起来可用但实际有零值"的结果。
func ResolveSunLocation(ruleLat, ruleLon *float64, ruleTZ *string) (SunLocation, error) {
	var out SunLocation

	if ruleLat == nil {
		return SunLocation{}, ErrSunLatitudeMissing
	}
	if ruleLon == nil {
		return SunLocation{}, ErrSunLongitudeMissing
	}
	out.Latitude = *ruleLat
	out.Longitude = *ruleLon

	if ruleTZ == nil || strings.TrimSpace(*ruleTZ) == "" {
		// 空串与 nil 同等对待：空串不是合法 IANA 名，把它当成"已配置"只会
		// 产生一条永不触发的规则（C5 型静默失败）。
		return SunLocation{}, ErrSunTimezoneMissing
	}
	tzName := strings.TrimSpace(*ruleTZ)

	if err := ValidateSunCoordinates(out.Latitude, out.Longitude); err != nil {
		return SunLocation{}, err
	}
	loc, err := ValidateTimezone(tzName)
	if err != nil {
		return SunLocation{}, err
	}
	out.Timezone = tzName
	out.Location = loc
	return out, nil
}

// ResolveRuleSunLocation 是 ResolveSunLocation 针对规则行的便捷包装。
func ResolveRuleSunLocation(rule AutomationRule) (SunLocation, error) {
	return ResolveSunLocation(rule.Latitude, rule.Longitude, rule.Timezone)
}
