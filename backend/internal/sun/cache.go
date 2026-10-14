package sun

import (
	"sync"
	"time"
)

// CacheKey 是日出日落缓存的**完整**键。
//
// ⚠️ 四项缺一不可 (选型文档 §5.4 的可失败要求): 少了任一字段都会让"改了配置
// 却仍用旧时刻"成为静默错误 —— 换坐标后按旧经度触发 BMS 动作是安全相关的。
// 具体:
//   - Lat/Lon: 坐标不同 ⇒ 时刻不同 (地球自转/纬度决定的昼长);
//   - TZ: 同一物理时刻在不同时区的**本地钟面值**不同, 而规则用本地日归日;
//   - Date: 同一坐标在不同日期的时刻不同 (太阳赤纬随季节变化);
//   - Kind: 日出与日落是同日两个不同的时刻。
//
// 刻意**不含** offset: 偏移是规则级参数, 由求值器在取出原始事件时刻后叠加。
// 把它放进键会让"同坐标不同偏移"的 N 条规则各占一份缓存, 而原始事件时刻完全相同。
type CacheKey struct {
	Lat  float64
	Lon  float64
	TZ   string
	Date string // 节点本地日期 "2006-01-02"
	Kind Kind
}

// CacheEntry 是一次计算的缓存结果。
//
// At 在 Status != StatusOK 时为零值 —— 缓存也不编造时刻 (与 SunEvent 同约定)。
type CacheEntry struct {
	At     time.Time
	Status EventStatus
}

// computeFunc 是可注入的计算函数 (默认 SunEvent)。
type computeFunc func(lat, lon float64, tz *time.Location, date time.Time, kind Kind) (time.Time, EventStatus, error)

// Cache 是日出日落结果的进程内缓存。
//
// 规模上界 (方案 §3.1.3 的结构性推导): 活跃条目 = 坐标组合 × 2 日 × 2 事件。
// localDate 进入键意味着条目会跨日累积, 故 Lookup 在检测到日期推进时自动
// 淘汰旧日期 (见 pruneLocked) —— 否则长期运行会无界增长, 使"内存 ≈ 常数级"
// 的结论失效。这是本实现相对方案推导必须补的一点。
//
// 并发: 内部互斥锁保护; 计算在锁外进行 (同 evaluator 的 H2 教训: 持锁做
// 慢操作会阻塞所有求值)。
type Cache struct {
	mu      sync.Mutex
	entries map[CacheKey]CacheEntry
	compute computeFunc
	calls   int64 // 实际落到计算函数的次数 (观测用; 测试据此断言"改任一项必须重算")
}

// NewCache 构造使用真实天文计算的缓存。
func NewCache() *Cache { return NewCacheWithCompute(SunEvent) }

// NewCacheWithCompute 构造使用自定义计算的缓存 (测试注入计数函数用)。
func NewCacheWithCompute(fn computeFunc) *Cache {
	if fn == nil {
		fn = SunEvent
	}
	return &Cache{entries: make(map[CacheKey]CacheEntry), compute: fn}
}

// ComputeCalls 返回本缓存真正调用计算函数的次数。
//
// 为什么生产代码保留它: 它是"缓存是否真的在生效"的唯一可观测信号 —— 每次
// tick 的调用次数应当远小于规则数。同时 A-18 的可失败断言靠它 (改坐标后
// 必须重算 ⇒ 计数 +1)。
func (c *Cache) ComputeCalls() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Len 返回当前缓存条目数 (用于断言规模上界与淘汰生效)。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Lookup 返回 (lat,lon,tz) 在 localDate 当日的 kind 事件, 命中缓存则直接返回。
//
// 未命中时计算当日事件, **并顺带预计算次日** (方案 §3.1.3): 午夜后的首个 tick
// 若赶上日期翻转, 次日条目已在缓存里, 不会出现"无事件可判"的空窗。预计算的
// 代价是一次纯函数调用, 无 I/O。
//
// 返回 error 只在非法输入时出现 (由 SunEvent 判定), 且**不会**被缓存 ——
// 缓存错误会让一次瞬时配置错误长期化。
func (c *Cache) Lookup(lat, lon float64, tz *time.Location, localDate time.Time, kind Kind) (time.Time, EventStatus, error) {
	if tz == nil {
		return time.Time{}, "", ErrNilLocation
	}
	day := localDate.In(tz)
	key := CacheKey{
		Lat:  lat,
		Lon:  lon,
		TZ:   tz.String(),
		Date: day.Format("2006-01-02"),
		Kind: kind,
	}

	if e, ok := c.get(key); ok {
		return e.At, e.Status, nil
	}

	// 锁外计算: 先算当日 (返回值给调用方), 再算次日 (纯预热)。
	at, status, err := c.compute(lat, lon, tz, day, kind)
	if err != nil {
		return time.Time{}, "", err
	}
	nextDay := day.AddDate(0, 0, 1)
	nextAt, nextStatus, nextErr := c.compute(lat, lon, tz, nextDay, kind)
	if nextErr != nil {
		// 次日预计算失败不影响当日结果 (例如极区边界日的参数化边界),
		// 只跳过预热 —— 届时该日期到来会重新计算并如实返回错误。
		nextAt, nextStatus = time.Time{}, ""
	}

	c.mu.Lock()
	c.calls++ // 与上面对 compute 的两次调用对应的是"被计入的未命中次数", 见下
	c.entries[key] = CacheEntry{At: at, Status: status}
	if nextStatus != "" {
		c.entries[CacheKey{Lat: lat, Lon: lon, TZ: tz.String(),
			Date: nextDay.Format("2006-01-02"), Kind: kind}] = CacheEntry{At: nextAt, Status: nextStatus}
	}
	c.pruneLocked(key.Date)
	c.mu.Unlock()
	return at, status, nil
}

// get 读缓存。命中即返回。
func (c *Cache) get(key CacheKey) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return e, ok
}

// pruneLocked 淘汰与当前日期不相邻的条目 (只留 today 与 tomorrow)。
//
// 为什么必须在缓存内部做: localDate 在键里 ⇒ 条目随日期累积。若不淘汰,
// 一个跑一年的进程会保留 365 × 坐标 × 2 事件 条, 与方案 §3.1.3 的
// "内存为常数级"结论直接矛盾。只留两日是**充分**的: Lookup 永远只查当日
// (并预热次日), 更早的日期不会再被访问。
func (c *Cache) pruneLocked(today string) {
	for k := range c.entries {
		if k.Date != today && k.Date != nextDateString(today) {
			delete(c.entries, k)
		}
	}
}

// nextDateString 返回 "YYYY-MM-DD" 的次日。解析失败时返回原值 (不淘汰, 保守)。
func nextDateString(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02")
}

// Invalidate 清空全部条目 (配置变更时调用)。
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.entries = make(map[CacheKey]CacheEntry)
	c.mu.Unlock()
}
