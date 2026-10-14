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
	jdBase := julianDay(y, m, d)

	// ── 归日: 找出**唯一**落在目标本地日内的事件瞬时 ──
	//
	// 为什么不能只算一次: 经度与时区不匹配 (例如经度 179° 却配 UTC), 或高纬地区
	// 日落跨过本地午夜时, 用"UT 当日"直接算出的瞬时可能落在相邻的本地日。
	//
	// 为什么不能用"按差值平移 jd0 后迭代" (本实现的初版做法, 已被本包的门禁
	// TestStatusMatchesIndependentSemantics 抓出为**真实缺陷**): 高纬逼近极昼时
	// 日落每天向后漂移几分钟, 若当日事件落在次日本地日, 把 jd0 **向前**平移会
	// 算出更晚的事件, 于是越迭代越远 —— 实测 66.0°N 的 2026-06-08 日落被漂到
	// 06-12 00:44, 整整错 4 天。正确做法是**枚举候选 UT 日取唯一落窗者**。
	//
	// 候选顺序: 先目标日 (绝大多数情况一次命中, 也让缓存预热与常规路径最快),
	// 再 ±1 日, 最后 ±2 日。窗口 ±2 日足以覆盖跨午夜与经度/时区错配的情形。
	var inst time.Time
	found := false
	for _, off := range []int{0, -1, 1, -2, 2} {
		at, ok := sunEventInstant(lat, lon, jdBase+float64(off), kind)
		if !ok {
			continue
		}
		ey, em, ed := at.In(tz).Date()
		if dayDelta(y, m, d, ey, em, ed) == 0 {
			inst, found = at, true
			break
		}
	}

	if found {
		return inst.In(tz), StatusOK, nil
	}

	// ── 日内不存在该事件: 太阳在该本地日内**始终**在阈值同一侧 ──
	//
	// 判据 = 当地正午的时角余弦 (正午时角 H=0 ⇒ cos H = 1): 正午太阳在阈值之上
	// ⟺ 1 > arg。该判据与事件种类**无关**, 因此同时覆盖三种情形:
	//   极昼 (arg < -1)        → 正午在阈值上 → always_up
	//   极夜 (arg >  1)        → 正午也在阈值下 → always_down
	//   边界日 (|arg| ≤ 1 但窗口内无穿越, 例如日出已提前到本地 00:00 之前)
	//                          → 由正午高度判定, 不再按 kind 猜
	arg := hourAngleArg(lat, sunDeclination(julianCentury(jdBase)))
	if arg > 1 {
		return time.Time{}, StatusAlwaysDown, nil
	}
	return time.Time{}, StatusAlwaysUp, nil
}

// sunEventInstant 返回以 jd0 (UT 日) 为基准求解出的事件绝对瞬时。
// 含 NOAA calcSunriseSet 的二次迭代: 先用 0h UT 的赤纬求近似时刻, 再用该时刻
// 重算, 消除地球公转带来的误差。
//
// ok=false 表示该 UT 日**不存在穿越** (时角余弦全程越界), 而不是某一种极昼/
// 极夜状态 —— 极昼与极夜的**取向**只在 SunEvent 的无事件分支里判定一次。
// 为什么不让本函数也返回状态: 两处判定同一件事会留下"改了 A 忘了 B"的漂移面,
// 且调用方只关心"有没有解"。单一判定点 (本批"不过度设计"约束的体现)。
func sunEventInstant(lat, lon, jd0 float64, kind Kind) (time.Time, bool) {
	minutes, ok := sunriseSetUTCMinutes(lat, lon, jd0, kind)
	if !ok {
		return time.Time{}, false
	}
	refined, ok2 := sunriseSetUTCMinutes(lat, lon, jd0+minutes/minutesPerDay, kind)
	if !ok2 {
		return time.Time{}, false
	}
	return julianToTime(jd0 + refined/minutesPerDay), true
}

// sunriseSetUTCMinutes 是 NOAA/Meeus 简化式的核心: 返回事件相对 jd 当日
// 0h UT 的 UT 分钟数; ok=false 表示该 UT 日无穿越 (|cos H| 越界)。
//
// 与 NOAA 官方 main.js 的 calcHourAngleSunrise/calcSunriseSetUTC 同构。
func sunriseSetUTCMinutes(lat, lon, jd float64, kind Kind) (float64, bool) {
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
	if haArg > 1 || haArg < -1 {
		return 0, false // 该 UT 日无穿越 (极昼/极夜, 或穿越落在相邻 UT 日)
	}

	ha := math.Acos(haArg) // 弧度, ∈ [0, π]
	if kind == Sunset {
		ha = -ha // NOAA: "for sunset, use -HA"
	}
	delta := lon + ha*degPerRad
	return 720 - 4*delta - eqTime, true
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
