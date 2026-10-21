package sensorfield

import (
	"time"
)

// Kind 是选择器的四种形态。
type Kind string

const (
	KindLiteral  Kind = "literal"
	KindFamily   Kind = "family"
	KindSemantic Kind = "semantic"
	KindVirtual  Kind = "virtual"
)

// Aggregate 是 family 选择器的聚合方式。
//
//	any  存在某字段满足条件（默认；与历史单字段行为等价）
//	all  全部字段满足条件（不采用空真：空集为 unbound）
//	min/max/avg/sum  先归约再比较（空集为 unbound；缺员为 unbound）
type Aggregate string

const (
	AggAny Aggregate = "any"
	AggAll Aggregate = "all"
	AggMin Aggregate = "min"
	AggMax Aggregate = "max"
	AggAvg Aggregate = "avg"
	AggSum Aggregate = "sum"
)

// ValidAggregate 报告 a 是否在白名单内（保存期校验用）。
func ValidAggregate(a Aggregate) bool {
	switch a {
	case AggAny, AggAll, AggMin, AggMax, AggAvg, AggSum:
		return true
	}
	return false
}

// ValueType 是字段值的语义类型（来自丰富层；未标注时为空）。
//
// 只有 TypeNumber 参与数值比较；其它类型视为"本帧缺失"。
type ValueType string

const (
	TypeUnknown ValueType = ""
	TypeNumber  ValueType = "number"
	TypeBoolean ValueType = "boolean"
	TypeEnum    ValueType = "enum"
	TypeBitmask ValueType = "bitmask"
	TypeString  ValueType = "string"
	TypeRaw     ValueType = "raw"
)

// Field 是一个字段的描述（声明或观测）。
//
// 单位与类型只用于展示/校验，不参与"是否命中"的判断。
type Field struct {
	Name      string    `json:"name"`
	Unit      string    `json:"unit,omitempty"`
	ValueType ValueType `json:"value_type,omitempty"`
	// LastSeen 仅观测来源有值（声明来源为零值）。
	LastSeen time.Time `json:"last_seen,omitempty"`
	// Samples 仅观测来源有值。
	Samples int64 `json:"samples,omitempty"`
	// Declared / Observed 标明来源（同一字段可能两者都有）。
	Declared bool `json:"declared,omitempty"`
	Observed bool `json:"observed,omitempty"`
}

// Reading 是一帧里的一个读数（等价于 parser.Field 的解析层视图）。
//
// 本包刻意不 import internal/pkg/parser：调用方做一次零成本转换，
// 从而保证 sensorfield 不依赖任何 internal 业务包（红线 2）。
type Reading struct {
	Name  string
	Value float64
	Unit  string
}

// Resolved 是控制面解析结果。
type Resolved struct {
	Selector  string    `json:"selector"`
	Status    string    `json:"status"` // "resolved" | "unbound"
	Kind      Kind      `json:"kind"`
	Aggregate Aggregate `json:"aggregate,omitempty"`
	Matched   []string  `json:"matched"`
	Ambiguous bool      `json:"ambiguous,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// StatusResolved / StatusUnbound 是 Resolved.Status 的仅有两个取值。
const (
	StatusResolved = "resolved"
	StatusUnbound  = "unbound"
)

// EvalResult 是数据面求值结果。
type EvalResult struct {
	Value  float64
	OK     bool   // false = unbound（不触发）
	Reason string // OK=false 时说明原因（落审计用，禁止为空对）
}

// Plan 是一次编译产物：绑定某一 (设备, 选择器, 快照版本)。
//
// 实现必须满足：
//   - 零 DB、零锁、零分配（literal 路径）；
//   - 不可变（并发安全，可被多 shard 同时调用）。
type Plan interface {
	Selector() string
	Kind() Kind
	// Eval 对本帧读数求值。ok=false 表示 unbound（不触发），reason 用于审计。
	Eval(frame []Reading) EvalResult
}

// Observation 是一次观测计数（供 Flush 批量落库）。
type Observation struct {
	DeviceID  uint      `json:"device_id"`
	Name      string    `json:"sensor_name"`
	Unit      string    `json:"unit"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Samples   int64     `json:"samples"`
}

// FlushSink 由调用方实现（把一批观测 UPSERT 到投影表）。
//
// 本包不 import 数据库层：落库是调用方的职责，失败只影响观测滞后，不影响求值。
type FlushSink interface {
	UpsertObservations(obs []Observation) error
}

// AliasRule 是一条语义别名规则。
type AliasRule struct {
	Semantic     string   `json:"semantic"`      // 语义名（@ 后面的部分）
	Candidates   []string `json:"candidates"`    // 物理名候选（有序）
	ExpectedUnit string   `json:"expected_unit"` // 期望单位类；候选单位不同类 ⇒ 拒绝加载
	AllowOverlap bool     `json:"allow_overlap"` // 同一物理名允许归属多个语义
}

// VirtualDef 是一个派生字段定义。
type VirtualDef struct {
	Name string   `json:"name"` // 不含 "virtual:" 前缀
	Fn   string   `json:"fn"`   // product/sum/diff/abs_diff/ratio/scale/max_of/min_of/sum_of
	Args []string `json:"args"` // 选择器串（可含 @ / literal / family）
	// Scale 仅 fn=scale 使用。
	Scale float64 `json:"scale,omitempty"`
}

// Config 是解析层的构造参数（零值字段取默认值）。
type Config struct {
	Enabled bool `yaml:"enabled"`
	// MaxSelectorChars 选择器最大字符数（默认 64）。
	MaxSelectorChars int `yaml:"max_selector_chars"`
	// MaxFamilySize 单族展开上限（默认 64）。
	MaxFamilySize int `yaml:"max_family_size"`
	// MaxVirtualDepth 派生嵌套深度上限（默认 4）。
	MaxVirtualDepth int `yaml:"max_virtual_depth"`
	// SnapshotTTL 编译缓存兜底 TTL（默认 10m）。
	SnapshotTTL time.Duration `yaml:"-"`
}
