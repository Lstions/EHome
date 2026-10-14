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

// SunLocationSource 记录三项配置各自的取值来源 (审计/前端提示用)。
//
// 为什么需要它: 用户看到"日落 18:32"时无法判断这个时刻用的是规则级覆盖还是
// 节点级权威值。迁移节点后坐标变了, 规则时刻随之变化, 没有来源标注就无法解释。
type SunLocationSource struct {
	Latitude  string // "rule" | "node"
	Longitude string
	Timezone  string
}

// 坐标与时区缺失/非法的错误 (handler 据此返回 400, 求值器据此不触发 + 审计)。
var (
	ErrSunLatitudeMissing  = errors.New("sun: latitude is not configured (rule override and node both empty)")
	ErrSunLongitudeMissing = errors.New("sun: longitude is not configured (rule override and node both empty)")
	ErrSunTimezoneMissing  = errors.New("sun: timezone is not configured (rule override and node both empty)")
	ErrSunLatitudeRange    = errors.New("sun: latitude out of range [-90, 90]")
	ErrSunLongitudeRange   = errors.New("sun: longitude out of range [-180, 180]")
	ErrSunTimezoneInvalid  = errors.New("sun: timezone is not a valid IANA name")
)

// ValidateSunCoordinates 校验坐标取值范围 (V3)。
//
// 单独导出以便保存路径在**没有节点上下文**时也能先做范围校验: 无坐标节点上
// 建 sun_event 规则要报 400, 而不是静默落库后永不触发 (O-7 fail-closed)。
func ValidateSunCoordinates(lat, lon float64) error {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return fmt.Errorf("%w: %v", ErrSunLatitudeRange, lat)
	}
	if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return fmt.Errorf("%w: %v", ErrSunLongitudeRange, lon)
	}
	return nil
}

// ValidateTimezone 校验 IANA 时区名 (V4)。
//
// 为什么必须用 time.LoadLocation 而不是查表: Go 标准库自带 time/tzdata,
// 时区数据可编译进二进制, 不依赖宿主 /usr/share/zoneinfo。LoadLocation 失败
// 即该名字在本进程内不可解析 —— 求值必然失败, 故保存时就该拒绝 (O-7)。
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

// ResolveSunLocation 把"规则级覆盖 + 节点级权威"解析成日出日落求值所需的
// 三项配置。这是该解析的**唯一实现** (handler 校验与 evaluator 求值共用),
// 避免保存校验与运行期求值出现两套取值顺序而分叉。
//
// 解析顺序 (用户裁决 O-3): 规则值 → 节点值 → 缺失即失败 (fail-closed, O-7)。
// node == nil 表示该规则没有节点上下文 (例如 action 指向的节点已被删除),
// 此时只有规则级值可用。
//
// 返回的 SunLocation 保证: 坐标在合法范围内且 Location 已成功解析 —— 任何
// 不满足的情况都返回错误, 绝不返回"看起来可用但实际有零值"的结果。
func ResolveSunLocation(ruleLat, ruleLon *float64, ruleTZ *string, node *Node) (SunLocation, SunLocationSource, error) {
	var out SunLocation
	var src SunLocationSource

	// ── 纬度 ──
	switch {
	case ruleLat != nil:
		out.Latitude = *ruleLat
		src.Latitude = "rule"
	case node != nil && node.Latitude != nil:
		out.Latitude = *node.Latitude
		src.Latitude = "node"
	default:
		return SunLocation{}, src, ErrSunLatitudeMissing
	}
	// ── 经度 ──
	switch {
	case ruleLon != nil:
		out.Longitude = *ruleLon
		src.Longitude = "rule"
	case node != nil && node.Longitude != nil:
		out.Longitude = *node.Longitude
		src.Longitude = "node"
	default:
		return SunLocation{}, src, ErrSunLongitudeMissing
	}
	// ── 时区 ──
	var tzName string
	switch {
	case ruleTZ != nil && strings.TrimSpace(*ruleTZ) != "":
		// 刻意把"空串"与"nil"同等对待: 空串不是合法 IANA 名, 但把它当成
		// "显式配置了空时区"会让用户得到一条永不触发的规则 (C5 型静默失败)。
		// 按未配置处理 ⇒ 继承节点; 节点也没有 ⇒ 报错拒绝保存。
		tzName = strings.TrimSpace(*ruleTZ)
		src.Timezone = "rule"
	case node != nil && node.Timezone != nil && strings.TrimSpace(*node.Timezone) != "":
		tzName = strings.TrimSpace(*node.Timezone)
		src.Timezone = "node"
	default:
		return SunLocation{}, src, ErrSunTimezoneMissing
	}

	if err := ValidateSunCoordinates(out.Latitude, out.Longitude); err != nil {
		return SunLocation{}, src, err
	}
	loc, err := ValidateTimezone(tzName)
	if err != nil {
		return SunLocation{}, src, err
	}
	out.Timezone = tzName
	out.Location = loc
	return out, src, nil
}

// ResolveRuleSunLocation 是 ResolveSunLocation 针对规则行的便捷包装。
func ResolveRuleSunLocation(rule AutomationRule, node *Node) (SunLocation, SunLocationSource, error) {
	return ResolveSunLocation(rule.Latitude, rule.Longitude, rule.Timezone, node)
}
