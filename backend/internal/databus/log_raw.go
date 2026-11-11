package databus

import "encoding/hex"

// log_raw.go — 日志里承载「原始总线字节」的公共格式（S1 可观测性，2026-11-11）。
//
// ==================== 为什么需要这个文件 ====================
//
// 产品裁决「原始数据不入库、解析失败打日志就好了」之后，日志成了**唯一的**原始
// 字节取证来源（device_data 整表退役，见 docs/设计/原始数据退役-改造方案-2026-11-11.md
// §4.1/§4.4）。因此日志里的 raw 必须满足三件事：
//
//  1. **有界**：一帧最长可到 1 KB 以上（多帧重组后），全量 hex 进日志会让单条
//     日志变成数 KB，而当前 docker 日志是唯一存储（已配 50m×5 轮转）。截断到
//     128 B 是**前缀**截断 —— 协议头、长度、命令字、channel 都在前 128 B 内，
//     排查「帧对不对」需要的信息全在这里；尾部校验和可以按 128 B 前缀重新计算。
//  2. **定长口径**：截断后的长度单独记 raw_len（未截断的真实长度），否则读日志
//     的人无法判断「这帧本来就 128 B」还是「被截断了」。
//  3. **机器可 grep**：统一输出为裸 hex（不带 0x、不带空格），与 device_data 时代
//     data_json.raw_hex 同形，历史排查习惯与脚本可以直接复用。
//
// 为什么不用 logger 的格式化字符串：结构化字段（logger.Warn(msg, kv...)）才能按
// node_id/edge_device_id/reason 聚合，格式化字符串只能全文搜索。

// rawLogMaxBytes 是日志里保留的原始字节上限（128 B）。
//
// 取值依据：BMS/逆变器帧长 78~256 B，128 B 覆盖协议头 + 绝大多数字段段；
// 再大对「判断帧格式/命令字」没有增量信息，只增加日志体积。
const rawLogMaxBytes = 128

// rawHexForLog 返回 raw 的 hex 表示，超出上限时**前缀截断**。
//
// 截断语义是「取前 N 字节」，不是「取后 N 字节」：协议头在前，它才是判断
// 「这是不是我要的那条命令」的依据。
func rawHexForLog(raw []byte) string {
	if len(raw) > rawLogMaxBytes {
		raw = raw[:rawLogMaxBytes]
	}
	return hex.EncodeToString(raw)
}
