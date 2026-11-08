package api

// device_data_sensors.go — 读路径「合成 sensors」（task-19）。
//
// ============ 背景 ============
//
// device_data 的 data_json 曾是
//
//	{"raw_hex":"...","sensors":[{"Name":"total_voltage","Value":54.1,"Unit":"V"}...],"channel_id":1,"timestamp":...}
//
// 其中 sensors 与 unified_data / telemetry_frame 存的是**完全相同的 (name,value,unit)**，
// 生产实测占 device_data JSON 的 **83%（204 MB / 245 MB）**、折合 1,251 B/帧。
// 同一份物理量存三份（unified_data 1,294 B + device_data.sensors 1,251 B + 帧表 129 B），
// 砍掉这一份后日增从 344 MB 降到约 197 MB。
//
// 写路径已停止写 sensors（internal/databus/consumers_heavy.go）。本文件负责**读时补回**，
// 使前端零改动：/latest-data 的响应形状与语义保持不变。
//
// ============ 为什么必须「每个 sensor 各取最新」而不是查同一时间戳 ============
//
// 2026-11-07 L1 写入收窄**无条件启用**后，unified_data 只写「值有变化」的字段：
// 某帧 29 个物理量里通常只有 1~3 个变化，其余被跳过。因此
//
//	WHERE timestamp = <该帧时刻>
//
// 只能取回**变化的**那几个字段 —— 直接拿它合成 sensors 会缺值（用户看到字段「消失」）。
// 正确口径是「每个 sensor_name 各取最新值」，与 /latest-values 完全一致：
// 缓存优先（正常运行时的主路径），miss 才回落按 sensor_name 分组取最新。
//
// ============ StringValue 的保留（不能无视的数据）============
//
// 少数物理量的真值在 StringValue 而 Value 恒为 0（jiabaida_parse.go 的
// hardware_version="V19" / serial_number）。unified_data 与帧表**都没有**这一列，
// 也就是说 device_data.sensors 是它们**唯一**的落库位置。
// 因此写路径改为只保留 StringValue 非空的条目（紧凑的 str_values 映射，通常为空），
// 读时再合并回来。若直接丢弃，BMS 的硬件版本/序列号会**永久丢失**。

import (
	"encoding/json"
	"sort"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// sensorView 是 data_json.sensors 里一项的线上形状。
//
// ⚠ 字段名必须与**历史写入形状逐字一致**：写侧当初直接 json.Marshal 了
// []parser.Field（pkg/parser/parser.go —— 没有 json tag），所以键是 Go 字段名
// 大驼峰 Name/Value/Unit/StringValue。前端 DataPanel.vue 与 useDeviceData.ts
// 都按 s.Name / s.Value / s.StringValue 取值 —— 改成小写会静默全空。
type sensorView struct {
	Name        string
	Value       float64
	Unit        string
	StringValue string
}

// deviceDataJSONWithSensors 返回补好 sensors 的 data_json。
//
// 三条不变量：
//  1. **存量兼容**：老行本来就带 sensors ⇒ 原样返回，绝不覆盖（它可能比合成结果更全）；
//  2. **失败不 5xx**：任何解析/查询失败都退回原串 —— 进度接口式的"宁可少一块也不整页挂掉"；
//  3. **无副作用**：只读，不写库（读路径改库会让 GET 变成写放大）。
func deviceDataJSONWithSensors(db *gorm.DB, deviceID uint, raw string) string {
	if raw == "" || db == nil || deviceID == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return raw // 坏 JSON：保持原样，不制造第二处解析入口
	}
	if _, ok := obj["sensors"]; ok {
		return raw // 存量行：本来就有，不动
	}

	views := latestSensorViews(db, deviceID)
	// 即便一个物理量都没有也要**写出空数组**而不是省略键：
	// 前端 `Array.isArray(dj.sensors)` 是形状判据，缺键与空数组的语义不同
	// （缺键 ⇒ 走"解析失败"分支，空数组 ⇒ "库里确实还没有数值"）。
	if views == nil {
		views = []sensorView{}
	}
	// 排序不是为了好看：LatestValue 缓存是 map[sensor_name]，**迭代顺序随机**。
	// 不排序的话同一份数据两次请求的 sensors 顺序不同 —— 前端按名取值不受影响，
	// 但会让「响应形状不变」的断言变成随机失败（flaky），排查成本远高于这一行。
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

	// StringValue 只存在于 device_data 自己的 str_values（见文件头）。
	views = mergeStringValues(obj, views)

	enc, err := json.Marshal(views)
	if err != nil {
		return raw
	}
	obj["sensors"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return string(out)
}

// mergeStringValues 把 data_json.str_values 合并进 views，并**补齐只有字符串的字段**。
//
// ============ 为什么要「补齐」，不能只「更新已存在的」（2026-11-08）============
//
// 旧实现只做「遍历 views，若 str_values 里有同名字段就填 StringValue」。
// 当数值源拿不到该字段时（缓存 miss + 帧表不可用），views 里**没有**这个字段，
// 于是 str_values 里的它被**静默丢弃**。
//
// 后果是永久性的：`hardware_version` / `serial_number` 的 Value 恒为 0，真值只在 str_values
// 里，而 unified_data 与 telemetry_frame **都没有 StringValue 列**（帧表的 Reading 只有
// Name/Value）—— device_data 是它们的**唯一**落库位置。丢掉它 = BMS 硬件版本/序列号
// 永久消失，且无副本可恢复。
//
// 实测（2026-11-08，生产只读）：全库 **0 行** device_data 含 str_values（24h 内也是 0）。
// 原因：这些字段只由**控制动作** read_hardware_version / read_sn 产生，现场从未读过。
// 即：这是「**还没被走过**」的路径，不是「走不通」的路径。
//
// ⚠ 两个事实必须一起读，缺一个就会得出错误结论：
//
//		· 只看到「唯一落库位置」 => 以为里面有大量数据要保护，可能为它保留整条旧读路径；
//		· 只看到「当前 0 行」     => 当成死代码删掉，于是将来第一次读 BMS 版本时静默丢失。
//	 代价不对称（删错的代价是永久丢数据且无副本），所以保留「补齐」逻辑。
//
// 所以在硬切换删掉数值回落之后，必须显式补齐：str_values 里有的、views 里没有的字段，
// 直接用 `Value=0` + StringValue 造一条。前端读 StringValue 优先，显示正确。
//
// 钉住它的用例（两条，覆盖两条不同写出路径）：
//
//	· TestStringOnlyField_SurvivesWhenValueSourceEmpty  单行（latest-data）
//	· TestStringOnlyField_SurvivesInListPath            列表（edge-devices/:id/data）
//
// 它们的断言形状是契约而非实现：str_values 里有、数值源给不出的字段，必须以
// Value=0 + StringValue 出现在 sensors 里 —— 与「现在有几条路径」无关，加路径自动受约束。
func mergeStringValues(obj map[string]json.RawMessage, views []sensorView) []sensorView {
	rawsv, ok := obj["str_values"]
	if !ok {
		return views
	}
	var sv map[string]string
	if err := json.Unmarshal(rawsv, &sv); err != nil || len(sv) == 0 {
		return views
	}
	seen := make(map[string]int, len(views))
	for i := range views {
		seen[views[i].Name] = i
	}
	for name, s := range sv {
		if i, has := seen[name]; has {
			views[i].StringValue = s
			continue
		}
		views = append(views, sensorView{Name: name, StringValue: s})
	}
	return views
}

// latestSensorViews 取该设备每个物理量的最新值（缓存优先，miss 回落 SQL）。
//
// 与 /latest-values 端点**同一口径**：两者若不一致，详情页(用 latest-values)
// 与数据面板(用 latest-data) 会显示不同的数值，且这种不一致只在缓存冷热切换时出现，
// 极难复现。
func latestSensorViews(db *gorm.DB, deviceID uint) []sensorView {
	cached := LatestValues(deviceID)
	if len(cached) > 0 {
		out := make([]sensorView, 0, len(cached))
		for _, r := range cached {
			out = append(out, sensorView{Name: r.SensorName, Value: r.Value, Unit: r.Unit})
		}
		return out
	}
	return latestSensorViewsFromDB(db, deviceID)
}

// latestSensorViewsFromDB 是缓存 miss 时的回落查询：每个 sensor_name 取最新一行。
//
// 为什么不用「ORDER BY sensor_name, timestamp DESC 全取再在 Go 里去重」
// （/latest-values 目前的写法）：那会把该设备**全部历史行**读进内存；
// 一台跑了半年的设备是千万级。这里用 GROUP BY 拿每传感器的最新时间戳再等值 JOIN，
// 两边都走 (device_id, sensor_name, timestamp) 或 (device_id, timestamp) 索引，
// 只返回「每个传感器一行」的结果集。
//
// 两条查询：先走**有界**快路（近 listBaselineLookback = 2h），为空再走无界兜底。
//
// 为什么需要这个分层：无界版的 "GROUP BY sensor_name, MAX(timestamp)" 在 PG 上
// 用不到索引（Parallel Seq Scan + HashAggregate）。实测 1,276 万行的设备
// **689 ms**，而有界版 **7 ms**（量级差 100 倍）。生产上这个函数只在缓存 miss 时
// 走到 —— 正常情况缓存是热的（main.go:524 启动 WarmupLatestValues + 每帧
// SetLatestValue 更新），但**回填（task-12）给老设备写进 unified_data 的行不会进缓存**，
// 于是"打开一台老设备详情页"就是一次冷查询。改造前 /latest-data 只读 device_data
// 一行、完全不碰 unified_data，所以这 689 ms 是本次改造引入的，必须自己兜掉。
//
// 为什么快路为空还要走无界：有界版对"已停止上报超过 2h 的设备"会返回空，
// 而 /latest-data 的语义是"最后已知状态"——设备停了不该直接变成空面板。
// 无界兜底只在**真的有历史数据但全在 2h 之前**时才执行，属于冷门路径。
func latestSensorViewsFromDB(db *gorm.DB, deviceID uint) []sensorView {
	// ── 唯一数据源：帧表（2026-11-08 硬切换）──
	//
	// 为什么必须把它排在最前（不再是「缓存 miss 后的回落」）：
	//
	//	unified_data 已于 2026-11-08 停写，剩下的是保留期内的**旧数据**。
	//	所以下面那两条 SQL 现在拿到的不是「最新值」，而是「停写那一刻的值」，
	//	并且会随时间越来越旧 —— 但**不会报错**，用户看到的是过期数值而不是空面板。
	//
	// 「返回过期值」比「返回空」危险得多（latest_values_frames.go 文件头同一句）：
	//	返回空     => 用户立刻发现
	//	返回过期值 => 用户以为正常
	//
	// 帧表是 L2 主写入路径，停写后仍在增长，且帧是「该 slot 的全部字段」
	//（不受 L1 收窄影响），一次解码就能拿到完整的最新快照。
	if views, ok := latestSensorViewsFromFrames(db, deviceID); ok {
		return views
	}

	// ============ 帧表不可用：返回空，**没有任何替代数据源**（2026-11-08）============
	//
	// 这里原本有两条回落 SQL（有界 2h 快路 + 无界兜底），都已删除。
	//
	// ⚠ **明确记下：帧表 ok=false 时本路径没有任何数据源，/latest-data 的 sensors
	// 会返回空数组。这是 2026-11-08 硬切换后的**既定状态，不是遗漏**。**
	//
	// 为什么不留回落：unified_data 已停写，回落拿到的是「停写那一刻的值」并且
	// 会越来越旧，却**不会报错** —— 用户看到过期数值而不是空面板。
	// 返回空 ⇒ 用户立刻发现；返回过期值 ⇒ 用户以为正常。
	//
	// 若你（后来的维护者）想「补一条回落让这里看起来更完整」——**请先读这段**：
	// 那会把硬切换悄悄撤销掉，而且不会有任何用例变红。
	//
	// 生产实测：帧表覆盖全部 5 台设备、3,463,342 帧；ok=false 从未出现。
	// 它只在三种情况下发生：字典缺失 / 该时段无帧 / 解码失败。
	return []sensorView{}
}

// latestSensorViewsFromFrames 从帧表重建「该设备每个物理量的最新值」。
//
// 返回 ok=false 表示帧表路径**不可用**（不是「没有数据」），调用方必须回落：
//
//	· 字典未注入 / 预热失败（无法还原字段名与量化规格）；
//	· 该设备在帧表里一帧都没有（新设备刚上线 / 帧还没落库）；
//	· 解码报错或超出护栏。
//
// 它只是 LatestRowsFromFrames 的视图化包装 —— **故意不自己写一份解码逻辑**，
// 因为 /latest-values 的 miss 回落要的是 models.UnifiedData 形状，而两者的口径
// 必须逐字段相等（文件下方 latestSensorViews 的注释：不一致只在缓存冷热切换时出现，
// 极难复现）。共用同一个 rows 生成器是唯一能让这条不变量**结构上成立**的做法。
func latestSensorViewsFromFrames(db *gorm.DB, deviceID uint) ([]sensorView, bool) {
	rows, ok := LatestRowsFromFrames(db, deviceID)
	if !ok {
		return nil, false
	}
	views := make([]sensorView, 0, len(rows))
	for _, r := range rows {
		views = append(views, sensorView{Name: r.SensorName, Value: r.Value, Unit: r.Unit})
	}
	return views, true
}

// deviceDataJSONListWithSensors 给**分页列表**的每一行补 sensors（/edge-devices/:id/data）。
//
// ============ 口径：该行时刻的「设备状态快照」，不是「这一帧解析出了什么」============
//
// 两种都是可能的语义，这里刻意选**前者**（as-of 快照）：
//
//	as-of   : 每个 sensor_name 取「timestamp <= 本行时刻」的最新值 —— 完整的设备状态
//	同刻严格: WHERE timestamp = 本行时刻 —— 只有该帧**变化**的字段
//
// 为什么不是同刻严格：2026-11-07 L1 写入收窄**无条件启用**后，非心跳帧在
// unified_data 里只留变化字段（心跳每 60s 一次、写全字段）。一台 29 物理量的
// BMS 每 5s 一帧，绝大多数帧只变 1~3 个值 ⇒ 同刻查会合成出 1~3 个传感器，
// 面板「数据」列看起来像坏了（改造前是 29 个）。as-of 给的是完整快照，数量与
// 改造前对齐。
//
// 为什么不是「每 sensor 取当前最新」（= /latest-data 的口径）：
// 列表是**历史帧**，给 3 天前的行配今天的读数是把历史伪造成现状。as-of 保留了
// 时间维度 —— 每行看到的是**那一刻**的值。
//
// 与 /latest-data 的关系：as-of(T=now) 恰好等价于 /latest-data 的「每 sensor 取最新」，
// 所以两者是**同一个定义在不同 T 上的取值**，不是两套逻辑。
//
// ⚠ 一个刻意的行为差异（不是 bug）：as-of 会把**其它命令**帧里的物理量也带进来。
// read_basic_info 帧的上一帧若是 read_cell_voltage，本行就会同时显示单体电压。
// 改造前该行只显示 read_basic_info 的字段。这是 as-of 语义的必然结果，而数据面板
// 的用途是"看设备在该时刻是什么状态"，因此接受。
//
// 成本：整页 **2 条**查询（基线 GROUP BY + 区间增量），与页大小无关；
// 逐行一条会变成 50 次往返，而这是个翻页接口，改造前是 0 次。
//
// 存量行（本来带 sensors）一律不动：它们记的是那一帧的完整物理量，比合成结果准。
func deviceDataJSONListWithSensors(db *gorm.DB, deviceID uint, rows []models.DeviceData) {
	if db == nil || deviceID == 0 || len(rows) == 0 {
		return
	}
	// 先挑出真正需要合成的行（存量行 / 坏 JSON / 零时间戳都不参与），
	// 否则会给一个纯存量页白发两条查询。
	type pending struct {
		idx int
		ts  time.Time
	}
	todo := make([]pending, 0, len(rows))
	for i := range rows {
		if rows[i].DataJSON == "" || rows[i].Timestamp.IsZero() {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rows[i].DataJSON), &obj); err != nil {
			continue // 坏 JSON：原样留给前端（与单行路径同一约定）
		}
		if _, has := obj["sensors"]; has {
			continue // 存量行：记的是那一帧的完整物理量，比合成结果准
		}
		todo = append(todo, pending{idx: i, ts: rows[i].Timestamp})
	}
	if len(todo) == 0 {
		return
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].ts.Before(todo[j].ts) })
	earliest := todo[0].ts
	latest := todo[len(todo)-1].ts

	// ── 首选：帧表合成（P0 回归修复，2026-11-07）──
	//
	// unified_data 分区正在分批退役（10 月已 DETACH/删除），而帧表是长期存储且
	// **全字段**（不受 L1 收窄影响）。一次解码服务整页，比原来的「基线 GROUP BY +
	// 区间增量」还便宜，且天然拿到完整快照。
	//
	// 帧表不可用（字典缺失 / 解码失败 / 该时段无帧 / 窗口超限）⇒ ok=false ⇒
	// 落到下面的 unified_data 逻辑，行为与改造前完全一致（fail-open）。
	if snaps, ok := latestSensorSnapshotsFromFrames(db, deviceID, earliest, latest); ok {
		// ============ 收集本页涉及的全部字段名（**去重**，2026-11-08 P0）============
		//
		// 原实现是「遍历每个快照、每个字段，直接 append」：
		//
		//	snapNames := make([]string, 0, 32)
		//	for _, sn := range snaps {
		//	    for fn := range sn.Values { snapNames = append(snapNames, fn) }
		//	}
		//
		// 一页 763 个快照 × 42 字段 = 32,046 项，**同一字段出现 763 次**。
		// 下游 unitMapForDevice 按数组长度迭代发查询 => 单字段被查 763 次
		// => 一次翻页 12,140 次 DB 查询（生产实测，约 2.4 秒纯浪费）。
		//
		// 这里去重是**正确性**（本函数知道自己要的是「字段名集合」，不是「字段名序列」）；
		// unitMapForDevice 内部也有一层去重，那是**兜底**（防其它调用方踩）。
		// 两层都要有：只靠兜底 = 依赖别人的实现细节；只靠这里 = 别处仍会踩。
		seen := make(map[string]struct{}, 64)
		snapNames := make([]string, 0, 64)
		for _, sn := range snaps {
			for fn := range sn.Values {
				if _, dup := seen[fn]; dup {
					continue
				}
				seen[fn] = struct{}{}
				snapNames = append(snapNames, fn)
			}
		}
		units := unitMapForDevice(db, deviceID, snapNames)
		for _, pd := range todo {
			idx := snapshotAt(snaps, pd.ts)
			if idx < 0 {
				// 页首之前一个帧都没有（设备当时还没上线）：保持原样，不编造。
				continue
			}
			applySensorViews(&rows[pd.idx], buildFrameSensorViews(snaps[idx].Values, units))
		}
		return
	}

	// ============ 帧表不可用：没有替代数据源（2026-11-08）============
	//
	// 这里原本是「as-of 基线 + 区间增量游标」的实现（约 50 行），已整段删除。
	//
	// ⚠ 帧表 ok=false 时，这些行的 sensors 不会渲染（data_json 里没有 sensors 键）。
	// 这是 2026-11-08 硬切换后的既定状态，不是遗漏。
	//
	// 为什么整段删而不是只删基线：只删基线会让 deltas 从空状态开始叠，
	// 结果只含「区间内的变化」—— 页面显示不完整，但看起来像有数据。
	// 那比「整个不渲染」更糟：显眼的缺失会被发现，半截数据不会。
	//
	// 前端已确认可接受：useDeviceData.ts 是 if (dj.sensors && Array.isArray(...))，
	// 缺键不抛错，只是该行不参与合并。
	//
	// ============ 但纯字符串字段必须仍然写出来（2026-11-08，契约）============
	//
	// 「数值源没有」不等于「这一行什么都写不出」：str_values 里的 hardware_version /
	// serial_number 等字段的 Value 恒为 0，**本来就不在数值源里**，它们只此一份。
	//
	// 所以这里仍要逐行调一次 applySensorViews(&rows[pd.idx], nil)：
	// 传入空 views ⇒ mergeStringValues 只产出 str_values 里的纯字符串字段；
	// 若该行 str_values 也为空，applySensorViews 内部的 len(views)==0 守卫会让它
	// 保持原样（不写 sensors 键）。
	//
	// 这条由 TestStringOnlyField_SurvivesInListPath 钉住。
	for _, pd := range todo {
		applySensorViews(&rows[pd.idx], nil)
	}
}

// applySensorViews 把合成好的 sensors 写回一行 data_json（并合并本行的 str_values）。
//
// 抽成函数是为了让**帧表路径**与 **unified_data 回落路径**共用同一段写出逻辑 ——
// 两条路径的线上形状必须逐字节同构，否则前端会在"哪段时间"上看到不同结构。
func applySensorViews(row *models.DeviceData, views []sensorView) {
	if row == nil {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.DataJSON), &obj); err != nil {
		return
	}
	// str_values 只存在于**本行**：它是该帧解析出的字符串读数，
	// 不存在于 unified_data / 帧表（那里只有数值），所以只能逐行合并，不能跨帧继承。
	//
	// 用 mergeStringValues 而不是原地「更新已存在项」：纯字符串字段（hardware_version /
	// serial_number 的 Value 恒为 0）本来就不会出现在帧表/数值源里，只「更新」会把它们
	// 整个丢掉。理由详见 mergeStringValues 的注释。
	views = mergeStringValues(obj, views)

	// 一个字段都没有 ⇒ **不写 sensors 键**，保持该行原样。
	//
	// 这条守卫让两种情形各得其所：
	//	· 数值源为空、str_values 也为空 => 行保持原样（没有 sensors 键）。
	//	  前端 `if (dj.sensors && Array.isArray(...))` 不会误判成「库里确实没有数值」，
	//	  而是走「这一帧没有合成结果」那条更朴素的理解 —— 与硬切换前的形状一致。
	//	· 数值源为空、但 str_values 有纯字符串字段 => 只写这些字段（见下）。
	//
	// 反过来说：如果这里不做守卫，`json.Marshal(nil)` 会写出 `"sensors":null`,
	// 而 null 和缺键是两种不同的坏形状（前端按 .length 取值会抛 TypeError）。
	if len(views) == 0 {
		return
	}

	enc, err := json.Marshal(views)
	if err != nil {
		return
	}
	obj["sensors"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return
	}
	row.DataJSON = string(out)
}
