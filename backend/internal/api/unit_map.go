package api

// unit_map.go — 「字段名 → 单位」映射（sensor_field_obs 的**唯一消费者**）。
//
// ==================== 为什么这个文件单独存在（2026-11-11）====================
//
// 它原来住在 device_data_sensors_frames.go 里，那个文件的职责是「用帧表合成
// device_data 列表行的 sensors」。device_data 整表退役后（docs/设计/原始数据退役-
// 改造方案-2026-11-11.md §2.1），合成逻辑整段删除，但**单位映射必须留下**：
//
//   · telemetry_frame **没有 unit 列** —— 帧里只有 (name, value) 与量化规格；
//   · 单位只存在于 driver/parser 层，最终被 field_resolver 的 projector 投影进
//     sensor_field_obs（维度表，主键 (device_id, sensor_name)）；
//   · /devices/:id/history、/latest-values、/sensor-fields 预览等**所有**图表与
//     读数接口都靠本文件补单位。删掉它 ⇒ 所有图表的单位变空（数值仍对，但界面
//     会从 "3.30 V" 变成 "3.30"）。
//
// 所以本文件的定位是明确的：**sensor_field_obs 的唯一读入口**。不要再把「合成
// sensors / data_json」之类的逻辑塞回这里 —— 那是已退役的读路径。

import (
	"sync"

	"gorm.io/gorm"
)

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

// unitMapForDevice 返回该设备 fields 中各字段的单位（来源：维度表 sensor_field_obs）。
//
// ============ 唯一来源：sensor_field_obs（2026-11-07 起，2026-11-08 后是唯一）============
//
// 维度表 sensor_field_obs 由 field_resolver 的 projector 从「每帧入库回调」投影写入
// （main.go 的 SetLatestSinkFn -> registry.Observe -> projector），**数据通路不经过
// unified_data**。它的 upsert 已带 CASE WHEN excluded.unit <> 空串 时保留旧值 的语义
// —— 写它的人已经表达了「单位是静态属性，一次观测到就长期有效」。
// 主键 (device_id, sensor_name)，查询是主键前缀扫描，比 DISTINCT ON 便宜。
//
// 历史（必须保留，否则后人会以为"还能再补一层"）：本函数曾有三层 ——
// ① sensor_field_obs、② unified_data 的 DISTINCT ON 兜底、③ 逐字段 LIMIT 1 兜底。
// 2026-11-08 实测后删掉了 ②③，理由有两条实测证据：
//
//	· ② 在生产上执行了 **33 秒**（device14 1500 万行，索引 ASC vs 查询 DESC）；
//	· ②③ 从未补上过任何单位 —— 那 16 个 alarm_* 字段在 unified_data 里
//	  有 620,305 行，但 unit 列**全部为空**（unit_nonempty = 0）。
//
// 也就是说它不是「会失效的兜底」，而是「一直在查、结论一直为空、还特别慢」的兜底。
// unified_data 停写/DROP 之后它连查都查不了了，删掉是行为等价 + 消除 42P01 噪音。
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
// ⚠ fields 必须由调用方给出（本次真正需要的字段）。不提供「全量模式」：
//
//	要列出全量字段名就得扫 unified_data 的 DISTINCT sensor_name，那正是要避免的。
func unitMapForDevice(db *gorm.DB, deviceID uint, fields []string) map[string]string {
	if db == nil {
		return nil
	}
	// ============ 输入去重（2026-11-08，P0）============
	//
	// 调用方传进来的 fields **可能含大量重复**（例如「逐个快照 × 逐个字段」拼出来的
	// 列表，一页 763 个快照 × 42 字段 = 32,046 项，同一字段出现 763 次）。
	//
	// 而本函数下面按数组长度迭代发查询 ⇒ 重复被放大成 N 倍：
	//
	//	· IN 查询参数长度 = len(missing)
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
	//
	// ============ 它与「空串入缓存」是两件独立的事，不要混为一谈 ============
	//
	//  · 空串入缓存（下面 got[r.SensorName] = r.Unit）修的是**正确性**：
	//    那些「观测过但本来就没有单位」的字段（alarm_* 等）进缓存 => missing 变空
	//    => 不再反复重查 => 42P01 归零。
	//
	//  · 本函数（去重）修的是**性能**：减少 99.9% 的遍历和 SQL 参数。
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

	// 维度表：只查还缺的字段（71 行的表，IN 查询微秒级）。
	var obs []row
	if err := db.Raw(
		"SELECT sensor_name, unit FROM sensor_field_obs WHERE device_id = ? AND sensor_name IN ?",
		deviceID, missing).Scan(&obs).Error; err == nil {
		for _, r := range obs {
			// ============ 空串也要写入（2026-11-08 修）============
			//
			// 空 unit 表示「**观测过，但这个字段本来就没有单位**」（典型：alarm_* 系列、
			// status 类物理量）。原实现是 if r.Unit != "" { got[...] = r.Unit }，
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
// 为什么保持顺序：missingUnitFields 的契约是「保持输入顺序，便于测试稳定」——
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
