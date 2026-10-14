package sun

import (
	"errors"
	"math"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// 黄金向量 —— 两级判据 (口径由统筹方 2026-10-14 复核后改判)
//
// 【主判据】TestGoldenNOAAOfficialImplementation / ±2 秒
//   期望值 = 按 NOAA 官方计算器实现 (main.js) 的**公开公式语义**复算得到的
//   绝对时刻, 不是"跑我们自己的实现再抄回来"。
//
//   为什么不算自证: 自证的定义是"用被检实现的输出当期望值", 那样门禁恒真。
//   这里的期望值来自**外部实现** —— NOAA 的 main.js 是第三方代码, 我们
//   逐式复现的是它的公式; 判据是"两份独立实现的数值一致性"。任何一处公式
//   笔误 (平黄经系数、中心差、黄赤交角、均时差、时角、迭代) 都会立刻超出 2 秒。
//
//   来源 (2026-10-14 实测下载, 非转述):
//     url    https://gml.noaa.gov/grad/solcalc/main.js
//     sha256 3832956f24724eafacf9e18173b9299b1554d6252bed0784d9c9aca6d9f54856
//     关键行 :226 calcHourAngleSunrise 内 Math.cos(degToRad(90.833)) (天顶角一手来源)
//             :357-368 calcSunriseSetUTC (720 - 4*delta - eqTime 与二次迭代)
//     url    https://gml.noaa.gov/grad/solcalc/calcdetails.html
//     sha256 3da7e18c6d2a9e4b0070214cdda1ab3d6fa02c3e700f0b4964982e355ce891ef
//     原文   "For sunrise and sunset calculations, we assume 0.833° of atmospheric refraction."
//
// 【旁证】TestExternalSourceConsistency / ±3 分钟且方向一致
//   sunrise-sunset.org 的源值 (docs/分析/日出日落黄金向量-2026-10-14.md §1)
//   作为**独立第三方实现**的一致性检查, 不 pin 精度。
//   实测该服务与 NOAA 官方实现存在**系统性同向偏差** (日出偏晚、日落偏早,
//   1.1~2.6 分钟), 说明它用了比 90.833° 略大的天顶角。故按 ±3 分钟 + 方向
//   断言: 它证明我们与另一个独立实现同量级, 但精度以 NOAA 官方为准。
// ═══════════════════════════════════════════════════════════════════════════

// noaaRef 是按 NOAA main.js 公式复算得到的期望值 (UTC 绝对时刻)。null = 无解。
type noaaRef struct {
	id          string
	lat, lon    float64
	tz          string
	localDate   string
	wantSunrise string // RFC3339 UTC; "" = 无解
	wantSunset  string
	wantStatus  EventStatus // 无解时的期望状态; 有解时为空
}

var noaaGolden = []noaaRef{
	// 中纬度·春分 / 夏至 / 冬至
	{"beijing_equinox", 39.9042, 116.4074, "Asia/Shanghai", "2026-03-20",
		"2026-03-19T22:18:31.664Z", "2026-03-20T10:25:56.088Z", ""},
	{"beijing_summer_solstice", 39.9042, 116.4074, "Asia/Shanghai", "2026-06-21",
		"2026-06-20T20:45:55.965Z", "2026-06-21T11:46:19.173Z", ""},
	{"beijing_winter_solstice", 39.9042, 116.4074, "Asia/Shanghai", "2026-12-21",
		"2026-12-20T23:32:08.581Z", "2026-12-21T08:52:25.270Z", ""},
	// 南半球·六月冬至 (与北半球季节相反)
	{"sydney_june_solstice", -33.8688, 151.2093, "Australia/Sydney", "2026-06-21",
		"2026-06-20T20:59:57.381Z", "2026-06-21T06:53:49.983Z", ""},
	// 赤道
	{"equator_equinox", 0, 0, "UTC", "2026-03-20",
		"2026-03-20T06:04:10.443Z", "2026-03-20T18:10:41.320Z", ""},
	// 跨时区: 乌鲁木齐 (几何位置与北京差异大, 但同用 Asia/Shanghai)
	{"urumqi_summer_solstice", 43.83, 87.62, "Asia/Shanghai", "2026-06-21",
		"2026-06-20T22:27:16.998Z", "2026-06-21T13:55:18.111Z", ""},
	// DST 切换日 (2026-03-08 02:00 美东切到 EDT): 必须用 IANA 名, 固定偏移会差 1 小时
	{"newyork_dst_switch", 40.7128, -74.006, "America/New_York", "2026-03-08",
		"2026-03-08T11:18:52.440Z", "2026-03-08T22:55:12.665Z", ""},
	// 极昼 / 极夜: 只断言状态
	{"svalbard_polar_day", 78.22, 15.65, "Arctic/Longyearbyen", "2026-06-21",
		"", "", StatusAlwaysUp},
	{"svalbard_polar_night", 78.22, 15.65, "Arctic/Longyearbyen", "2026-12-21",
		"", "", StatusAlwaysDown},
	// 极圈边界 (选型 §5.2 第 9 条): 66.5°N 的状态翻转日
	{"arctic_66_5_polar_day", 66.5, 25.0, "Europe/Helsinki", "2026-06-21",
		"", "", StatusAlwaysUp},
}

func TestGoldenNOAAOfficialImplementation(t *testing.T) {
	const tol = 2 * time.Second
	for _, c := range noaaGolden {
		tz := mustLoad(t, c.tz)
		date := mustParseLocal(t, c.localDate, tz)
		for _, kind := range []Kind{Sunrise, Sunset} {
			wantStr := c.wantSunrise
			if kind == Sunset {
				wantStr = c.wantSunset
			}
			got, status, err := SunEvent(c.lat, c.lon, tz, date, kind)
			if err != nil {
				t.Fatalf("%s/%s: 意外错误 %v", c.id, kind, err)
			}
			if wantStr == "" {
				// 无解: 必须返回显式状态 + **零值时间**, 不编造时刻。
				if status != c.wantStatus {
					t.Errorf("%s/%s: status=%s want %s", c.id, kind, status, c.wantStatus)
				}
				if !got.IsZero() {
					t.Errorf("%s/%s: 无解时返回了非零时间 %v —— 必须返回零值 (选型 §6.1)", c.id, kind, got)
				}
				continue
			}
			if status != StatusOK {
				t.Fatalf("%s/%s: status=%s want ok", c.id, kind, status)
			}
			want, perr := time.Parse(time.RFC3339Nano, wantStr)
			if perr != nil {
				t.Fatalf("测试向量 %q 解析失败: %v", wantStr, perr)
			}
			delta := got.Sub(want)
			if delta < 0 {
				delta = -delta
			}
			if delta > tol {
				t.Errorf("%s/%s: got %s want %s (差 %s > %s 容差) —— "+
					"与 NOAA 官方实现不一致, 优先检查天顶角常数 90.833° 与各公式系数",
					c.id, kind, got.UTC().Format(time.RFC3339Nano), want.UTC().Format(time.RFC3339Nano), delta, tol)
			}
			// 归日不变式: 返回时刻必须落在请求的**节点本地**日历日内。
			if ld := got.In(tz).Format("2006-01-02"); ld != c.localDate {
				t.Errorf("%s/%s: 归日错误, 落在 %s 而请求 %s (必须按节点时区归日, 方案 §3.1.3)",
					c.id, kind, ld, c.localDate)
			}
		}
	}
}

// ── 极圈边界: 状态翻转日 ────────────────────────────────────────────────
//
// 66.5°N / 25.0°E 用 NOAA 公式扫描 2026 全年得到的翻转:
//
//	2026-06-07  ok → always_up   (此后进入极昼)
//	2026-07-07  always_up → ok
//
// 外部交叉确认 (sunrise-sunset.org, Europe/Helsinki):
//
//	2026-06-03 day_length=84252 > 0 ⇒ 状态 ok
//	2026-06-21 day_length=0       ⇒ 状态 always_up
//
// 高纬只断言**状态**, 不断言时刻 (选型 §5.2: 高纬算法误差 10 分钟, 不设时间断言)。
func TestArcticCircleBoundaryStateFlip(t *testing.T) {
	const lat, lon = 66.5, 25.0
	tz := mustLoad(t, "Europe/Helsinki")
	cases := []struct {
		date   string
		status EventStatus
	}{
		{"2026-06-05", StatusOK},       // 翻转前一日: 仍有日出
		{"2026-06-06", StatusOK},       // 翻转前一日
		{"2026-06-07", StatusAlwaysUp}, // 翻转日
		{"2026-06-21", StatusAlwaysUp}, // 极昼
		{"2026-07-06", StatusAlwaysUp}, // 翻转前一日 (另一侧)
		{"2026-07-07", StatusOK},       // 翻转回 ok
	}
	for _, c := range cases {
		date := mustParseLocal(t, c.date, tz)
		_, status, err := SunEvent(lat, lon, tz, date, Sunrise)
		if err != nil {
			t.Fatalf("%s: %v", c.date, err)
		}
		if status != c.status {
			t.Errorf("66.5N %s: status=%s want %s (翻转点两侧都应有外部证据)", c.date, status, c.status)
		}
	}
}

// ── 旁证: 外部服务一致性 (±3 分钟 + 方向一致) ───────────────────────────
//
// 期望值逐位抄自 docs/分析/日出日落黄金向量-2026-10-14.md §1 (来源
// sunrise-sunset.org API, formatted=0)。**不得**用本实现输出反填。
var externalGolden = []struct {
	id              string
	lat, lon        float64
	tz, localDate   string
	sunrise, sunset string // 本地钟面 "HH:MM:SS"
}{
	{"beijing_equinox", 39.9042, 116.4074, "Asia/Shanghai", "2026-03-20", "06:16:44", "18:27:05"},
	{"beijing_summer_solstice", 39.9042, 116.4074, "Asia/Shanghai", "2026-06-21", "04:44:23", "19:47:51"},
	{"sydney_june_solstice", -33.8688, 151.2093, "Australia/Sydney", "2026-06-21", "06:58:34", "16:55:13"},
	{"equator_equinox", 0, 0, "UTC", "2026-03-20", "06:03:03", "18:11:51"},
	{"urumqi_summer_solstice", 43.83, 87.62, "Asia/Shanghai", "2026-06-21", "06:25:35", "21:56:59"},
	{"newyork_dst_switch", 40.7128, -74.006, "America/New_York", "2026-03-08", "07:17:04", "18:56:22"},
}

func TestExternalSourceConsistency(t *testing.T) {
	const tol = 3 * time.Minute
	for _, c := range externalGolden {
		tz := mustLoad(t, c.tz)
		date := mustParseLocal(t, c.localDate, tz)

		gotRise, st, err := SunEvent(c.lat, c.lon, tz, date, Sunrise)
		if err != nil || st != StatusOK {
			t.Fatalf("%s sunrise: status=%s err=%v", c.id, st, err)
		}
		gotSet, st, err := SunEvent(c.lat, c.lon, tz, date, Sunset)
		if err != nil || st != StatusOK {
			t.Fatalf("%s sunset: status=%s err=%v", c.id, st, err)
		}
		wantRise := mustClock(t, c.localDate, c.sunrise, tz)
		wantSet := mustClock(t, c.localDate, c.sunset, tz)

		// 方向一致性: 该外部实现的天顶角比 NOAA 的 90.833° 略大
		// ⇒ 其日出更早、日落更晚。方向反了说明我们的公式有系统性错误。
		if !gotRise.After(wantRise) {
			t.Errorf("%s: 我们的日出 %s 不晚于外部源 %s —— 方向应与外部源的系统性偏差一致",
				c.id, gotRise.Format("15:04:05"), c.sunrise)
		}
		if !gotSet.Before(wantSet) {
			t.Errorf("%s: 我们的日落 %s 不早于外部源 %s —— 方向应与外部源的系统性偏差一致",
				c.id, gotSet.Format("15:04:05"), c.sunset)
		}
		if d := gotRise.Sub(wantRise); d > tol || d < -tol {
			t.Errorf("%s sunrise: 与外部源相差 %s (> %s)", c.id, d, tol)
		}
		if d := gotSet.Sub(wantSet); d > tol || d < -tol {
			t.Errorf("%s sunset: 与外部源相差 %s (> %s)", c.id, d, tol)
		}
	}
}

// ── 极昼/极夜: 禁止 clamp 的钉死 ────────────────────────────────────────
//
// ⚠️ 外部权威源 sunrise-sunset.org 在极昼/极夜时返回的是 **1970-01-01 的
// Unix epoch 哨兵值** (day_length=0), 而不是显式错误码 —— 实证见
// docs/分析/日出日落黄金向量-2026-10-14.md §2.1。
//
// 这就是"绝不返回零值时间"这一要求的现实依据: 把哨兵值喂给下游边沿判定,
// 会被当成"1970 已过"或"今天 00:00" ⇒ **午夜误触发** (安全相关)。
// 故本实现返回显式状态 + 零值时间 + 调用方必须先看状态。
func TestGoldenPolarNoClamp(t *testing.T) {
	cases := []struct {
		id     string
		lat    float64
		date   string
		status EventStatus
	}{
		{"svalbard_polar_day", 78.22, "2026-06-21", StatusAlwaysUp},
		{"svalbard_polar_night", 78.22, "2026-12-21", StatusAlwaysDown},
	}
	tz := mustLoad(t, "Arctic/Longyearbyen")
	for _, c := range cases {
		date := mustParseLocal(t, c.date, tz)
		for _, kind := range []Kind{Sunrise, Sunset} {
			got, status, err := SunEvent(c.lat, 15.65, tz, date, kind)
			if err != nil {
				t.Fatalf("%s/%s: %v", c.id, kind, err)
			}
			if status != c.status {
				t.Fatalf("%s/%s: status=%s want %s", c.id, kind, status, c.status)
			}
			if !got.IsZero() {
				t.Fatalf("%s/%s: 无解时必须返回零值时间, got %v", c.id, kind, got)
			}
			// 显式反哨兵断言: 绝不能把外部服务的 1970 哨兵当成结果返回。
			if got.Year() == 1970 || got.Unix() == 0 {
				t.Fatalf("%s/%s: 返回了 1970 哨兵值 —— 正是外部服务的失败模式", c.id, kind)
			}
		}
	}
}

// TestNoClampEverFabricatesTime 把"禁止 clamp"钉死为**全域性质**:
// 扫 60°..85°N 全年的每个本地日, 只要状态不是 ok, 就必须返回零值时间;
// 只要状态是 ok, 就必须返回非零时间。若有人把余弦项 clamp 到 [-1,1],
// 极夜日会"算出"一个虚假时刻 ⇒ 本测试立刻变红。
func TestNoClampEverFabricatesTime(t *testing.T) {
	tz := mustLoad(t, "Arctic/Longyearbyen")
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, tz)
	polarDaysSeen := 0
	for _, lat := range []float64{60, 66.5, 70, 78.22, 85} {
		for d := 0; d < 365; d++ {
			date := start.AddDate(0, 0, d)
			for _, kind := range []Kind{Sunrise, Sunset} {
				at, status, err := SunEvent(lat, 15.65, tz, date, kind)
				if err != nil {
					t.Fatalf("lat=%v %s: %v", lat, date.Format("2006-01-02"), err)
				}
				switch status {
				case StatusOK:
					if at.IsZero() {
						t.Fatalf("lat=%v %s/%s: status=ok 却返回零值时间", lat, date.Format("2006-01-02"), kind)
					}
				case StatusAlwaysUp, StatusAlwaysDown:
					polarDaysSeen++
					if !at.IsZero() {
						t.Fatalf("lat=%v %s/%s: status=%s 却返回非零时间 %v —— "+
							"极昼/极夜无解时绝不允许编造时刻 (禁止 clamp, 选型 §6.1)",
							lat, date.Format("2006-01-02"), kind, status, at)
					}
				default:
					t.Fatalf("未知状态 %q", status)
				}
			}
		}
	}
	// 分母守卫: 若扫描没碰到任何极昼/极夜日, 本门禁是空转 (恒真)。
	if polarDaysSeen == 0 {
		t.Fatal("扫描未命中任何极昼/极夜日 —— 门禁空转, 检查扫描范围")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 禁止 clamp 的**独立语义判据** (选型文档 §6.1 的明令禁止项)
//
// 为什么需要这条独立判据 (统筹方 2026-10-14 变异测试的结论):
// TestNoClampEverFabricatesTime 只断言"状态与时间是否自洽" —— 它抓不住
// "把无解情形 clamp 成一个时刻并声明为 ok" 这种实现: 那种实现下状态与时间
// **依然自洽**, 只是状态错了。故必须有一条**独立于被测实现**的判据回答
// "这一天到底该不该有解", 再断言函数返回的状态与之一致。
//
// 独立性的诚实边界: 本判据在测试内**重新实现**了太阳赤纬与天顶角时角的
// **公开公式** (NOAA 发布的系数), 不调用被测包的任何私有函数。它与被测
// 实现共享同一套公开公式 ⇒ 它的价值在于咬住 clamp / 状态极性反转 /
// 纬度带分类错误, 而**不**负责发现公式系数笔误 (那一类由
// TestGoldenNOAAOfficialImplementation 的 ±2 秒判据负责)。两者互补。
// ═══════════════════════════════════════════════════════════════════════════

// refArgAt 独立复算**给定儒略日**的时角余弦 (测试内实现, 不调用被测包)。
//
// ⚠️ 必须接收**精确儒略日**而不是 time.Time: 若把 time.Time 截断到当日 0h UT,
// 会丢掉日内的小数部分, 使赤纬停在该 UT 日零点 —— 二分迭代越靠近边界, 这层
// 误差越显著 (实测: 60°N 春分附近日落被判成 2 分 00.1 秒偏差而误红)。测试内
// 一律用精确 jd 计算。
func refArgAt(lat float64, jd float64) float64 {
	t := (jd - 2451545.0) / 36525.0

	// 几何平黄经 / 平近点角 / 中心差 / 视黄经 (NOAA 公开系数)。
	l0 := 280.46646 + t*(36000.76983+t*0.0003032)
	for l0 > 360 {
		l0 -= 360
	}
	for l0 < 0 {
		l0 += 360
	}
	manom := 357.52911 + t*(35999.05029-0.0001537*t)
	mrad := manom * math.Pi / 180
	c := math.Sin(mrad)*(1.914602-t*(0.004817+0.000014*t)) +
		math.Sin(2*mrad)*(0.019993-0.000101*t) +
		math.Sin(3*mrad)*0.000289
	omega := 125.04 - 1934.136*t
	lambda := l0 + c - 0.00569 - 0.00478*math.Sin(omega*math.Pi/180)
	sec := 21.448 - t*(46.8150+t*(0.00059-t*0.001813))
	e0 := 23.0 + (26.0+sec/60.0)/60.0
	eps := (e0 + 0.00256*math.Cos(omega*math.Pi/180)) * math.Pi / 180
	decl := math.Asin(math.Sin(eps)*math.Sin(lambda*math.Pi/180)) * 180 / math.Pi

	latRad := lat * math.Pi / 180
	decRad := decl * math.Pi / 180
	zRad := 90.833 * math.Pi / 180
	return math.Cos(zRad)/(math.Cos(latRad)*math.Cos(decRad)) - math.Tan(latRad)*math.Tan(decRad)
}

// refEqTimeMinutes 独立复算均时差 (分钟, NOAA 公开系数)。
func refEqTimeMinutes(t float64) float64 {
	l0 := 280.46646 + t*(36000.76983+t*0.0003032)
	for l0 > 360 {
		l0 -= 360
	}
	for l0 < 0 {
		l0 += 360
	}
	manom := 357.52911 + t*(35999.05029-0.0001537*t)
	ecc := 0.016708634 - t*(0.000042037+0.0000001267*t)
	sec := 21.448 - t*(46.8150+t*(0.00059-t*0.001813))
	eps := (23.0 + (26.0+sec/60.0)/60.0 + 0.00256*math.Cos((125.04-1934.136*t)*math.Pi/180)) * math.Pi / 180
	y := math.Tan(eps / 2)
	y *= y
	l0r := l0 * math.Pi / 180
	mr := manom * math.Pi / 180
	etime := y*math.Sin(2*l0r) - 2*ecc*math.Sin(mr) + 4*ecc*y*math.Sin(mr)*math.Cos(2*l0r) -
		0.5*y*y*math.Sin(4*l0r) - 1.25*ecc*ecc*math.Sin(2*mr)
	return etime * 180 / math.Pi * 4.0
}

// refInDayEvent 是**独立于被测实现**的判据: 返回该本地日内是否真的存在
// 该 kind 的穿越瞬时, 以及该瞬时 (由测试内公式独立求解)。
//
// 为什么不能只判"余弦项是否越界" (统筹方最初建议的简化版, 本实现初期采用过):
// 高纬临近极昼时, 日落会**跨过本地午夜** (实测 66.0°N 的 2026-06-05:
// 日落实际发生在次日 00:15, 外部源同样把它的日期标为 06-06)。
// 因此"几何上存在穿越"不等于"该本地日内存在穿越瞬时"。只判越界会把
// 这种日子误判为必须 ok, 而正确行为恰恰是"本日无事件"。
// ⇒ 判据必须落到**瞬时是否落在本日窗口内**这一层。
// dayProfile 是独立判据对"一个本地日"的结论。
type dayProfile struct {
	// polar 为真表示在整个本地日内, 时角余弦**始终**在同一侧越过 ±1 ——
	// 即该日不存在任何时刻的穿越, 必然是无事件的极昼/极夜日。
	polar bool
	// side 在 polar 为真时给出应该的状态 (always_up / always_down)。
	side EventStatus
}

// refDayProfile 独立判定该本地日的性质 (不调用被测包任何函数)。
//
// 判据 = 在本地日内**逐点采样**时角余弦, 看它是否始终落在 ±1 之外。
//   - 始终 > 1  ⇒ 太阳终日在地平线下 ⇒ always_down
//   - 始终 < -1 ⇒ 太阳终日在地平线上 ⇒ always_up
//   - 其余      ⇒ 日内存在穿越 (或有穿越落在窗口外), 属"有歧义日", 本判据不表态
//
// 为什么"逐点始终越界"才是 clamp-proof 的: clamp 的实现会把 acos 的参数压到
// ±1, 于是它**给不出非 ok 状态** —— 在全程越界的日子里它必然返回 ok。所以
// "全程越界的日子必须非 ok"这一条足以咬住 clamp, 且不依赖对归日边界的精确
// 复刻 (那属于被测实现的职责, 判据重复它只会引入判据自身的缺陷)。
func refDayProfile(lat float64, date time.Time, tz *time.Location) dayProfile {
	y, m, d := date.In(tz).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, tz)

	allAbove, allBelow := true, true
	// 每 10 分钟采样一次, 覆盖整个本地日 (含端点)。
	for step := 0; step <= 24*6; step++ {
		at := start.Add(time.Duration(step) * 10 * time.Minute)
		arg := refArgAt(lat, refJulianDayOf(at))
		if arg <= 1 {
			allAbove = false
		}
		if arg >= -1 {
			allBelow = false
		}
	}
	switch {
	case allAbove:
		return dayProfile{polar: true, side: StatusAlwaysDown}
	case allBelow:
		return dayProfile{polar: true, side: StatusAlwaysUp}
	default:
		return dayProfile{}
	}
}

// refJulianDayOf 把绝对瞬时换算为精确儒略日 (保留日内小数)。
func refJulianDayOf(at time.Time) float64 {
	u := at.UTC()
	sec := float64(u.Unix()) + float64(u.Nanosecond())/1e9
	return 2440587.5 + sec/86400.0
}

// refJulianDay 独立复算儒略日 (NOAA getJD 公历算法)。
func refJulianDay(year int, month time.Month, day int) float64 {
	yy, mm := year, int(month)
	if mm <= 2 {
		yy--
		mm += 12
	}
	a := math.Floor(float64(yy) / 100)
	b := 2 - a + math.Floor(a/4)
	return math.Floor(365.25*float64(yy+4716)) + math.Floor(30.6001*float64(mm+1)) +
		float64(day) + b - 1524.5
}

// refJulianToTime 独立复算儒略日 → time.Time (UTC)。
func refJulianToTime(jd float64) time.Time {
	ms := (jd - 2440587.5) * 86400000.0
	sec := int64(ms / 1000)
	return time.Unix(sec, int64((ms-float64(sec)*1000)*1e6)).UTC()
}

// refDaySide 返回"本日无穿越时"应有的状态: 由**当地正午太阳高度**独立判定。
//
//	正午太阳在阈值之上 ⟺ cos(φ−δ) > cos(90.833°) ⟺ 无穿越且终日可见 ⇒ always_up
//
// 与实现在无解分支用的"时角余弦 > 1 ⇔ 正午在阈值下"互为等价 (代数上可互推)。
func refDaySide(lat float64, date time.Time, tz *time.Location) EventStatus {
	y, m, d := date.In(tz).Date()
	noonLocal := time.Date(y, m, d, 12, 0, 0, 0, tz)
	// 用正午的赤纬求该日太阳在正午的高度余弦。
	jd := refJulianDay(y, m, d) + 0.5
	t := (jd - 2451545.0) / 36525.0
	_ = noonLocal
	decl := refDeclAt(t)
	phi := lat * math.Pi / 180
	dec := decl * math.Pi / 180
	cosZ := math.Sin(phi)*math.Sin(dec) + math.Cos(phi)*math.Cos(dec) // H=0 ⇒ cos z = cos(φ−δ)
	if cosZ > math.Cos(90.833*math.Pi/180) {
		return StatusAlwaysUp
	}
	return StatusAlwaysDown
}

// refDeclAt 独立复算太阳赤纬 (度)。
func refDeclAt(t float64) float64 {
	l0 := 280.46646 + t*(36000.76983+t*0.0003032)
	for l0 > 360 {
		l0 -= 360
	}
	for l0 < 0 {
		l0 += 360
	}
	manom := 357.52911 + t*(35999.05029-0.0001537*t)
	mr := manom * math.Pi / 180
	c := math.Sin(mr)*(1.914602-t*(0.004817+0.000014*t)) +
		math.Sin(2*mr)*(0.019993-0.000101*t) + math.Sin(3*mr)*0.000289
	omega := 125.04 - 1934.136*t
	lambda := l0 + c - 0.00569 - 0.00478*math.Sin(omega*math.Pi/180)
	sec := 21.448 - t*(46.8150+t*(0.00059-t*0.001813))
	eps := (23.0 + (26.0+sec/60.0)/60.0 + 0.00256*math.Cos(omega*math.Pi/180)) * math.Pi / 180
	return math.Asin(math.Sin(eps)*math.Sin(lambda*math.Pi/180)) * 180 / math.Pi
}

// refInDayEvent 独立判定该本地日内是否存在该 kind 的穿越, 并独立求解其瞬时。
//
// 独立性与完备性:
//   - 公式按 NOAA 公开系数在测试内重写 (几何平黄经/中心差/视黄经/黄赤交角/
//     赤纬/均时差), 不调用被测包任何函数;
//   - **枚举候选 UT 日** (0,-1,+1,-2,+2) 取唯一落在本本地日窗口内的解 ——
//     这一点必须与实现同样完备: 实测在 (经度, 时区) 错配的配置下, 约 39% 的
//     样本需要 -1 或 +1 偏移 (另有 16 例需要 -2), 只搜正向会漏解。
//
// 精确度要求: 一律用**精确儒略日**传入 refArgAt。若截断到当日 0h UT, 赤纬会
// 停在该 UT 日零点, 边界附近的偏差可达 2 分钟以上 (本判据初版因此误红, 记此)。
func refInDayEvent(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, bool) {
	y, m, d := date.In(tz).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, tz)
	end := start.Add(24 * time.Hour)
	jdBase := refJulianDay(y, m, d)

	// 与 NOAA calcSunriseSetUTC 同构: 第二次重算的结果即为答案。
	solveAt := func(jd float64) (float64, bool) {
		t := (jd - 2451545.0) / 36525.0
		a := refArgAt(lat, jd)
		if math.Abs(a) > 1 {
			return 0, false
		}
		ha := math.Acos(a)
		if kind == Sunset {
			ha = -ha
		}
		return 720 - 4*(lon+ha*180/math.Pi) - refEqTimeMinutes(t), true
	}

	for _, off := range []int{0, -1, 1, -2, 2} {
		jd := jdBase + float64(off)
		m0, ok := solveAt(jd)
		if !ok {
			continue
		}
		m1, ok := solveAt(jd + m0/1440.0)
		if !ok {
			continue
		}
		inst := refJulianToTime(jd + m1/1440.0)
		if !inst.Before(start) && inst.Before(end) {
			return inst, true
		}
	}
	return time.Time{}, false
}

// refPolarAllDay 独立判定该本地日是否**整天**越界 (即无任何穿越), 并给出取向。
func refPolarAllDay(lat float64, date time.Time, tz *time.Location) (EventStatus, bool) {
	y, m, d := date.In(tz).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, tz)
	allAbove, allBelow := true, true
	for step := 0; step <= 24*6; step++ {
		arg := refArgAt(lat, refJulianDayOf(start.Add(time.Duration(step)*10*time.Minute)))
		if arg <= 1 {
			allAbove = false
		}
		if arg >= -1 {
			allBelow = false
		}
	}
	switch {
	case allAbove:
		return StatusAlwaysDown, true
	case allBelow:
		return StatusAlwaysUp, true
	}
	return "", false
}

// edgeBandMinutes 是"穿越瞬时贴近本地日边界"的忽略带 (分钟)。
//
// 为什么需要它: 当穿越恰好发生在本地日边界前后数分钟内时, 判据与实现在
// "算得稍早/稍晚"上的浮点差异就可能把它们分到相邻两侧, 那是数值边界而非缺陷。
// 落入该带的样本不做穿越/无穿越的**分类**断言, 但仍断言状态与时间的自洽性。
// 带宽 5 分钟远小于真实缺陷的量级 (漏解整整一天, 或归日错 4 天)。
const edgeBandMinutes = 5

// TestStatusMatchesIndependentSemantics 是**主门禁**, 同时钉死:
//
//	(1) 极昼/极夜不被 clamp (全程越界的日子必须非 ok, 且取向正确);
//	(2) 归日完备 (本本地日内确有穿越的日子必须被找到并落在当日);
//	(3) 有事件日的时刻与独立求解值一致 (±2 分钟)。
//
// 判据完全独立于被测实现 (见 refInDayEvent / refPolarAllDay 的说明)。
func TestStatusMatchesIndependentSemantics(t *testing.T) {
	const tol = 2 * time.Minute
	const lon = 15.65
	tz := mustLoad(t, "Arctic/Longyearbyen")
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, tz)

	withEvent, noEvent, polarUp, polarDown := 0, 0, 0, 0
	for _, lat := range []float64{60.0, 64.0, 66.0, 66.5, 67.0, 70.0, 78.22, 85.0} {
		for d := 0; d < 365; d++ {
			date := start.AddDate(0, 0, d)
			wantAt, hasEvent := refInDayEvent(lat, lon, tz, date, kind0())
			wantSide, isPolar := refPolarAllDay(lat, date, tz)
			for _, kind := range []Kind{Sunrise, Sunset} {
				// 判据与实现各自求解 sunrise/sunset, 分别判定。
				wantKindAt, hasKindEvent := refInDayEvent(lat, lon, tz, date, kind)
				_ = wantAt
				_ = hasEvent
				got, status, err := SunEvent(lat, lon, tz, date, kind)
				if err != nil {
					t.Fatalf("lat=%v %s/%s: %v", lat, date.Format("2006-01-02"), kind, err)
				}
				ds := date.In(tz).Format("2006-01-02")

				// ── 自洽性 (全样本, 永不跳过) ──
				if status == StatusOK && got.IsZero() {
					t.Fatalf("lat=%v %s/%s: status=ok 却返回零值时间", lat, ds, kind)
				}
				if status != StatusOK && !got.IsZero() {
					t.Fatalf("lat=%v %s/%s: status=%s 却返回时刻 %v —— 无事件日不得编造 (选型 §6.1)",
						lat, ds, kind, status, got)
				}

				// ── (1) 极昼/极夜: 全程越界必须非 ok 且取向正确 ──
				if isPolar {
					if status == StatusOK {
						t.Fatalf("lat=%v %s/%s: 独立判据显示整天越界 (应 %s), 实现返回 ok + %v —— "+
							"clamp 或漏判 (选型 §6.1)", lat, ds, kind, wantSide, got.In(tz).Format("15:04:05"))
					}
					if status != wantSide {
						t.Fatalf("lat=%v %s/%s: 无事件日取向错误, 实现=%s 独立判据=%s", lat, ds, kind, status, wantSide)
					}
					noEvent++
					if status == StatusAlwaysUp {
						polarUp++
					} else {
						polarDown++
					}
					continue
				}

				// ── (2)(3) 非全程越界: 由判据决定本日是否有穿越 ──
				if !hasKindEvent {
					// 判据找不到穿越: 可能是"穿越落在相邻本地日"(高纬跨午夜),
					// 也可能是判据/实现在边界上的数值分歧。仅当**贴近边界**时
					// 才允许实现返回 ok, 否则必须非 ok。
					if status == StatusOK {
						// 实现给出的事件离本地日边界多远?
						mid := got.In(tz)
						dayStart := time.Date(mid.Year(), mid.Month(), mid.Day(), 0, 0, 0, 0, tz)
						off := mid.Sub(dayStart)
						if off > edgeBandMinutes*time.Minute && off < 24*time.Hour-edgeBandMinutes*time.Minute {
							t.Fatalf("lat=%v %s/%s: 独立判据在本本地日内找不到穿越, 实现却返回 %v (离日界 %s) —— 归日/求解不一致",
								lat, ds, kind, mid.Format("15:04:05"), off)
						}
					}
					noEvent++
					continue
				}
				withEvent++
				if status != StatusOK {
					t.Fatalf("lat=%v %s/%s: 独立判据求出本日事件 %s, 实现却返回 %s —— 漏解",
						lat, ds, kind, wantKindAt.In(tz).Format("15:04:05"), status)
				}
				if delta := got.Sub(wantKindAt); delta > tol || delta < -tol {
					t.Fatalf("lat=%v %s/%s: 实现 %s 与独立判据 %s 相差 %s (>%s)",
						lat, ds, kind, got.In(tz).Format("15:04:05"), wantKindAt.In(tz).Format("15:04:05"), delta, tol)
				}
				if ld := got.In(tz).Format("2006-01-02"); ld != ds {
					t.Fatalf("lat=%v %s/%s: 归日错误, 时刻落在 %s", lat, ds, kind, ld)
				}
			}
		}
	}
	if withEvent < 3000 {
		t.Fatalf("有事件样本只有 %d 个, 期望 >=3000 —— 扫描范围失效", withEvent)
	}
	if noEvent == 0 || polarUp == 0 || polarDown == 0 {
		t.Fatalf("覆盖不足 (无事件=%d always_up=%d always_down=%d) —— 极昼/极夜未被真实覆盖, 门禁可能空转",
			noEvent, polarUp, polarDown)
	}
	t.Logf("独立语义判据: 有事件 %d / 无事件 %d (always_up %d, always_down %d)", withEvent, noEvent, polarUp, polarDown)
}

// kind0 只是为了让首轮 refInDayEvent 调用有实参 (结果会按 kind 重新计算)。
func kind0() Kind { return Sunrise }

// TestDayAttributionRequiresWideCandidateWindow 钉死"归日候选窗口必须足够宽"。
//
// 为什么单独成例: 归日候选 0/-1/+1/-2/+2 中, **-2 在常规配置下极难触发**
// (全参数扫描 54097 个在窗解里只有 16 例需要它, 占比 0.03%)。统筹方的变异测试
// 把候选收窄到 {0,-1,+1} 后其余用例全绿 —— 也就是说, 没有本用例的话这条规则
// 会在无人察觉时被删掉, 而删掉后那 16 例会静默漏解 (返回 always_* 而实际有
// 日出日落)。以下用例来自全参数扫描定位到的**真实需要 -2 的样本**:
//
//	lat=-66.5 lon=-179.9 tz=Pacific/Kiritimati (UTC+14, 与经度极端错配), 日落
//
// 它们同时被 TestStatusMatchesIndependentSemantics 的扫描覆盖, 但那里是
// "碰巧扫到"; 本用例把样本固定下来, 保证收窄候选窗口立刻变红。
func TestDayAttributionRequiresWideCandidateWindow(t *testing.T) {
	tz := mustLoad(t, "Pacific/Kiritimati") // UTC+14, 与西经 179.9 错配 28 小时
	cases := []struct {
		lat, lon float64
		dates    []string
		kind     Kind
	}{
		{-85.0, -179.9, []string{"2026-10-03", "2026-10-04"}, Sunset},
		{-66.5, -179.9, []string{"2026-01-07", "2026-01-12", "2026-01-16"}, Sunset},
	}
	checked := 0
	for _, c := range cases {
		for _, ds := range c.dates {
			date := mustParseLocal(t, ds, tz)
			wantAt, hasEvent := refInDayEvent(c.lat, c.lon, tz, date, c.kind)
			got, status, err := SunEvent(c.lat, c.lon, tz, date, c.kind)
			if err != nil {
				t.Fatalf("lat=%v %s: %v", c.lat, ds, err)
			}
			// 该日必须确实有事件 (否则本用例失去意义)。
			if !hasEvent {
				t.Fatalf("lat=%v %s/%s: 独立判据也说本日无事件 —— 样本选取失效, 本用例不再能钉住候选窗口",
					c.lat, ds, c.kind)
			}
			checked++
			if status != StatusOK {
				t.Fatalf("lat=%v %s/%s: 独立判据求出本日事件 %s, 实现返回 %s —— "+
					"归日候选窗口过窄 (本样本需要 -2 偏移, 收窄到 {0,-1,+1} 会静默漏解)",
					c.lat, ds, c.kind, wantAt.In(tz).Format("15:04:05"), status)
			}
			if delta := got.Sub(wantAt); delta > 2*time.Minute || delta < -2*time.Minute {
				t.Fatalf("lat=%v %s/%s: 实现 %s 与独立判据 %s 相差 %s",
					c.lat, ds, c.kind, got.In(tz).Format("15:04:05"), wantAt.In(tz).Format("15:04:05"), delta)
			}
			if ld := got.In(tz).Format("2006-01-02"); ld != ds {
				t.Fatalf("lat=%v %s/%s: 归日错误, 时刻落在 %s", c.lat, ds, c.kind, ld)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("只验证了 %d 个样本, 期望 >=5", checked)
	}
	t.Logf("宽候选窗口样本 %d 个全部通过", checked)
}

// TestPolarBandsStructurallyPresent 是**结构性**判据 (不依赖任何公式复算):
// 85°N 在 1 月必须存在 always_down、在 6 月必须存在 always_up;
// 66.5°N 在春秋分附近必须无任何极昼/极夜。clamp 会把全年压成 ok ⇒ 必红。
func TestPolarBandsStructurallyPresent(t *testing.T) {
	tz := mustLoad(t, "Arctic/Longyearbyen")
	count := func(lat float64, from, to time.Time) map[EventStatus]int {
		out := map[EventStatus]int{}
		for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
			_, st, err := SunEvent(lat, 15.65, tz, d, Sunrise)
			if err != nil {
				t.Fatalf("lat=%v %s: %v", lat, d.Format("2006-01-02"), err)
			}
			out[st]++
		}
		return out
	}
	jan := time.Date(2026, 1, 1, 12, 0, 0, 0, tz)
	feb := time.Date(2026, 2, 1, 12, 0, 0, 0, tz)
	jun := time.Date(2026, 6, 1, 12, 0, 0, 0, tz)
	jul := time.Date(2026, 7, 1, 12, 0, 0, 0, tz)

	if got := count(85, jan, feb); got[StatusAlwaysDown] == 0 {
		t.Errorf("85°N 一月未出现 always_down (%v) —— 极夜被 clamp 成 ok? ", got)
	}
	if got := count(85, jun, jul); got[StatusAlwaysUp] == 0 {
		t.Errorf("85°N 六月未出现 always_up (%v) —— 极昼被 clamp 成 ok?", got)
	}
	// 春秋分附近极圈上不该有极昼/极夜。
	mar := time.Date(2026, 3, 10, 12, 0, 0, 0, tz)
	marEnd := time.Date(2026, 3, 25, 12, 0, 0, 0, tz)
	if got := count(66.5, mar, marEnd); got[StatusAlwaysUp]+got[StatusAlwaysDown] != 0 {
		t.Errorf("66.5°N 春分前后不应有极昼/极夜: %v", got)
	}
	// 66.5°N 在 6 月整月必须是 always_up (翻转日 2026-06-07 之后)。
	if got := count(66.5, jun.AddDate(0, 0, 20), jul); got[StatusAlwaysUp] == 0 {
		t.Errorf("66.5°N 六月下旬未出现 always_up: %v", got)
	}
}

// ── 性质测试 (不依赖外部源, 选型 §5.3) ─────────────────────────────────

// TestSymmetrySunriseSunset 同日期的日出/日落关于太阳正午对称。
//
// 判据: 白昼中点 = 太阳正午 = 本地钟面 12:00 - 均时差 - 4*(经度 - 时区中央经线),
// 上界 ±45 分钟 (均时差全年 ∈ [-16.4,+14.3] 分钟, 经度偏离中央经线 ≤ 14.4 分钟,
// 两者之和远小于 45 分钟 ⇒ 能抓住"日出日落不对称"的结构性错误, 又不会把
// 合法的均时差当成缺陷)。
//
// ⚠️ 每个坐标必须配**自己的真实时区**: 给 0°E 配 Asia/Shanghai 时, 归日逻辑会
// 把这个物理上属于 UTC 的事件挪进上海本地日 —— 那是"配置错误"下的既定行为
// (由 TestGoldenNOAAOfficialImplementation 的归日不变式覆盖), 不是对称性缺陷。
func TestSymmetrySunriseSunset(t *testing.T) {
	cases := []struct {
		lat, lon float64
		tz       string
		date     string
	}{
		{39.9042, 116.4074, "Asia/Shanghai", "2026-03-20"},
		{39.9042, 116.4074, "Asia/Shanghai", "2026-06-21"},
		{0, 0, "UTC", "2026-03-20"},
		{-33.8688, 151.2093, "Australia/Sydney", "2026-06-21"},
	}
	for _, c := range cases {
		tz := mustLoad(t, c.tz)
		date := mustParseLocal(t, c.date, tz)
		rise, _, err := SunEvent(c.lat, c.lon, tz, date, Sunrise)
		if err != nil {
			t.Fatal(err)
		}
		set, _, err := SunEvent(c.lat, c.lon, tz, date, Sunset)
		if err != nil {
			t.Fatal(err)
		}
		mid := rise.Add(set.Sub(rise) / 2)
		m := mid.In(tz)
		noon := time.Date(m.Year(), m.Month(), m.Day(), 12, 0, 0, 0, tz)
		off := clockDelta(m, noon)
		if absDuration(off) > 45*time.Minute {
			t.Errorf("lat=%v %s: 白昼中点 %s 偏离当地正午 %s (> 45min) —— 日出日落不对称",
				c.lat, c.date, m.Format("15:04:05"), off)
		}
		// 守卫: 中点必须严格落在日出与日落之间 (构造性不变式, 防止未来改动
		// 把 mid 算成 elsewhere)。
		if !mid.After(rise) || !mid.Before(set) {
			t.Errorf("白昼中点 %v 不在 (日出, 日落) 开区间内", mid)
		}
	}
}

// TestMonotonicContinuity 连续性: 相邻本地日之间的日出时刻变化不得出现断崖
// (状态翻转点除外)。选型 §5.3 的"单调性"——时刻随季节平滑移动, 单日跳变
// 最多几分钟, 出现小时级跳变即说明公式或归日有 bug。
func TestMonotonicContinuity(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	loc := time.Date(2026, 1, 1, 12, 0, 0, 0, tz)
	var prev time.Time
	const maxJump = 10 * time.Minute
	for d := 0; d < 365; d++ {
		date := loc.AddDate(0, 0, d)
		at, status, err := SunEvent(39.9042, 116.4074, tz, date, Sunrise)
		if err != nil {
			t.Fatal(err)
		}
		if status != StatusOK {
			prev = time.Time{}
			continue
		}
		if !prev.IsZero() {
			// 必须比较**当地钟面时刻**, 不能比较绝对瞬时: 相邻两天的事件
			// 本就相隔约 24 小时, 直接相减只会得到 24h±几分钟, 恒大于阈值
			// (这正是本测试初版的错误, 会永远红)。跨 DST 时绝对时刻还会
			// 额外跳 1 小时, 用钟面值同样把它排除在外 (北京无 DST)。
			delta := clockDelta(at.In(tz), prev.In(tz))
			if delta > maxJump {
				t.Errorf("%s: 日出钟面时刻较前一日跳变 %s (> %s) —— 连续性被破坏",
					date.Format("2006-01-02"), delta, maxJump)
			}
		}
		prev = at
	}
}

// TestCoordinatePerturbationContinuous 坐标微扰: 纬度/经度微扰引起的时刻
// 变化必须连续且量级正确 (无断崖)。判据用**当地钟面差值**的绝对值。
//
// 量级依据: 经度每 +1° 使当地太阳时提前 4 分钟 ⇒ +0.5° 约 -2 分钟;
// 纬度 +0.5° 在春分对日出时刻的影响也在分钟级。故上界取 4 分钟/0.5°,
// 若出现小时级跳变 (公式或归日 bug) 立刻暴露。
func TestCoordinatePerturbationContinuous(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	date := mustParseLocal(t, "2026-03-20", tz)
	base, _, err := SunEvent(39.9042, 116.4074, tz, date, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []float64{0.01, 0.1, 0.5} {
		moved, _, err := SunEvent(39.9042+d, 116.4074, tz, date, Sunrise)
		if err != nil {
			t.Fatal(err)
		}
		// 纬度 +d° 在北半球春分使白昼变长 ⇒ 日出更早; 但只断言"连续不跳崖"。
		if delta := absDuration(clockDelta(moved.In(tz), base.In(tz))); delta > 4*time.Minute {
			t.Errorf("纬度 +%v° 引起 %s 跳变 —— 连续性被破坏", d, delta)
		}
		east, _, err := SunEvent(39.9042, 116.4074+d, tz, date, Sunrise)
		if err != nil {
			t.Fatal(err)
		}
		// 经度向东移动 ⇒ 当地太阳时提前 ⇒ 钟面日出时刻**变早** (负向)。
		delta := clockDelta(east.In(tz), base.In(tz))
		if absDuration(delta) > 4*time.Minute {
			t.Errorf("经度 +%v° 引起 %s 跳变 —— 连续性被破坏", d, delta)
		}
		if delta > 0 {
			t.Errorf("经度 +%v° 使日出钟面时刻**推迟** %s —— 方向应为提前 (东移经度越大太阳越早过地平线)",
				d, delta)
		}
		// 定量: 4 分钟/度 ⇒ d° 对应 **-4d** 分钟 (东移变早), 容差 20%。
		want := -time.Duration(4 * d * float64(time.Minute))
		if absDuration(delta-want) > absDuration(want)/5+time.Second {
			t.Errorf("经度 +%v°: 钟面偏移 %s, 期望约 %s (4 分钟/度, 东移变早)", d, delta, want)
		}
	}
}

// clockDelta 返回两个时刻的**钟面**差值 (去掉日历日), 用于跨"本地日"比较。
func clockDelta(a, b time.Time) time.Duration {
	secOfDay := func(t time.Time) time.Duration {
		return time.Duration(t.Hour())*time.Hour +
			time.Duration(t.Minute())*time.Minute +
			time.Duration(t.Second())*time.Second +
			time.Duration(t.Nanosecond())
	}
	return secOfDay(a) - secOfDay(b)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestInvalidInputFailClosed 非法输入必须 fail-closed (选型 §5.3 / §6.2):
// 返回错误且**返回零值时间**, 绝不允许静默按 0 处理。
func TestInvalidInputFailClosed(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	date := mustParseLocal(t, "2026-03-20", tz)
	cases := []struct {
		name     string
		lat, lon float64
		tz       *time.Location
		kind     Kind
		wantErr  error
	}{
		{"lat>90", 90.0001, 0, tz, Sunrise, ErrLatitudeRange},
		{"lat<-90", -90.0001, 0, tz, Sunrise, ErrLatitudeRange},
		{"lon>180", 0, 180.0001, tz, Sunrise, ErrLongitudeRange},
		{"lon<-180", 0, -180.0001, tz, Sunrise, ErrLongitudeRange},
		{"nil tz", 39.9, 116.4, nil, Sunrise, ErrNilLocation},
		{"kind 空", 39.9, 116.4, tz, "", ErrUnknownKind},
		{"kind 未知", 39.9, 116.4, tz, Kind("noon"), ErrUnknownKind},
		{"lat NaN", math.NaN(), 0, tz, Sunrise, ErrLatitudeRange},
		{"lon Inf", 0, math.Inf(1), tz, Sunrise, ErrLongitudeRange},
	}
	for _, c := range cases {
		at, status, err := SunEvent(c.lat, c.lon, c.tz, date, c.kind)
		if err == nil {
			t.Errorf("%s: 必须报错 (fail-closed), 却成功了 (status=%s at=%v)", c.name, status, at)
			continue
		}
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s: 错误类型不符, got %v want %v", c.name, err, c.wantErr)
		}
		if !at.IsZero() {
			t.Errorf("%s: 报错时必须返回零值时间, got %v", c.name, at)
		}
		if status != "" {
			t.Errorf("%s: 报错时状态必须为空, got %q", c.name, status)
		}
	}
	// 边界值是**合法**的 (含端点), 不能被误拒。
	for _, c := range []struct {
		name     string
		lat, lon float64
	}{
		{"lat=90", 90, 0}, {"lat=-90", -90, 0}, {"lon=180", 0, 180}, {"lon=-180", 0, -180},
	} {
		if _, _, err := SunEvent(c.lat, c.lon, tz, date, Sunrise); err != nil {
			t.Errorf("%s: 边界值应合法, got %v", c.name, err)
		}
	}
}

// TestKindValid 守卫: Valid() 的取值集合必须与常量一致 (防新增 kind 漏改)。
func TestKindValid(t *testing.T) {
	if !Sunrise.Valid() || !Sunset.Valid() {
		t.Fatal("Sunrise/Sunset 必须有效")
	}
	for _, k := range []Kind{"", "Sunrise", "SUNSET", "elevation", "noon"} {
		if k.Valid() {
			t.Errorf("kind %q 不应有效 (大小写敏感, fail-closed)", k)
		}
	}
}

// ── 测试辅助 ───────────────────────────────────────────────────────────

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func mustParseLocal(t *testing.T, date string, tz *time.Location) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", date, tz)
	if err != nil {
		t.Fatalf("解析日期 %q: %v", date, err)
	}
	return d
}

// mustClock 把 "HH:MM:SS" 组装成 tz 下该日期的时刻。
func mustClock(t *testing.T, date, clock string, tz *time.Location) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+clock, tz)
	if err != nil {
		t.Fatalf("解析时刻 %q %q: %v", date, clock, err)
	}
	return d
}

// TestDayAttributionForMismatchedCoordinates 钉死"经度/时区错配"下的归日**完备性**。
//
// 为什么必须单独覆盖: 常规配置下归日几乎第一次命中, 归日的候选枚举被完全短路。
// 统筹方的变异测试把候选顺序改成单向 +1..+4 后全绿 —— 暴露的正是这个覆盖盲区。
// 本用例给经度与 IANA 时区**故意错配**的极端组合, 并**用独立判据断言本日确实
// 存在穿越** (而不只是断言"返回的时刻落在当日"), 逼迫归日路径真正工作:
// 单向搜索会漏掉需要 -1 偏移的解 ⇒ 返回 always_up/down 而判据说本日有事件 ⇒ 必红。
//
// 实测偏移使用分布 (lats×lons×tzs 全扫描, 54097 个在窗解):
//
//	off=0 → 32891, off=-1 → 12038, off=+1 → 9152, off=-2 → 16
//
// ⇒ -1/+1 都是必需候选, 单向搜索必然漏解。
func TestDayAttributionForMismatchedCoordinates(t *testing.T) {
	cases := []struct {
		name string
		lat  float64
		lon  float64
		tz   string
	}{
		{"东经 179.9 配 UTC", -16.5, 179.9, "UTC"},
		{"西经 179.9 配 UTC", 20.0, -179.9, "UTC"},
		{"东经 179.9 配 Asia/Shanghai", -16.5, 179.9, "Asia/Shanghai"},
		{"西经 120 配 Asia/Shanghai", 39.9, -120.0, "Asia/Shanghai"},
		{"格林尼治配 Pacific/Kiritimati", 51.48, 0.0, "Pacific/Kiritimati"},
		{"高危纬度 + 错配时区", 65.0, 179.0, "Pacific/Kiritimati"},
		{"高危纬度 + 错配时区(西)", -65.0, -179.0, "Pacific/Midway"},
	}
	days := []string{"2026-01-15", "2026-03-20", "2026-06-21", "2026-09-23", "2026-12-21"}
	const tol = 2 * time.Minute
	checked, noEvent := 0, 0
	for _, c := range cases {
		tz := mustLoad(t, c.tz)
		for _, ds := range days {
			date := mustParseLocal(t, ds, tz)
			for _, kind := range []Kind{Sunrise, Sunset} {
				got, status, err := SunEvent(c.lat, c.lon, tz, date, kind)
				if err != nil {
					t.Fatalf("%s %s/%s: %v", c.name, ds, kind, err)
				}
				// 独立判据: 本本地日内是否真有穿越?
				wantAt, hasEvent := refInDayEvent(c.lat, c.lon, tz, date, kind)
				if hasEvent {
					checked++
					if status != StatusOK {
						t.Fatalf("%s %s/%s: 独立判据求出本日事件 %s, 实现却返回 %s —— "+
							"归日候选搜索不完备 (只搜单向会漏掉 -1/-2 偏移的解)",
							c.name, ds, kind, wantAt.In(tz).Format("15:04:05"), status)
					}
					if delta := got.Sub(wantAt); delta > tol || delta < -tol {
						t.Fatalf("%s %s/%s: 实现 %s 与独立判据 %s 相差 %s (>%s)",
							c.name, ds, kind, got.In(tz).Format("15:04:05"), wantAt.In(tz).Format("15:04:05"), delta, tol)
					}
					if ld := got.In(tz).Format("2006-01-02"); ld != ds {
						t.Fatalf("%s %s/%s: 归日错误 —— 返回 %s (%s), 必须落在请求的本地日 %s",
							c.name, ds, kind, ld, got.In(tz).Format("15:04:05"), ds)
					}
					continue
				}
				noEvent++
				if status == StatusOK {
					mid := got.In(tz)
					dayStart := time.Date(mid.Year(), mid.Month(), mid.Day(), 0, 0, 0, 0, tz)
					off := mid.Sub(dayStart)
					if off > edgeBandMinutes*time.Minute && off < 24*time.Hour-edgeBandMinutes*time.Minute {
						t.Fatalf("%s %s/%s: 判据无穿越, 实现返回 %v (离日界 %s) —— 归日不一致",
							c.name, ds, kind, mid.Format("15:04:05"), off)
					}
				}
				if !got.IsZero() && status != StatusOK {
					t.Fatalf("%s %s/%s: 无事件却返回时刻 %v", c.name, ds, kind, got)
				}
			}
		}
	}
	if checked < 30 {
		t.Fatalf("只覆盖 %d 个有事件样本 (无事件 %d), 期望 >=30 —— 门禁空转", checked, noEvent)
	}
	t.Logf("错配坐标归日覆盖: 有事件 %d, 无事件 %d", checked, noEvent)
}

// TestPolarBandsStructurallyPresent 是**结构性**判据 (不依赖任何公式复算):
// 85°N 在 1 月必须存在 always_down、在 6 月必须存在 always_up;
