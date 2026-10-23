package databus

import (
	"encoding/hex"
	"time"

	"ehome/backend/internal/ingest"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"
)

// ingest_bridge.go 是 databus 与 L1 接入层（internal/ingest）之间的**唯一**粘合点。
//
// # 为什么单独一个文件
//
// consumers_heavy.go 是每帧热路径的核心文件（1000+ 行），把接入层调用内联进去
// 会让「解析」与「落库/分发」两件事再次纠缠 —— 而这正是本次改造要消除的问题。
// 这里只做三件事：
//  1. 把设备行/配置查好，构造 SupportContext / DecodeContext（adapter 不查库）
//  2. 调 Resolver.Decode
//  3. 把结果（或分类失败原因）交回 consumer
//
// # 惰性上下文（性能红线）
//
// 改造前的逻辑里，标定字节/命令模板的查询**只在 driver 分支内发生**
// （consumers_heavy.go:296-338）。若在这里提前查，那么「配置解析成功」的帧
// （生产绝大多数）会白白多两次 DB 查询。因此用闭包延迟到 adapter 真正需要时。

// decodeOutcome 是一次解析尝试的结果。
//
// 为什么不用 (Sample, error) 直接返回：consumer 需要区分「无 adapter」
// 与「有 adapter 但失败」—— 两者审计级别不同（前者是配置缺陷、后者是数据问题）。
type decodeOutcome struct {
	Sample ingest.Sample
	// Attempted 表示是否有任何 adapter 尝试过解析（false ⇒ no_adapter）。
	Attempted bool
	// FailReason 是失败原因（Attempted=true 且 Sample 无效时有效）。
	FailReason ingest.Reason
	// Err 是底层错误（用于日志）。
	Err error
}

// ok 报告解析是否成功。
func (o decodeOutcome) ok() bool { return len(o.Sample.Fields) > 0 }

// decodeFrame 用接入层解析一帧。
//
// 调用前提：device 已查好（含 Node），merged 是已重组的完整帧，
// commandTemplateID 来自原始 DataEvent。
//
// 返回值语义：
//   - outcome.Attempted == false ⇒ 无 adapter 可处理（no_adapter）
//   - outcome.ok() ⇒ 成功
//   - 否则失败，outcome.FailReason 给出分类原因
func (c *SensorParserConsumer) decodeFrame(device models.EdgeDevice, merged []byte,
	logicalID *uint, at time.Time, commandTemplateID uint64) decodeOutcome {

	if c.ingestResolver == nil {
		// 未接线：按 no_adapter 处理（安全降级 —— 绝不静默丢弃）。
		return decodeOutcome{FailReason: ingest.ReasonNoAdapter}
	}

	// 驱动分支判定：与 adapter 内部使用**同一个函数**，防止
	// 「Supports 认为普通、Decode 走标定」的静默分裂。
	driverKind, driverExists := ingest.ResolveDriverKind(c.driverRegistry, device.Type)

	// 设备配置：与改造前同一次主键查询（ConfigParser 分支本来就无条件查）。
	var parserJSON []byte
	var configName string
	hasDeviceConfig := false
	if device.DeviceConfigID > 0 {
		var dc models.DeviceConfig
		if err := c.db.First(&dc, device.DeviceConfigID).Error; err == nil {
			if len(dc.Parser) > 0 && string(dc.Parser) != "{}" && string(dc.Parser) != "null" {
				hasDeviceConfig = true
				parserJSON = dc.Parser
				configName = dc.Name
			}
		}
	}

	sctx := ingest.SupportContext{
		EdgeDevice:      device,
		HasDeviceConfig: hasDeviceConfig,
		DriverKind:      driverKind,
		DriverExists:    driverExists,
	}

	dctx := ingest.DecodeContext{
		EdgeDevice:       device,
		Raw:              merged,
		LogicalDeviceID:  logicalID,
		At:               at,
		ConfigParserJSON: parserJSON,
		ConfigName:       configName,
		// 惰性：只有 driver 的标定/命令分支才会调用本闭包。
		DriverContextFn: func() *ingest.DriverContext {
			return c.loadDriverContext(device, driverKind, commandTemplateID)
		},
	}

	sample, err := c.ingestResolver.Decode(dctx, sctx)
	if err == nil && len(sample.Fields) > 0 {
		return decodeOutcome{Sample: sample, Attempted: true}
	}

	reason, classified := ingest.ReasonOf(err)
	if !classified {
		reason = ingest.ReasonDecodeFailed
	}
	return decodeOutcome{
		Attempted:  reason != ingest.ReasonNoAdapter,
		FailReason: reason,
		Err:        err,
	}
}

// loadDriverContext 惰性装载驱动分支上下文。
//
// 语义与改造前逐条对齐：
//   - 标定设备缺标定行 ⇒ 返回 nil（adapter 归因 calibration_missing，拒绝落库）
//   - 标定十六进制非法 ⇒ 返回 nil（同上）
//   - 命令驱动缺模板/缺 write_data ⇒ 返回 nil（adapter 归因 command_ctx_missing）
func (c *SensorParserConsumer) loadDriverContext(device models.EdgeDevice,
	kind ingest.DriverKind, commandTemplateID uint64) *ingest.DriverContext {

	switch kind {
	case ingest.DriverKindCalibration:
		var calibration models.CalibrationCache
		if err := c.db.Where("edge_device_id = ? AND device_type = ?", device.ID, device.Type).
			First(&calibration).Error; err != nil {
			logger.Warn("databus: calibration missing; refusing to persist sample",
				"edge_device_id", device.ID, "device_type", device.Type, "error", err)
			return nil
		}
		calBytes, err := hex.DecodeString(calibration.Data)
		if err != nil {
			logger.Warn("databus: calibration encoding invalid; refusing to persist sample",
				"edge_device_id", device.ID, "error", err)
			return nil
		}
		return &ingest.DriverContext{Calibration: calBytes}

	case ingest.DriverKindCommand:
		if commandTemplateID == 0 {
			logger.Infof("[%s] Command-aware driver requires command template context", device.NodeID)
			return nil
		}
		var template models.ConfigTemplate
		if err := c.db.Where("id = ? AND node_id = ?", commandTemplateID, device.NodeID).
			First(&template).Error; err != nil {
			logger.Infof("[%s] Failed to resolve command template %d: %v",
				device.NodeID, commandTemplateID, err)
			return nil
		}
		if template.WriteData == "" {
			logger.Infof("[%s] Command template %d has no write data", device.NodeID, commandTemplateID)
			return nil
		}
		return &ingest.DriverContext{CommandWriteData: template.WriteData}

	default:
		return nil
	}
}

// toParserFields 把 ingest.Field 转回 parser.Field（供既有 sink 消费）。
//
// # 为什么 M1 还要转回去
//
// M1 的目标是「接入层就位、行为零变化」：4 个既有 sink（alert/automation/
// latest/sourceHealth）的签名都吃 []parser.Field。让它们在 M1 就改签名会把
// 接入层改造与消费层改造耦在一起，两个风险叠加。
// M2 再把 sink 迁到 ingest.Sample，届时本函数删除。
func toParserFields(in []ingest.Field) []parser.Field {
	out := make([]parser.Field, len(in))
	for i, f := range in {
		out[i] = parser.Field{Name: f.Name, Value: f.Value, Unit: f.Unit, StringValue: f.StringValue}
	}
	return out
}

// ingestReasonIsConfigDefect 报告该失败原因是否属于「配置缺陷」类。
//
// 用途：consumer 决定日志级别与指标标签 —— 配置缺陷应当 WARN（需要人去补配置），
// 数据问题应当是 INFO（帧本身坏了，重发即可）。
func ingestReasonIsConfigDefect(r ingest.Reason) bool {
	switch r {
	case ingest.ReasonNoAdapter, ingest.ReasonCalibrationMiss, ingest.ReasonCommandCtxMiss:
		return true
	default:
		return false
	}
}
