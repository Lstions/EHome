// Package telemetry 实现遥测数据的 L2 帧级增量编码与解码。
//
// 权威设计：docs/设计/遥测存储优化方案-2026-11-06.md §4（L2 核心）与 §5.1（解码层）。
//
// # 一句话
//
// 一帧（同一设备同一时刻解析出的全部字段）压成**一行**：payload =
// 1B flags + ceil(N/8)B changed_mask + 变化字段的量化值 LSB-first 位打包。
// 现状是每字段一行 × 5 条索引（实测 178.7 B/行），一帧 29 行 ≈ 5.2 KB；
// 本编码把同一帧压到 12–17 B（§4.5 实测 16.7 B / 4.7 B）。
//
// # 分层（不要让下面几层互相穿透）
//
//	spec.go    量化规格（§4.3）：每字段 (量程下界, 步长, 位宽)。**唯一的精度真值来源**。
//	           slot 描述里固化一份快照，改表不会让旧数据解错。
//	codec.go   payload 编解码（§4.2）：flags / changed_mask / LSB-first 位打包。
//	slot.go    slot 字典（§4.4）：字段名集合 + 量化规格版本 → slot_id。
//	           **禁止硬编码槽位**：device15 实测 7 种帧类型（3/14/2/5/5/9/4 字段），
//	           硬编码 42 槽会把不同指令的值写串。
//	encoder.go 有状态编码器：按 (设备, slot) 维护上一帧量化码，产出 changed_mask；
//	           60 s 关键帧兜住增量链断裂窗口（§8 风险表）。
//	decode.go  §5.1 解码层：DecodeRange（时间窗）与 LatestSnapshot（当前全量）。
//	store.go   telemetry_frame 的读写（表由 datalifecycle 的 partition_mgr 建）。
//	dualwrite.go 双写旁路（阶段 2 第 2 步）：默认关闭，失败绝不影响主写入。
//
// # 依赖方向
//
//	telemetry → models（+ gorm）
//
// 本包**不得** import internal/databus / internal/api / internal/datalifecycle：
// 双写由 databus 反向调用本包（见 dualwrite.go 的接线说明），一旦反过来就是环。
package telemetry
