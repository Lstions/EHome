// Package sun 提供日出/日落的纯函数天文计算 (NOAA/Meeus 简化式, 候选 A)。
//
// 选型依据: docs/分析/日出日落天文计算选型-2026-10-14.md。
// **零新增依赖** —— 只用标准库 math 与 time。
//
// 设计约束 (选型文档 §5.1 / §6.1):
//   - 无全局状态、无 I/O、不读 time.Now() (求值时刻由调用方注入), 全部行为可确定性复现;
//   - 无解 (极昼/极夜) 时返回**显式** EventStatus, **绝不返回零值时间冒充结果**。
//     零值时间 (1970-01-01) 一旦流入下游边沿判定, 会被当作"1970 已过"或"今天 00:00"
//     ⇒ 午夜误触发 —— 这是本选型最危险的失败模式, 不是理论推演: 外部权威源
//     sunrise-sunset.org 在极昼/极夜时正是返回 1970 哨兵值 (day_length=0),
//     实证见 docs/分析/日出日落黄金向量-2026-10-14.md §2.1。
//   - 非法输入 fail-closed: 返回错误, **不得**静默按 0 处理 (选型文档 §6.2)。
package sun

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ZenithDeg 是"日出/日落"这一约定对应的太阳天顶角 (度)。
//
// 这不是可调参数, 而是"什么叫日出"的定义 (选型文档 §2.4): 日出 = 太阳**上边缘**
// 与地平线相切并计入标准大气折射, 而不是太阳几何中心过地平线 (后者时刻晚约 4 分钟)。
//
// 推导: 90° (几何地平线) + 34′ (地平线处标准大气折射) + 16′ (太阳视半径)
//
//	= 90° + 50′ = 90.8333…° ⇒ 取 NOAA 自己使用的 90.833°。
//
// 一手来源 (2026-10-14 实测抓取 NOAA 官方主机, 非转述、非凭印象):
//   - https://gml.noaa.gov/grad/solcalc/calcdetails.html
//     (sha256 3da7e18c6d2a9e4b0070214cdda1ab3d6fa02c3e700f0b4964982e355ce891ef)
//     原文: "For sunrise and sunset calculations, we assume 0.833° of atmospheric refraction."
//   - https://gml.noaa.gov/grad/solcalc/main.js
//     (sha256 3832956f24724eafacf9e18173b9299b1554d6252bed0784d9c9aca6d9f54856)
//     第 226 行逐字: Math.cos(degToRad(90.833))/(Math.cos(latRad)*Math.cos(sdRad))-...
//
// 交叉验证: 以 NOAA 官方 main.js 函数实算的 6 条中低纬向量做 ±2 秒紧容差断言
// (sun_test.go 的 TestGoldenNOAAReferenceImplementation), 与外部服务向量的 ±2 分钟
// 断言 (TestGoldenVectorsExternal) 两级并行。任何对常数"凭印象"的改动都会让两级同时变红。
const ZenithDeg = 90.833

// Kind 是日出/日落二选一的事件种类 (与 models.AutomationSunKind* 同域)。
type Kind string

const (
	Sunrise Kind = "sunrise" // 日出
	Sunset  Kind = "sunset"  // 日落
)

// Valid 报告 kind 是否为已支持的取值 (fail-closed 入口用)。
func (k Kind) Valid() bool { return k == Sunrise || k == Sunset }

// EventStatus 是日出日落计算的**显式**结果状态 (选型文档 §6.1)。
//
// 为什么必须有它: 高纬度一年中存在"无日出"或"无日落"的日期, 此时余弦项落在
// [-1,1] 之外 —— 这是**数学上的正常信号**, 不是计算失败。用零值时间表达它会让
// 下游无法与"1970 年触发过"区分 (外部哨兵值实证见包注释)。
type EventStatus string

const (
	StatusOK         EventStatus = "ok"          // 当日本地日存在该事件, 返回的时刻有效
	StatusAlwaysUp   EventStatus = "always_up"   // 极昼: 太阳终日在地平线上, 本日无该事件
	StatusAlwaysDown EventStatus = "always_down" // 极夜: 太阳终日在地平线下, 本日无该事件
)

// 非法输入错误 (fail-closed; 调用方用 errors.Is 判定)。
var (
	ErrNilLocation    = errors.New("sun: timezone location is nil")
	ErrLatitudeRange  = errors.New("sun: latitude out of range [-90, 90]")
	ErrLongitudeRange = errors.New("sun: longitude out of range [-180, 180]")
	ErrUnknownKind    = errors.New("sun: unknown sun event kind")
)

const (
	// julianDayUnixEpoch 是 Unix 纪元 (1970-01-01T00:00:00Z) 的儒略日 (JD)。
	julianDayUnixEpoch = 2440587.5
	julianDayJ2000     = 2451545.0
	minutesPerDay      = 1440.0
	msPerDay           = 86400000.0
	degPerRad          = 180.0 / math.Pi
)

// SunEvent 计算给定坐标在**节点本地日期** date 当日的日出或日落时刻。
//
// 参数:
//   - lat/lon: WGS84 十进制度。lat ∈ [-90,90], lon ∈ [-180,180] (东经/北纬为正)。
//   - tz: 节点 IANA 时区 (time.LoadLocation 得到)。**必须非 nil**。
//   - date: 任意时刻, 只取其**在 tz 下的日历日期**作为"当日"。
//   - kind: sunrise | sunset。
//
// 返回:
//   - status == StatusOK 时, 返回的 time.Time 是该事件在 tz 下的时刻 (非零值);
//   - status == StatusAlwaysUp/AlwaysDown 时, 返回**零值时间**与 nil error ——
//     调用方必须先看状态, 绝不能把零值时间当结果用;
//   - 非法输入返回零值时间 + 非 nil error (fail-closed)。
//
// 纯函数: 不读时钟、不碰全局状态、无 I/O。预计算/缓存在 Cache (cache.go)。
func SunEvent(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
	if tz == nil {
		return time.Time{}, "", ErrNilLocation
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrLatitudeRange, lat)
	}
	if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrLongitudeRange, lon)
	}
	if !kind.Valid() {
		return time.Time{}, "", fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}

	// "当日" = date 在节点时区下的日历日期。
	y, m, d := date.In(tz).Date()

	// NOAA 的 getJD 给的是当日 0h UT 的儒略日 (整日落在 .5)。事件时刻的绝对
	// 瞬时 = jd0 + 计算出的 UT 分钟数/1440, 与后续"归日"无关 (归日只改显示日期)。
	jd0 := julianDay(y, m, d)
	inst := time.Time{}
	for i := 0; i < 3; i++ {
		utcMinutes, status := sunEventUTCMinutes(lat, lon, jd0, kind)
		if status != StatusOK {
			// 极昼/极夜: 显式状态 + 零值时间, 不编造时刻, 更不 clamp。
			return time.Time{}, status, nil
		}
		inst = julianToTime(jd0 + utcMinutes/minutesPerDay)
		// 归日: 要求返回的事件确实发生在 date 所代表的**节点本地日历日**内。
		// 时区与经度不匹配的配置 (例如经度 179° 却配 UTC) 会让裸公式落到相邻本地日,
		// 这里按天差平移 jd0 后重算 (至多 3 次; NOAA 官方实现对显示值做同款归一化)。
		ey, em, ed := inst.In(tz).Date()
		shift := dayDelta(y, m, d, ey, em, ed)
		if shift == 0 {
			break
		}
		jd0 += float64(shift)
	}
	return inst.In(tz), StatusOK, nil
}

// sunEventUTCMinutes 是 NOAA/Meeus 简化式的核心: 返回事件相对 jd0 当日 0h UT 的
// UT 分钟数, 以及无解时的显式状态。
//
// 与 NOAA 官方 main.js 的 calcSunriseSetUTC/calcSunriseSet 同构 (含二次迭代:
// 先用 0h UT 的赤纬求近似时刻, 再用该时刻重算, 消除地球公转带来的误差)。
func sunEventUTCMinutes(lat, lon, jd0 float64, kind Kind) (float64, EventStatus) {
	minutes, status := sunriseSetUTCMinutes(lat, lon, jd0, kind)
	if status != StatusOK {
		return 0, status
	}
	// 二次迭代: 用近似时刻的儒略日重算 (NOAA calcSunriseSet 的第二行)。
	refined, status2 := sunriseSetUTCMinutes(lat, lon, jd0+minutes/minutesPerDay, kind)
	if status2 != StatusOK {
		return 0, status2
	}
	return refined, StatusOK
}

func sunriseSetUTCMinutes(lat, lon, jd float64, kind Kind) (float64, EventStatus) {
	t := julianCentury(jd)
	eqTime := equationOfTime(t)   // 分钟
	solarDec := sunDeclination(t) // 度
	haArg := hourAngleArg(lat, solarDec)

	// 无解信号: |cos H| > 1。这是极昼/极夜的**正常**数学信号。
	//
	// ⚠️ 绝对禁止把 haArg clamp 到 [-1,1] (选型文档 §6.1 的明令禁止项):
	// clamp 会让朗伊尔城在极夜"算出"一个日出时刻 (实际是把太阳压在地平线上),
	// 规则会在错误时刻执行 BMS 动作。sun_test.go 的 TestGoldenPolarNoClamp
	// 与 TestNoClampEverFabricatesTime 把该禁止项钉死。
	//
	// 判据方向 (与 NOAA 实现一致):
	//   haArg >  1 ⇒ 太阳**终日在地平线下** ⇒ 极夜 always_down (日出与日落都不存在)
	//   haArg < -1 ⇒ 太阳**终日在地平线上** ⇒ 极昼 always_up
	if haArg > 1 {
		return 0, StatusAlwaysDown
	}
	if haArg < -1 {
		return 0, StatusAlwaysUp
	}

	ha := math.Acos(haArg) // 弧度, ∈ [0, π]
	if kind == Sunset {
		ha = -ha // NOAA: "for sunset, use -HA"
	}
	delta := lon + ha*degPerRad
	return 720 - 4*delta - eqTime, StatusOK
}

// hourAngleArg 返回 cos(时角) 的原始值 (可能越界 —— 越界即极昼/极夜信号)。
// 与 NOAA main.js calcHourAngleSunrise 逐字同式, 仅天顶角取常量化。
func hourAngleArg(lat, solarDec float64) float64 {
	latRad := lat / degPerRad
	decRad := solarDec / degPerRad
	return math.Cos(ZenithDeg/degPerRad)/(math.Cos(latRad)*math.Cos(decRad)) -
		math.Tan(latRad)*math.Tan(decRad)
}

// julianDay 返回给定公历日期 0h UT 的儒略日 (NOAA getJD, 格里高利历)。
// 对 1800–2100 精度充分 (NOAA 对这套近似式的适用区间声明)。
func julianDay(year int, month time.Month, day int) float64 {
	y, m := year, int(month)
	if m <= 2 {
		y--
		m += 12
	}
	a := math.Floor(float64(y) / 100)
	b := 2 - a + math.Floor(a/4)
	return math.Floor(365.25*float64(y+4716)) + math.Floor(30.6001*float64(m+1)) +
		float64(day) + b - 1524.5
}

func julianCentury(jd float64) float64 { return (jd - julianDayJ2000) / 36525.0 }

// julianToTime 把绝对儒略日瞬时转成 time.Time (UTC)。
func julianToTime(jd float64) time.Time {
	ms := (jd - julianDayUnixEpoch) * msPerDay
	sec := int64(ms / 1000)
	nsec := int64((ms - float64(sec)*1000) * 1e6)
	return time.Unix(sec, nsec).UTC()
}

// dayDelta 返回 (y2,m2,d2) - (y1,m1,d1) 的天数 (仅用日期, 不含时分秒)。
func dayDelta(y1 int, m1 time.Month, d1 int, y2 int, m2 time.Month, d2 int) int {
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a) / (24 * time.Hour))
}

// ───────────────────────── NOAA/Meeus 简化式各项 ─────────────────────────
// 以下五式与 NOAA 官方 main.js 同构 (逐式对应 calcGeomMeanLongSun /
// calcGeomMeanAnomalySun / calcEccentricityEarthOrbit / calcSunEqOfCenter /
// calcSunApparentLong / calcMeanObliquityOfEcliptic / calcObliquityCorrection /
// calcSunDeclination / calcEquationOfTime)。

func geomMeanLongSun(t float64) float64 {
	l0 := 280.46646 + t*(36000.76983+t*0.0003032)
	for l0 > 360 {
		l0 -= 360
	}
	for l0 < 0 {
		l0 += 360
	}
	return l0
}

func geomMeanAnomalySun(t float64) float64 {
	return 357.52911 + t*(35999.05029-0.0001537*t)
}

func eccentricityEarthOrbit(t float64) float64 {
	return 0.016708634 - t*(0.000042037+0.0000001267*t)
}

func sunEqOfCenter(t float64) float64 {
	m := geomMeanAnomalySun(t) / degPerRad
	sinm, sin2m, sin3m := math.Sin(m), math.Sin(2*m), math.Sin(3*m)
	return sinm*(1.914602-t*(0.004817+0.000014*t)) + sin2m*(0.019993-0.000101*t) + sin3m*0.000289
}

func sunTrueLong(t float64) float64 { return geomMeanLongSun(t) + sunEqOfCenter(t) }

func sunApparentLong(t float64) float64 {
	omega := 125.04 - 1934.136*t
	return sunTrueLong(t) - 0.00569 - 0.00478*math.Sin(omega/degPerRad)
}

func meanObliquityOfEcliptic(t float64) float64 {
	seconds := 21.448 - t*(46.8150+t*(0.00059-t*0.001813))
	return 23.0 + (26.0+seconds/60.0)/60.0
}

func obliquityCorrection(t float64) float64 {
	omega := 125.04 - 1934.136*t
	return meanObliquityOfEcliptic(t) + 0.00256*math.Cos(omega/degPerRad)
}

// sunDeclination 返回太阳赤纬 (度)。
func sunDeclination(t float64) float64 {
	e := obliquityCorrection(t) / degPerRad
	lambda := sunApparentLong(t) / degPerRad
	return math.Asin(math.Sin(e)*math.Sin(lambda)) * degPerRad
}

// equationOfTime 返回均时差 (分钟)。
func equationOfTime(t float64) float64 {
	epsilon := obliquityCorrection(t) / degPerRad
	l0 := geomMeanLongSun(t) / degPerRad
	e := eccentricityEarthOrbit(t)
	m := geomMeanAnomalySun(t) / degPerRad

	y := math.Tan(epsilon / 2)
	y *= y
	sin2l0 := math.Sin(2 * l0)
	sinm := math.Sin(m)
	cos2l0 := math.Cos(2 * l0)
	sin4l0 := math.Sin(4 * l0)
	sin2m := math.Sin(2 * m)
	etime := y*sin2l0 - 2*e*sinm + 4*e*y*sinm*cos2l0 - 0.5*y*y*sin4l0 - 1.25*e*e*sin2m
	return etime * degPerRad * 4.0
}
