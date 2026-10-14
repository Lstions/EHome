package sun

import (
	"testing"
	"time"
)

// TestCacheKeyIncludesAllFourFields 是选型文档 §5.4 的可失败要求:
// 改 (lat, lon, tzName, localDate) 任一项都必须**重算** (调用计数 +1)。
//
// 反向验证: 把 CacheKey 里的任一项去掉 → 对应子用例的计数不变 → 本测试红。
func TestCacheKeyIncludesAllFourFields(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	tzOther := mustLoad(t, "Asia/Urumqi") // 同为 UTC+8 但**是不同时区名**
	day := mustParseLocal(t, "2026-03-20", tz)

	newCounting := func() (*Cache, *int64) {
		var n int64
		c := NewCacheWithCompute(func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
			n++
			return SunEvent(lat, lon, tz, date, kind)
		})
		return c, &n
	}

	t.Run("命中不重算", func(t *testing.T) {
		c, n := newCounting()
		k := CacheKey{Lat: 39.9042, Lon: 116.4074, TZ: tz.String(), Date: "2026-03-20", Kind: Sunrise}
		if _, _, err := c.Lookup(k.Lat, k.Lon, tz, day, k.Kind); err != nil {
			t.Fatal(err)
		}
		before := *n
		// 再查两次: 必须全部命中缓存, 计算次数不变。
		for i := 0; i < 2; i++ {
			if _, _, err := c.Lookup(k.Lat, k.Lon, tz, day, k.Kind); err != nil {
				t.Fatal(err)
			}
		}
		if *n != before {
			t.Errorf("命中缓存却重算了: calls %d → %d", before, *n)
		}
	})

	cases := []struct {
		name string
		lat  float64
		lon  float64
		tz   *time.Location
		day  string
		kind Kind
	}{
		{"纬度变化", 39.9043, 116.4074, tz, "2026-03-20", Sunrise},
		{"经度变化", 39.9042, 116.4075, tz, "2026-03-20", Sunrise},
		{"时区名变化", 39.9042, 116.4074, tzOther, "2026-03-20", Sunrise},
		// 用 day+2 而不是 day+1: day+1 会被"次日预计算"合法地填进缓存
		// (方案 §3.1.3 的硬要求), 用它就区分不出"日期在键里"还是"预热命中"。
		{"本地日期变化", 39.9042, 116.4074, tz, "2026-03-22", Sunrise},
		{"事件种类变化", 39.9042, 116.4074, tz, "2026-03-20", Sunset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, n := newCounting()
			base := mustParseLocal(t, "2026-03-20", tz)
			if _, _, err := c.Lookup(39.9042, 116.4074, tz, base, Sunrise); err != nil {
				t.Fatal(err)
			}
			before := *n
			day := mustParseLocal(t, tc.day, tz)
			if _, _, err := c.Lookup(tc.lat, tc.lon, tc.tz, day, tc.kind); err != nil {
				t.Fatal(err)
			}
			if *n <= before {
				t.Errorf("%s: 未重算 (calls %d → %d) —— 缓存键必须含 (lat,lon,tzName,localDate,kind) 全部五项",
					tc.name, before, *n)
			}
		})
	}
}

// TestCacheDateDimensionHonoured 是"日期必须参与键"的**语义**断言 (不止计数):
// 同坐标相邻两日必须得到**不同**的时刻, 且各自等于直接计算值。
//
// 为什么补这条: 计数断言只能证明"重算了", 不能证明"算的是所请求的那一天"。
// 若实现把日期漏出键外却恰好因预热而重算, 计数会绿而结果错日 —— 这条把它抓住。
func TestCacheDateDimensionHonoured(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	day1 := mustParseLocal(t, "2026-06-21", tz) // 夏至: 相邻日时刻差异最明显
	day2 := day1.AddDate(0, 0, 1)
	c := NewCache()

	at1, st1, err := c.Lookup(39.9042, 116.4074, tz, day1, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	at2, st2, err := c.Lookup(39.9042, 116.4074, tz, day2, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	if st1 != StatusOK || st2 != StatusOK {
		t.Fatalf("状态异常: %s / %s", st1, st2)
	}
	want1, _, _ := SunEvent(39.9042, 116.4074, tz, day1, Sunrise)
	want2, _, _ := SunEvent(39.9042, 116.4074, tz, day2, Sunrise)
	if !at1.Equal(want1) {
		t.Errorf("day1 缓存值 %v != 直接计算 %v", at1, want1)
	}
	if !at2.Equal(want2) {
		t.Errorf("day2 缓存值 %v != 直接计算 %v", at2, want2)
	}
	if at1.Equal(at2) {
		t.Error("相邻两日返回了同一时刻 —— 日期未参与缓存键")
	}
	// 归日正确性: 各自落在各自的本地日。
	if got := at1.In(tz).Format("2006-01-02"); got != "2026-06-21" {
		t.Errorf("day1 归日错误: %s", got)
	}
	if got := at2.In(tz).Format("2006-01-02"); got != "2026-06-22" {
		t.Errorf("day2 归日错误: %s", got)
	}
}

// TestCacheSameTZOffsetDifferentNameIsDifferentKey 单独钉死"时区名"这一维:
// Asia/Shanghai 与 Asia/Urumqi 当前 UTC 偏移相同, 但**不是同一个时区**。
// 若缓存键用偏移而不是名字, 这两者会互相串用 (选型文档 §4: 必须用 IANA 名)。
func TestCacheSameTZOffsetDifferentNameIsDifferentKey(t *testing.T) {
	sh := mustLoad(t, "Asia/Shanghai")
	ur := mustLoad(t, "Asia/Urumqi")
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, sh)
	_, off1 := now.In(sh).Zone()
	_, off2 := now.In(ur).Zone()
	if off1 != off2 {
		t.Skipf("前提不成立: 两个时区当前偏移已不同 (%d vs %d)", off1, off2)
	}
	var n int64
	c := NewCacheWithCompute(func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
		n++
		return SunEvent(lat, lon, tz, date, kind)
	})
	if _, _, err := c.Lookup(43.83, 87.62, sh, now, Sunset); err != nil {
		t.Fatal(err)
	}
	before := n
	if _, _, err := c.Lookup(43.83, 87.62, ur, now, Sunset); err != nil {
		t.Fatal(err)
	}
	if n == before {
		t.Fatal("同为 UTC+8 但不同 IANA 名必须视为不同缓存键 —— 键不得用偏移")
	}
}

// TestCachePrecomputesNextDay 钉死方案 §3.1.3 的"次日预计算":
// 查询当日之后, 次日的条目必须已在缓存里 (午夜后首个 tick 无空窗)。
func TestCachePrecomputesNextDay(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	day := mustParseLocal(t, "2026-03-20", tz)
	var n int64
	c := NewCacheWithCompute(func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
		n++
		return SunEvent(lat, lon, tz, date, kind)
	})
	if _, _, err := c.Lookup(39.9042, 116.4074, tz, day, Sunrise); err != nil {
		t.Fatal(err)
	}
	callsAfterToday := n
	// 次日: 必须直接命中 (不增加计算次数)。
	next := day.AddDate(0, 0, 1)
	at, status, err := c.Lookup(39.9042, 116.4074, tz, next, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusOK || at.IsZero() {
		t.Fatalf("次日结果无效: %s %v", status, at)
	}
	if n != callsAfterToday {
		t.Errorf("次日未预计算: 查询次日又算了 %d 次", n-callsAfterToday)
	}
	// 数值正确性: 预计算的次日值必须与直接计算一致。
	want, wantStatus, err := SunEvent(39.9042, 116.4074, tz, next, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(want) || status != wantStatus {
		t.Errorf("预计算值与直接计算不一致: %v/%s vs %v/%s", at, status, want, wantStatus)
	}
}

// TestCachePrunesOldDates 钉死缓存规模上界 (方案 §3.1.3: 内存 ≈ 常数级)。
// 日期在键里 ⇒ 必须淘汰; 否则长期运行无界增长。
func TestCachePrunesOldDates(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	var n int64
	c := NewCacheWithCompute(func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
		n++
		return SunEvent(lat, lon, tz, date, kind)
	})
	start := mustParseLocal(t, "2026-03-20", tz)
	// 连续查 60 天, 每天两次 (日出+日落)。若无淘汰, 条目会到 120+。
	for d := 0; d < 60; d++ {
		day := start.AddDate(0, 0, d)
		for _, k := range []Kind{Sunrise, Sunset} {
			if _, _, err := c.Lookup(39.9042, 116.4074, tz, day, k); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 只允许留 today(60) + tomorrow(61) 两日 × 2 事件 = 4 条 (末次查询的日期为基准)。
	if got := c.Len(); got > 4 {
		t.Errorf("缓存条目 %d > 4 —— 日期淘汰失效, 长期运行会无界增长 (方案 §3.1.3)", got)
	}
	if c.Len() == 0 {
		t.Error("缓存被清空 —— 淘汰过度, 当日/次日条目必须保留")
	}
}

// TestCacheDoesNotCacheErrors 钉死"错误不入缓存":
// 一次瞬时非法输入不得被长期化 (否则修正配置后仍一直失败)。
func TestCacheDoesNotCacheErrors(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	day := mustParseLocal(t, "2026-03-20", tz)
	c := NewCache()
	if _, _, err := c.Lookup(999, 116.4, tz, day, Sunrise); err == nil {
		t.Fatal("非法纬度必须报错")
	}
	if c.Len() != 0 {
		t.Errorf("错误结果被缓存了 (%d 条) —— 非法输入不得入缓存", c.Len())
	}
	if _, _, err := c.Lookup(39.9042, 116.4074, nil, day, Sunrise); err == nil {
		t.Fatal("nil tz 必须报错")
	}
	if c.Len() != 0 {
		t.Errorf("错误结果被缓存了 (%d 条)", c.Len())
	}
}

// TestCachePolarStatusCached 极昼/极夜的状态也必须被缓存 (不能每 tick 重算),
// 且缓存里**不存时刻** (零值), 与 SunEvent 的约定一致。
func TestCachePolarStatusCached(t *testing.T) {
	tz := mustLoad(t, "Arctic/Longyearbyen")
	day := mustParseLocal(t, "2026-12-21", tz)
	var n int64
	c := NewCacheWithCompute(func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error) {
		n++
		return SunEvent(lat, lon, tz, date, kind)
	})
	at, status, err := c.Lookup(78.22, 15.65, tz, day, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusAlwaysDown {
		t.Fatalf("status=%s want always_down", status)
	}
	if !at.IsZero() {
		t.Fatalf("极夜不得返回时刻, got %v", at)
	}
	before := n
	at2, status2, err := c.Lookup(78.22, 15.65, tz, day, Sunrise)
	if err != nil {
		t.Fatal(err)
	}
	if n != before {
		t.Error("极昼/极夜状态未命中缓存 —— 每 tick 重算")
	}
	if !at2.IsZero() || status2 != StatusAlwaysDown {
		t.Errorf("缓存返回值不一致: %v/%s", at2, status2)
	}
}

// TestCacheInvalidate 配置大范围变更时的整体失效。
func TestCacheInvalidate(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	day := mustParseLocal(t, "2026-03-20", tz)
	c := NewCache()
	if _, _, err := c.Lookup(39.9042, 116.4074, tz, day, Sunrise); err != nil {
		t.Fatal(err)
	}
	if c.Len() == 0 {
		t.Fatal("查询后缓存应非空")
	}
	c.Invalidate()
	if c.Len() != 0 {
		t.Errorf("Invalidate 后仍剩 %d 条", c.Len())
	}
}

// TestCacheConcurrent 并发安全 (求值器多 goroutine 共用同一缓存)。
// go test -race 下若有数据竞争会直接失败。
func TestCacheConcurrent(t *testing.T) {
	tz := mustLoad(t, "Asia/Shanghai")
	day := mustParseLocal(t, "2026-03-20", tz)
	c := NewCache()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				lat := 39.9042 + float64(i)*0.01
				if _, _, err := c.Lookup(lat, 116.4074, tz, day, Sunrise); err != nil {
					t.Errorf("并发 Lookup 失败: %v", err)
					return
				}
				c.Len()
				c.ComputeCalls()
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
