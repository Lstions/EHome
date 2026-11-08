package api

// device_data_sensors_frames.go — 用**帧表**合成 device_data 列表行的 sensors（P0 回归修复）。
//
// ============ 为什么必须改（2026-11-07 生产回归）============
//
// 原实现（device_data_sensors.go 的 latestSensorViewsAt + deltas）从 unified_data 合成，
// 而 10 月的 unified_data 分区已 DETACH/删除。生产实测：
//
//	GET /edge-devices/14/data?start_time=2026-10-20T00:00:00Z&end_time=2026-10-20T00:10:00Z
//	  → data_json 键 = [channel_id, raw_hex, timestamp]，sensors 数 = 0   ← 回归
//	GET .../data?start_time=2026-11-06...（11 月还在）
//	  → sensors 数 = 29                                                   ← 正常
//
// 前端 edgeDevice.ts:794 / DataPanel.vue 的「解析数据」列直接消费它 ⇒ 用户可见。
//
// ============ 为什么帧表是对的数据源 ============
//
// 1. **长期存储**：帧表是 L2 主写入路径，10 月 2,567,920 帧完整，不随 unified_data 退役；
// 2. **全字段**：帧是「该 slot 的全部字段」，而不是 L1 收窄后的「只写变化字段」。
//    原实现之所以要 per-sensor MAX(timestamp)，正是因为 unified_data 被 L1 收窄了；
//    帧表没有这个问题，**一次解码就能拿到完整快照**；
// 3. **一次解码服务整页**：原实现是「基线 GROUP BY + 区间增量」两条查询，虽然也是 O(1)
//    次查询，但那条基线查询在千万行设备上要 651 ms（用不到索引）。解码整页只要一次。
//
// ============ 三个必须处理的点（Lead 2026-11-07 指定）============
//
//	① **回落**：帧表不可用（字典 nil / 解码失败 / 该时段无帧 / 窗口超限）⇒ 调用方
//	   回落原有 unified_data 逻辑。两条路径的 sensors 必须逐字段相等（PG 用例对拍）。
//	② **边界**：本行时刻之前最后一个帧可能在页首之前 ⇒ 解码窗口向前扩
//	   sensorFrameLookback 找基准帧，否则每页第一行的 sensors 会是空的。
//	③ **性能**：< 50 ms / 50 行页（用例钉住）。
//
// ============ 一个必须说清的差异：帧表没有 Unit 列 ============
//
// telemetry_frame 只存 (device_id, ts, seq, frame_kind, slot_id, payload) —— **没有单位**。
// 帧里解出来的是 (name, value)；单位只存在于 driver/parser 层（写进 unified_data.unit）。
//
// 所以帧表路径的单位从「该设备在存活 unified_data 里的 sensor→unit 映射」补齐
// （一条 DISTINCT ON 查询 + 进程内缓存）：
//
//	· 过渡期（11 月还在）⇒ 单位与 unified_data 路径**逐字段相等**，对拍用例能过；
//	· unified_data 全部退役后 ⇒ 单位退化为空串，**值仍然正确**（前端只是少个单位后缀）。
//
// 这是刻意接受的降级：把单位搬进帧表需要改编码格式（payload 加字节），代价远大于收益。

import (
	"context"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/telemetry"
)

// sensorFrameLookback 是页首之前的**基准回看窗口**。
//
// 为什么需要：as-of 语义要求「本行时刻之前最后一个帧」，而每页第一行恰好是页首，
// 它之前的那一帧可能落在解码窗口之外。向前扩一段换取基准。
//
// 取 10 分钟的依据：帧是**事件驱动**的（每解析成功一帧就编码一条），不是心跳式抽样。
// 生产实测（ehome_review_bench，device14/15）帧间隔 p50 = 1.0 s、p95 <= 10 s；
// 10 分钟约为 p95 的 60 倍，足以覆盖任何在采设备的「上一帧」。
//
// 注意它小于 device_data_sensors.go 里 listBaselineLookback（2h）是**有意的**：
// 那边的 2h 是为了覆盖「某字段最长 1h 才写一次」（L1 收窄 + 命令轮询间隔）；
// 而解码器有**关键帧链**：DecodeRangeAnchored 会自动把起点前移到关键帧，
// 因此即使某字段 1h 才出现一次，只要它在解码窗口内出现过一次，状态就会延续。
// 10 分钟只是「页首之前的即时基准」，不是「字段出现周期」。
const sensorFrameLookback = 10 * time.Minute

// ============ 保留的历史论证：为什么帧表快照不需要「回看窗口」（2026-11-08）============
//
// 注：2026-11-08 硬切换后，原来的「2h 基线 + 区间增量」实现已删除。
// 这套论证保留是因为它解释了「为什么帧表快照不需要经验窗口」——
// frameSnapshot 是累积 as-of 状态，不受轮询间隔影响。
// 原实现是 latestSensorViewsAt（已删）。以下推导对未来任何 as-of 查询都适用。
//
// listBaselineLookback 是列表 as-of 基线查询的**回看窗口**。
//
// ============ 为什么必须有下界（性能，不是正确性）============
//
// 无下界的 "GROUP BY sensor_name, MAX(timestamp)" 在 PG 上用不到索引：规划器会选
// Parallel Seq Scan + HashAggregate。实测（本机 PG 18，ehome_review_bench2 里
// device_id=14 是 1,276 万行 / 5 天）：
//
// 实测（本机 PG 18，ehome_review_bench2，device_id=14 是 1,276 万行 / 5 天，
// 锚点取该设备最后一次上报时刻）：见下方"实测回看窗口代价"表。
//
// ============ 为什么回看窗口是「2 × 最大轮询间隔」（正确性论证）============
//
// 基线查询的锚点是 **earliest**（本页最早那一行的时刻，不是 now）——那一刻既然
// 存在一条 device_data 行，就说明解析成功、设备正在报数。
//
// 设备在报数 ⇒ 它的 enabled 命令都在按 interval 轮询，而**命令 interval 的上限是
// 3,600,000 ms = 1 小时**（handler_edge_device.go:1261 的入参校验：
// "interval_ms must be between 0 and 3600000"；interval=0 的命令根本不轮询，
// 其物理量本来就不该出现在面板上）。所以任何在采物理量，两次写入的间隔
// **不可能超过 1 小时** ⇒ 2 小时窗口保证每个物理量至少被看到一次，且有 2 倍余量。
//
// ⚠ 不能用「L1 心跳 60s」来论证更短的窗口。心跳是逐 (设备, 字段) 的，而写入收窄
// 只对**本帧携带的字段**求值（telemetry_write_gate.go 的 filter 收到的是这一帧的
// records）。device15 的 7 种帧类型轮询时，只在 read_cell_voltage 里出现的物理量，
// 其写入周期跟那条命令的 interval 走（最长 1h），不是 60s。
//
// 实测回看窗口代价（同一设备同一锚点，结果恒为 29 个传感器 —— 窗口大小不影响正确性，
// 只影响扫描量）：
//
//	窗口   5 min  :   7.9 ms
//	窗口  10 min  :   8.5 ms
//	窗口    1 h   :  18.4 ms
//	窗口    2 h   :  40.8 ms   ← 采用（2 × 最大 interval，正确性有下界保证）
//	窗口    6 h   :  74.7 ms
//	窗口   24 h   : 197.0 ms
//	无下界        : 651.0 ms（Parallel Seq Scan + HashAggregate，用不到索引）
//
// 取 2h 而不是无界：后者在 1,276 万行设备上要 651 ms，而数据面板每翻一页都要付一次。
// frameSnapshot 是某一帧时刻的**累积 as-of 状态**（不是该帧自己的字段）。
//
// 为什么要累积：device15 有 7 种帧类型，单帧只带自己 slot 的字段。
// 只取单帧字段会让面板上不同行显示不同子集（改造前是固定 29 项）。
type frameSnapshot struct {
	TS     time.Time
	Values map[string]float64
}

// latestSensorSnapshotsFromFrames 解码 [from-lookback, to) 并累积出逐帧 as-of 快照。
//
// 返回 ok=false 表示**帧表路径不可用**，调用方必须回落 unified_data：
//
//	· 字典未注入/不可用（无法还原字段名与量化规格）；
//	· 解码报错（表缺失、链式损坏、窗口超 MaxDecodeWindow、帧数超 MaxDecodeFrames）；
//	· 该时段一个点都没有。
//
// 注意：「解出 0 个点」在这里不算故障（与 rollup 尾巴同一裁决）——该时段可能本来
// 就没有帧。返回 ok=false 让调用方回落，是比返回空 sensors 更诚实的选择。
func latestSensorSnapshotsFromFrames(db *gorm.DB, deviceID uint, from, to time.Time) ([]frameSnapshot, bool) {
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, false
	}
	// ============ 为什么用 Bounded 而不是 DecodeRangeAnchored（2026-11-07）============
	//
	// DecodeRangeAnchored 内部套的是**生产护栏** MaxDecodeWindow = 24h。而这里的解码窗口是
	// 「页内跨度 + 10 分钟 lookback」，**由用户请求的 page_size 和设备上报速率共同决定**，
	// 不是我们能控制的常量。套生产护栏会有一个数学上必然的失效阈值：
	//
	//	页内跨度 > 24h - 10min = 23.83h  ⇒  RangeTooLargeError  ⇒  ok=false  ⇒  回落
	//
	// 换算成设备速率（page_size 上限 200）：
	//
	//	 10 行/小时 → 200 行跨度 20.0h  OK
	//	  8 行/小时 → 200 行跨度 25.0h  **FAIL**（回落 ⇒ 10 月行仍然丢 sensors）
	//	  5 行/小时 → 200 行跨度 40.0h  FAIL
	//	  1 行/小时 → 200 行跨度 200h   FAIL
	//
	// 现状之所以看起来没事，只是因为 device14/15 是 67K/3.5K 行每小时。**接入一台
	// 5 分钟一报的温湿度传感器就会踩。** 这是潜伏 bug，不是理论洁癖。
	//
	// decode_anchored.go 的注释明确要求「需要放宽护栏的场景请显式用 Bounded，别改常量」——
	// 这里就是那个场景。**maxFrames 仍用生产值**，所以「帧数炸弹」照样被挡住；
	// 放宽的只有时间跨度，而时间跨度是请求参数，不是攻击面。
	//
	// ⚠ 后人若「顺手统一」改回 DecodeRangeAnchored，TestDeviceDataSensors_LongPageSpanStillUsesFrames
	//   会立刻红。
	window := to.Sub(from) + sensorFrameLookback
	points, _, err := telemetry.DecodeRangeAnchoredBounded(
		context.Background(), db, dict, deviceID, from.Add(-sensorFrameLookback), to, nil,
		// 比实际窗口宽一倍即可（护栏是"不超过"语义，多给一倍不影响判定）。
		window*2,
		telemetry.MaxDecodeFrames)
	if err != nil || len(points) == 0 {
		return nil, false
	}

	// DecodeFrames 保证按时间升序；按 TS 分组累积。
	out := make([]frameSnapshot, 0, 64)
	state := make(map[string]float64, 32)
	i := 0
	for i < len(points) {
		ts := points[i].TS
		for i < len(points) && points[i].TS.Equal(ts) {
			state[points[i].Name] = points[i].Value
			i++
		}
		snap := make(map[string]float64, len(state))
		for k, v := range state {
			snap[k] = v
		}
		out = append(out, frameSnapshot{TS: ts, Values: snap})
	}
	return out, true
}

// snapshotAt 返回最后一个 TS <= at 的快照下标；没有则 -1。
func snapshotAt(snaps []frameSnapshot, at time.Time) int {
	lo, hi := 0, len(snaps)
	for lo < hi {
		mid := (lo + hi) / 2
		if !snaps[mid].TS.After(at) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

// deviceUnitCache 缓存 sensor_name -> unit（帧表没有单位，只能从别处补）。
var (
	deviceUnitMu    sync.RWMutex
	deviceUnitCache = map[uint]map[string]string{}
)

// ResetDeviceUnitCache 清空单位缓存（测试用）。
func ResetDeviceUnitCache() {
	deviceUnitMu.Lock()
	deviceUnitCache = map[uint]map[string]string{}
	deviceUnitMu.Unlock()
}

// unitMapForDevice 返回该设备 sensor_name -> unit 的映射。
//
// ============ 两级来源（2026-11-07，boot-api 指出正解）============
//
// ① **维度表 sensor_field_obs**（长期来源，不随 unified_data 退役）：
//
//	它由 field_resolver 的 projector 从「每帧入库回调」投影写入（main.go 的
//	SetLatestSinkFn -> registry.Observe -> projector），**数据通路不经过 unified_data**。
//	它的 upsert 已带 CASE WHEN excluded.unit <> 空串 时保留旧值 的语义 ——
//	写它的人已经表达了「单位是静态属性，一次观测到就长期有效」。
//	主键 (device_id, sensor_name)，查询是主键前缀扫描，比下面的 DISTINCT ON 更便宜。
//
// ② **unified_data 过渡补全**：sensor_field_obs 只在 EHOME_FIELD_RESOLVER_ENABLED
//
//	打开时才被写入。若生产上是关的（或投影还没跑满），维度表会偏少 ⇒ 用仍存活的
//	unified_data 补缺口。unified_data 全退役后这一级自然变空，不影响 ①。
//
// 为什么可以跨时间取：单位是字段的**静态属性**，与时间无关，任取一行即可。
// 为什么缓存：单位一个月也不会变，而设备页是高频接口。
// unitMapForDevice 返回该设备 fields 中各字段的单位（sensor_field_obs 优先，unified_data 兜底）。
//
// ============ 生产事故（2026-11-08）：这里曾执行一条 33 秒的查询 ============
//
// 生产日志：
//
//	[devices-history] 慢请求 device=14 segments=5 decode=1.955s units=33.405s
//
// 根因不是「三层回落选错了层」，而是**第二层无条件执行**：
//
//	① sensor_field_obs（生产仅 71 行，微秒级）
//	② unified_data 的 DISTINCT ON（device14 1500 万行 + 索引 ASC vs 查询 DESC ⇒ 33 s）
//
// 原实现里 if !has 只决定**谁覆盖谁**，不决定**要不要查** —— 所以即使 ① 已覆盖全部字段，
// ② 照样跑。「三层回落」根本不是回落，是三段串行。
//
// ============ 修法：① 命中的字段一次 DB 都不查，② 只对缺失字段逐个 LIMIT 1 ============
//
// 为什么是「逐个 LIMIT 1」而不是「一条大 IN」：
//
//	索引是 (device_id, sensor_name, timestamp)
//	WHERE device_id=? AND sensor_name=? ORDER BY timestamp DESC LIMIT 1
//	  => 每个字段一次索引反向探测，~1ms，**与设备历史长度无关**
//	而「一条大 IN + DISTINCT ON」会退化成 33 秒那个形态；
//	「一条大 IN + LIMIT 1」只能拿到全局最近一条，不是每字段一条。
//
// 所以 29 个字段全缺也就是 ~30ms，且**不需要时间窗** ⇒ 离线很久的设备也不会丢单位。
//
// ⚠ fields 必须由调用方给出（本次真正需要的字段）。不提供「全量模式」：
//
//	要列出全量字段名就得扫 unified_data 的 DISTINCT sensor_name，那正是要避免的。
func unitMapForDevice(db *gorm.DB, deviceID uint, fields []string) map[string]string {
	if db == nil {
		return nil
	}
	// ============ 输入去重（2026-11-08，P0）============
	//
	// 调用方传进来的 fields **可能含大量重复**：device_data_sensors.go 的 snapNames
	// 是「逐个快照 × 逐个字段」append 出来的，一页 763 个快照 × 42 字段 = 32,046 项，
	// 其中同一字段出现 763 次。
	//
	// 而本函数下面两条路径都**按数组长度迭代发查询**，于是重复被放大成 N 倍：
	//
	//	· ① 的 IN 查询参数长度 = len(missing)
	//	· ② 的兜底循环对每个缺字段发一次查询
	//
	// 生产实测（2026-11-08，DROP unified_data 之后日志暴露）：单个字段被查 **763 次**，
	// 一次翻页产生 **12,140 次** DB 查询（约 2.4 秒纯浪费）。
	//
	// 另外（这一点常被误归类为「去重修的」，其实是空串入缓存修的）：缓存判定
	//
	//	if cached != nil && len(missing) == 0 { return cached }
	//
	// cached 是按字段名建的 map，而 missing 是从**含重复的 fields** 算出来的 ——
	// 即使所有字段都命中缓存，重复项也会让 len(missing) > 0 => 永远走全流程。
	// ============ 它与「空串入缓存」是两件独立的事，不要混为一谈 ============
	//
	//  · 空串入缓存（下面 ① 里的 got[r.SensorName] = r.Unit）修的是**正确性**：
	//    那 16 个字段进缓存 => missing 变空 => 兜底层不再跑 => 42P01 归零。
	//
	//  · 本函数（去重）修的是**性能**：snapNames 实测 12,208 项（763 快照 × 16 字段），
	//    missingUnitFields 每请求要遍历它**两遍**（缓存判定 + 查询参数），
	//    去重后 16 项 => 减少 99.9% 的遍历和 SQL 参数。
	//
	// 实测（2026-11-08，忠实复刻，打 3 次请求）：
	//
	//	HEAD（两者都没做）  IN 参数共 36,624   兜底 36,624 条 42P01
	//	仅空串入缓存        IN 参数共 12,208   兜底 0 条        ← 42P01 已归零
	//	仅输入去重          IN 参数共   48    兜底 48 条        ← 仍每次查 16 条
	//	两者都做            IN 参数共   16    兜底 0 条        ← 目标状态
	//
	// 也就是说：去重**不能**替代空串入缓存（42P01 仍会每次 16 条），
	//          空串入缓存也**不能**替代去重（性能仍差 763 倍）。
	//
	// 为什么在函数内部兜而不是修调用方：去重是**本函数的输入契约**的一部分，
	// 任何调用方传重复都会被放大成 N 倍查询 —— 这个风险应该在函数内部消除，
	// 而不是要求每个调用点都记得去重。
	fields = dedupFieldNames(fields)
	if len(fields) == 0 {
		return map[string]string{}
	}

	deviceUnitMu.RLock()
	cached := deviceUnitCache[deviceID]
	missing := missingUnitFields(cached, fields)
	deviceUnitMu.RUnlock()
	if cached != nil && len(missing) == 0 {
		return cached
	}

	type row struct {
		SensorName string
		Unit       string
	}
	got := make(map[string]string, len(missing))

	// ① 维度表：只查还缺的字段（71 行的表，IN 查询微秒级）。
	var obs []row
	if err := db.Raw(
		"SELECT sensor_name, unit FROM sensor_field_obs WHERE device_id = ? AND sensor_name IN ?",
		deviceID, missing).Scan(&obs).Error; err == nil {
		for _, r := range obs {
			// ============ 空串也要写入（2026-11-08 修）============
			//
			// 空 unit 表示「**观测过，但这个字段本来就没有单位**」（典型：alarm_* 系列、
			// status 类物理量）。原实现是 `if r.Unit != "" { got[...] = r.Unit }`，
			// 于是这些字段被当成「没查到」=> 进 missing => 每次请求重查一遍。
			//
			// 实测（2026-11-08，生产库）：device=15 的 sensor_field_obs 有 42 个字段，
			// 其中 16 个 unit 为空（12 个 alarm_* + 4 个同类）。这 16 个反复进兜底层，
			// 就是 DROP 之后 57 条/85 秒 42P01 错误日志的来源。
			//
			// 修法：写入空串。missing 于是只剩「**真的没观测过**」的字段 —— 那才是真缺。
			got[r.SensorName] = r.Unit
		}
	}

	// ============ ② 过渡补全：已删除（2026-11-08，unified_data DROP 之后）============
	//
	// 原实现：① 没覆盖到的字段，逐个去 unified_data 查最后一行单位：
	//
	//	for _, name := range missingUnitFields(got, missing) {
	//	    SELECT unit FROM unified_data WHERE device_id=? AND sensor_name=?
	//	    ORDER BY timestamp DESC LIMIT 1
	//	}
	//
	// ============ 为什么删（实测证据，不是推理）============
	//
	// 实测（2026-11-08，从生产备份恢复到 audit 库查）：那 16 个 alarm_* 字段在
	// unified_data 里**有 620,305 行**（12 个字段各 5 万+行），但
	// **unit 列全部为空**（unit_nonempty = 0）。
	//
	// 所以这一层即使查成功，也为 `if u.Unit != ""` 判假 => **从未补上过任何单位**。
	//
	// 也就是说它不是「会失效的兜底」，而是「**一直在查、结论一直为空**的兜底」。
	// DROP 之后它唯一的产出是每次请求 16 条 42P01 错误日志（生产实测 57 条/85 秒）。
	//
	// ⚠ 曾有一版注释在这里写「① 没覆盖到 ⇒ 从未被观测过 ⇒ unified_data 里也不会有行」，
	//   **那句是错的**（实测 62 万行）。它读起来很合理，但会让后来人以为
	//   「那边本来就没数据，不用加回来」—— 实际上那边有 62 万行，只是单位为空。
	//   留此记录：论证要用实测，不要用「合理推断」。
	//
	// 真正的根因是上面 ① 的过滤条件（已修）：空 unit 被判成「没查到」=> 反复重查。

	deviceUnitMu.Lock()
	m := deviceUnitCache[deviceID]
	if m == nil {
		m = make(map[string]string, 32)
		deviceUnitCache[deviceID] = m
	}
	for k, v := range got {
		m[k] = v
	}
	deviceUnitMu.Unlock()
	return m
}

// dedupFieldNames 去重并保持首次出现顺序。
//
// 为什么保持顺序：missingUnitFields 的注释说「保持输入顺序，便于测试稳定」——
// 去重也必须守这条，否则同一次请求的查询顺序会随 map 迭代随机变化，
// 让「查询次数」这类断言的失败信息不可复现。
func dedupFieldNames(fields []string) []string {
	if len(fields) == 0 {
		return fields
	}
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// missingUnitFields 返回 fields 里在 have 中没有的字段（保持输入顺序，便于测试稳定）。
func missingUnitFields(have map[string]string, fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := have[f]; !ok {
			out = append(out, f)
		}
	}
	return out
}

// buildFrameSensorViews 把一份 as-of 快照转成线上形状的 sensorView 列表（按名排序）。
func buildFrameSensorViews(values map[string]float64, units map[string]string) []sensorView {
	views := make([]sensorView, 0, len(values))
	for name, v := range values {
		views = append(views, sensorView{Name: name, Value: v, Unit: units[name]})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}
