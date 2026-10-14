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
