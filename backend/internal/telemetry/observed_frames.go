package telemetry

// observed_frames.go — 「该设备是否上报过某物理名」的**存在性判据**（2026-11-08）。
//
// ============ 为什么这个判据属于 telemetry 包 ============
//
// 它要回答的问题是「这台设备的帧里有没有这个字段」，输入是
// telemetry_frame.slot_id + telemetry_slot.fields_json —— **两个都是本包的表**。
// 放在这里而不是 api/datasource，原因有三：
//
//	① api 和 datasource **两个包都要用它**，而 datasource 不能 import api
//	   （api → datasource 已经存在，反向就是循环依赖）；
//	② 它依赖 slot 的 JSON 结构（UnmarshalFields），那是本包的契约；
//	③ 本包的依赖红线是「只依赖 models + gorm」，本函数正好只需要这两者。
//
// ============ 为什么不需要解码 ============
//
// 「这个物理名有没有被上报过」**不需要 payload**。帧表里已经有答案的索引：
//
//	telemetry_frame.device_id  -> 该设备近期出现过哪些 slot_id（几十个）
//	telemetry_slot.fields_json -> 该 slot 的字段名列表
//
// 一次 JOIN 就有答案，与 payload 大小、解码成本、slot 字典是否 WarmUp 全都无关。
// 这对「创建操作的校验路径」尤其重要：解码整段历史会把 5ms 的校验变成秒级。
//
// ============ 为什么有时间窗 ============
//
// 判据是「**近期**有没有上报过」，不是「历史上有没有过」。
// 取 30 天覆盖「配好字段名后立刻建数据源」的正常流程，
// 也不至于把半年前已废弃的字段名算数。
//
// 时间窗**不是正确性参数**，是「多久算曾上报」的语义选择 ——
// 与 latest_values_warmup 的回看窗口同一性质。

import (
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// 表名常量。
//
// 为什么不 import datalifecycle.TelemetryFrameTable：本包的依赖红线是
// **只依赖 models + gorm**（见 doc.go「依赖方向」）。datalifecycle 在依赖树上位于
// 本包之上，import 它就成环。
//
// 对齐由测试钉住（TestObservedFrameTableNamesAligned）：
// 常量值与 models.TelemetryFrame{}.TableName() 必须逐字一致 —— 写错了
// 只会让 HasTable 返回 false ⇒ 判据恒 false ⇒ 校验永远拒绝，症状是
// 「配什么都提示未上报」，很容易被误判成业务逻辑问题。
const (
	observedFrameTable = "telemetry_frame"
	observedSlotTable  = "telemetry_slot"
)

// ObservedLookbackDays 是「曾上报」的回看窗口（天）。
//
// ============ 取值依据（2026-11-08，Lead 要求写明）============
//
// 先说清一件事：这个窗口**不决定「历史观测会不会丢」**，因为主来源
// sensor_field_obs 是**累积表**，不是滚动窗口：
//
//	· fieldObsFlushSink.UpsertObservations 是 UPSERT（samples 累加、
//	  first_seen 取 min、last_seen 取 max），从不按时间删行；
//	· 它不在 TimeseriesTables / 保留期清单里（datalifecycle 不清理它）。
//
// 所以「某字段历史上被观测过」这件事，在投影表里是**永久**的。
// 本窗口只对**帧表兜底路径**生效，它需要覆盖的是两种场景：
//
//	① 投影表尚未 flush（正常态）
//	   projector 每 ObserveInterval（默认 60s）批量 UPSERT 一次，
//	   所以「设备刚上报 → 投影可见」之间最多有 60s 的洞。
//	   这一档其实 5 分钟就够。
//
//	② 投影表为空或缺失（异常态 / 新部署）
//	   投影表由 projector 冷启动后逐步填充，历史数据**不会**被回填 ——
//	   也就是说一个刚升级的库，投影表里只有「升级之后」的观测。
//	   这时帧表是唯一能看到「升级之前上报过什么」的来源。
//
// 取 30 天的理由：把 ② 覆盖得足够宽（一个月内上报过的字段都算数），
// 而 ① 只需要几分钟，30 天是它的超集，不会更差。
//
// ============ 为什么不取更长（例如 2h 或 1 年）============
//
//	· 2h（原 listBaselineLookback 的值）在这里**论证不成立**：
//	  那个值的前提是「设备在持续报数、命令 interval 上限 1h」，而校验路径上
//	  的设备可能刚上线、可能停了一周 —— 用「在采设备」的论证去卡「校验」
//	  会把合法配置判成未上报。**同一个数字放进不同前提里就不是同一个判据。**
//
//	· 1 年以上：没有收益。投影表已经覆盖了长期历史（累积表），
//	  而帧表本身受保留期管理（默认 45 天，见 EHOME_DATA_RETENTION_DAYS），
//	  窗口开得比保留期还长只是让 SQL 扫描更多分区，结果不变。
//
// 30 天与帧表保留期（45 天）同量级且留有余量：即使某天保留期被调短，
// 这个窗口也不会立刻失效。
//
// ============ 扫描成本 ============
//
// 这条查询走 telemetry_frame 主键 (device_id, ts, seq) 的前导列（device_id 等值）
// + ts 范围裁剪分区。30 天 vs 2 小时的区别是**多扫若干个月分区**，
// 但每个分区上仍是 index scan 且被 device_id 收窄。
//
// 关键：它只在**投影表未命中时**才执行（见 DeviceObservedField 的调用方），
// 而创建数据源/来源组是低频操作。**不为低频路径优化到牺牲正确性。**
const ObservedLookbackDays = 30

// DeviceObservedField 判断设备在近 ObservedLookbackDays 天内有没有过该物理名的帧。
//
// 返回 false 有三种情况，**调用方必须把三种都当作「不能确认已上报」**：
//
//	· 表不存在（精简部署 / 迁移未跑）
//	· 该设备近期一帧都没有（新设备刚上线，帧还没落库）
//	· 帧里确实没有这个字段
//
// 也就是说它**不做**「表不可用 ⇒ 放行」这种 fail-open —— 那会让配错的字段名通过校验，
// 而校验存在的意义正是挡住它。要 fail-open 的场景（例如 UI 提示）由调用方自己决定。
func DeviceObservedField(db *gorm.DB, deviceID uint, field string) bool {
	if db == nil || deviceID == 0 || field == "" {
		return false
	}
	if !db.Migrator().HasTable(observedFrameTable) ||
		!db.Migrator().HasTable(observedSlotTable) {
		return false
	}
	since := time.Now().UTC().AddDate(0, 0, -ObservedLookbackDays)

	// 用 JOIN + 去重而不是 count(*)：只关心存在性。
	//
	// 索引路径：telemetry_frame 主键 (device_id, ts, seq) 前导列命中 device_id，
	// ts 范围裁剪分区与行；telemetry_slot 走主键 slot_id。
	//
	// 为什么字段名匹配放在 Go 里而不是 SQL：fields_json 是 JSON 数组，
	// PG 的 jsonb 函数在 SQLite 上不可用（测试库是 SQLite）。
	// 而「该设备近期出现过哪些 slot」最多几十行，取回来在 Go 里比对既跨方言又更便宜。
	var slots []models.TelemetrySlot
	err := db.Raw(
		"SELECT DISTINCT s.slot_id, s.fields_json FROM "+observedFrameTable+" f "+
			"JOIN "+observedSlotTable+" s ON s.slot_id = f.slot_id "+
			"WHERE f.device_id = ? AND f.ts >= ?",
		deviceID, since).Scan(&slots).Error
	if err != nil || len(slots) == 0 {
		return false
	}
	for _, s := range slots {
		fields, ferr := UnmarshalFields(s.FieldsJSON)
		if ferr != nil {
			// 坏行跳过而不是整体失败：与 DBSlots.WarmUp 的既有约定一致
			// （「单行坏掉不该让整个字典失效」）。这里更保守 ——
			// 一个坏 slot 只会让它自己的字段判成「未上报」。
			continue
		}
		for _, f := range fields {
			if f.Name == field {
				return true
			}
		}
	}
	return false
}
