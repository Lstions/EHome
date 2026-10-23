// Package ingest 是数据流架构的 L1 接入层。
//
// # 职责
//
// 把「任意解析后端的产物」收敛成**唯一**的统一契约 ingest.Sample，
// 让下游（来源层、规则、告警）不再感知具体是哪个解析后端。
//
// # 为什么需要这一层
//
// 现状（2026-10-23 实测）：两条解析路径产出两套类型 ——
// parser.Field 与 drivers.SensorData —— 在 databus consumer 里手工转换
// （consumers_heavy.go:355-360）。后果：新增任何解析后端（Modbus 寄存器表 /
// MQTT JSON / OPC UA）都必须**改 consumer**，耦合点集中在一处。
//
// 本层把「选择哪个后端」与「后端如何解析」都收敛到 Adapter 接口后面，
// consumer 只依赖接口。新增后端 = 新增一个 adapter 文件 + 注册一行。
//
// # 依赖方向（公理 A1：单向）
//
//	databus → ingest → (pkg/parser, internal/drivers, internal/models)
//
// 本包**不得** import internal/databus、internal/api、gorm。
//
// # 热路径纪律（公理 A2）
//
// Supports/Decode 在每帧路径上调用，**不得查库**。设备行等上下文由调用方
// 查好后经 SupportContext/DecodeContext 传入 —— 这也是把 adpter 做成纯判断
// 的原因（可零 DB 单测）。
package ingest

import (
	"time"

	"ehome/backend/internal/models"
)

// Quality 是一次解析的质量。为将来的「部分解析/降级解析」预留。
//
// 当前生产只会产出 QualityOk；另两个值是**契约预留**（前端/审计可依赖其含义），
// 不要在没有真实产出路径时使用它们。
type Quality uint8

const (
	// QualityOk 全部字段解析成功。
	QualityOk Quality = 0
	// QualityPartial 部分字段成功（当前不产出，预留）。
	QualityPartial Quality = 1
	// QualityDegraded 走了降级路径（当前不产出，预留）。
	QualityDegraded Quality = 2
)

// String 用于日志与指标标签（有界枚举，公理 A5）。
func (q Quality) String() string {
	switch q {
	case QualityOk:
		return "ok"
	case QualityPartial:
		return "partial"
	case QualityDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// Field 是统一后的单个传感器字段。
//
// 与 pkg/parser.Field 逐字段等价（刻意保留 StringValue，兼容硬件版本/序列号
// 这类非数值量）。为什么不直接复用 parser.Field：
//
//   - pkg/parser 是**对外契约**，ingest 是 internal 领域层；
//   - 领域层需要独立演进（未来加 Quality/Source 等），复用 pkg 类型会把
//     领域演进泄露成对外兼容负担。
type Field struct {
	Name        string
	Value       float64
	Unit        string
	StringValue string
}

// Sample 是一次成功解析的统一产物，也是**唯一**跨层数据契约。
type Sample struct {
	// EdgeDeviceID 是产出该帧的边缘设备（= unified_data.device_id 口径）。
	EdgeDeviceID uint
	// LogicalDeviceID 是设备绑定的逻辑身份（nil = 未绑定，存量数据兼容）。
	LogicalDeviceID *uint
	// At 是解析时刻。
	At time.Time
	// Fields 解析出的字段集合（非空 —— 空结果按失败处理，见 ReasonEmptyResult）。
	Fields []Field
	// Method 是**自描述**的解析方法字符串，用于日志与审计。
	//
	// 取值必须与改造前的 parseMethod 完全一致（M1 等价性判据）：
	//   "ConfigParser(<name>)" / "Driver(<type>)"
	// 因为它已经进了生产日志与若干审计字段，改格式会造成历史不可比。
	Method string
	// Quality 解析质量。
	Quality Quality
}

// DriverKind 描述设备用哪种驱动解析分支。
//
// 为什么需要它：同一个 Driver 类型可能有三个解析入口（普通 / 标定 / 命令），
// 且**顺序有语义** —— 标定驱动绝不能被通用解析绕过（既有代码的硬约束，
// 见 consumers_heavy.go:270 附近的注释）。把它显式建模，adapter 就能纯判断。
type DriverKind uint8

const (
	// DriverKindPlain 普通驱动（ParseData）。
	DriverKindPlain DriverKind = 0
	// DriverKindCalibration 需要标定字节（ParseDataWithCalibration）。
	DriverKindCalibration DriverKind = 1
	// DriverKindCommand 需要命令上下文（ParseDataWithCommand）。
	DriverKindCommand DriverKind = 2
)

// String 用于指标标签（有界枚举）。
func (k DriverKind) String() string {
	switch k {
	case DriverKindPlain:
		return "plain"
	case DriverKindCalibration:
		return "calibration"
	case DriverKindCommand:
		return "command"
	default:
		return "unknown"
	}
}

// SupportContext 是 Supports 的输入。
//
// 关键设计：**设备行由调用方查好后传入**，adapter 不查库。
// 这样 Supports 是纯判断 —— 可在无 DB 环境下穷举测试所有分支组合。
type SupportContext struct {
	// EdgeDevice 是当前帧所属设备行（已 Preload 必要关联）。
	EdgeDevice models.EdgeDevice
	// HasDeviceConfig 表示该设备配了可用的 DeviceConfig（parser 非空）。
	HasDeviceConfig bool
	// DriverKind 是驱动解析分支（调用方用类型断言算好）。
	DriverKind DriverKind
	// DriverExists 表示驱动注册表里有该 type 的驱动。
	DriverExists bool
}

// DecodeContext 是 Decode 的输入。
type DecodeContext struct {
	// EdgeDevice 是当前帧所属设备行。
	EdgeDevice models.EdgeDevice
	// Raw 是已重组的完整帧字节。
	Raw []byte
	// LogicalDeviceID 是设备绑定的逻辑身份（可为 nil）。
	LogicalDeviceID *uint
	// At 是解析时刻（由调用方给定，保证同一帧内多处使用同一时间）。
	At time.Time
	// ConfigParserJSON 是 DeviceConfig.parser 原始 JSON（仅 ConfigParser adapter 用）。
	ConfigParserJSON []byte
	// ConfigName 是 DeviceConfig.Name（仅用于构造 Method 字符串）。
	ConfigName string
	// DriverContext 只在 DriverKind 非 Plain 时非 nil。
	//
	// ⚠ 优先使用 DriverContextFn（惰性）。保留本字段是为了让单测能直接给值。
	DriverContext *DriverContext

	// DriverContextFn 惰性构造驱动分支上下文。
	//
	// # 为什么必须惰性（不改会造成真实性能/查询回归）
	//
	// 改造前的消费者逻辑：先试 ConfigParser，**只有它失败**才进 driver 分支，
	// 而标定字节/命令模板的查询只发生在 driver 分支内（consumers_heavy.go:296-338）。
	// 若在构造 DecodeContext 时就把这两个查出来，那么「配置解析成功」的绝大多数帧
	// 也会白白多两次 DB 查询 —— 这是热路径，属于不可接受的回归。
	//
	// 因此由调用方提供闭包，adapter 只在真正需要时才调用（标定/命令分支）。
	// 闭包可以返回 nil（例如查询失败），adapter 会按 calibration_missing /
	// command_ctx_missing 归因。
	DriverContextFn func() *DriverContext
}

// DriverCtx 返回驱动分支上下文：优先惰性构造，其次用显式字段。
//
// adapter 应当只通过本方法取值，不要直接访问两个字段（避免漏用惰性路径）。
func (c DecodeContext) DriverCtx() *DriverContext {
	if c.DriverContextFn != nil {
		return c.DriverContextFn()
	}
	return c.DriverContext
}

// DriverContext 是驱动解析分支所需的额外上下文。
type DriverContext struct {
	// Calibration 是标定字节（DriverKindCalibration 时必填）。
	Calibration []byte
	// CommandWriteData 是命令模板的 write_data（DriverKindCommand 时必填）。
	CommandWriteData string
}
