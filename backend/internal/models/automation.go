package models

import (
	"encoding/json"
	"time"
)

// =====================================================================
// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)
// =====================================================================
//
// 语义边界: 与 alert（阈值告警，人感知异常）并存不合并。
// automation 解决"条件满足→系统自动处置"，告警是其 action=notification 的退化特化。
// 动作执行一律复用 commandexec.Service.Create（9 项 availability gate + 幂等 + 审计），
// 本模型只承载"何时触发、触发后做什么"的声明式描述。

// 触发器类型 (TriggerType 取值)
const (
	AutomationTriggerSensorThreshold = "sensor_threshold" // 数据驱动: 滑动窗口连续满足
	AutomationTriggerTimeWindow      = "time_window"      // 时钟驱动: 每日窗口 enter/exit
	AutomationTriggerEvent           = "event"            // 事件驱动 (本期仅占位, 未实现)
)

// 动作类型 (ActionType 取值)
const (
	AutomationActionDeviceAction = "device_action" // 走 commandexec 受控操作链路
	AutomationActionNotification = "notification"  // 仅生成通知 (退化告警)
)

// 时间窗口触发边沿 (TriggerWindowEdge 取值)
const (
	AutomationWindowEnter  = "enter"  // 进入窗口瞬间触发一次
	AutomationWindowExit   = "exit"   // 离开窗口瞬间触发一次
	AutomationWindowInside = "inside" // 窗口内每个 tick 参与求值 (受 CooldownSec 抑制)
)

// 触发/执行结果 (AutomationEvent.Result 取值)
const (
	AutomationResultExecuted             = "executed"               // 已提交 commandexec 执行
	AutomationResultPendingConfirm       = "pending_confirm"        // 高风险动作, 等待人工确认
	AutomationResultSuppressedCooldown   = "suppressed_cooldown"    // 冷却期内抑制
	AutomationResultSuppressedDailyLimit = "suppressed_daily_limit" // 达到每日熔断上限
	AutomationResultConditionChanged     = "condition_changed"      // 触发到执行间条件失效
	AutomationResultFailedGate           = "failed_gate"            // availability gate fail-closed
	AutomationResultFailedDispatch       = "failed_dispatch"        // Create 调用失败
	AutomationResultNotification         = "notification"           // 纯通知动作已发出
	AutomationResultExpired              = "expired"                // pending_confirm 超时未确认 (24h 清扫置位)
)

// 触发来源 (AutomationEvent.TriggerSource 取值)
// 手动触发走 POST /api/v1/automation-rules/:id/trigger, 跳过条件评估与确认制
// (用户点击即确认), 但仍计入 cooldown / max_daily_exec (防误连点)。
const (
	AutomationTriggerSourceAuto   = "auto"   // 求值器/ticker 自动触发 (默认)
	AutomationTriggerSourceManual = "manual" // 手动触发 (POST /automation-rules/:id/trigger)
)

// AutomationCondition 附加条件 (全部 AND 求值, ConditionsJSON 内嵌数组)。
type AutomationCondition struct {
	SensorName string  `json:"sensor_name"` // 与 parser.Field.Name 同域
	Comparator string  `json:"comparator"`  // gt|gte|lt|lte|eq|neq
	Threshold  float64 `json:"threshold"`
}

// ParseConditions 解析条件数组 (fail-closed: 非法 JSON 返回错误)。
func (r AutomationRule) ParseConditions() ([]AutomationCondition, error) {
	if r.ConditionsJSON == "" || r.ConditionsJSON == "[]" {
		return nil, nil
	}
	var conds []AutomationCondition
	if err := json.Unmarshal([]byte(r.ConditionsJSON), &conds); err != nil {
		return nil, err
	}
	return conds, nil
}

// ParseActionParams 解析动作参数。
func (r AutomationRule) ParseActionParams() (json.RawMessage, error) {
	if r.ActionParamsJSON == "" {
		return json.RawMessage("{}"), nil
	}
	raw := json.RawMessage(r.ActionParamsJSON)
	if !json.Valid(raw) {
		return nil, errInvalidAutomationParams
	}
	return raw, nil
}

var errInvalidAutomationParams = errorString("invalid action_params json")

type errorString string

func (e errorString) Error() string { return string(e) }

// AutomationRule 自动化策略规则 (ECA 模型: Trigger → Condition → Action)。
//
// 字段组合约束 (创建/更新时 fail-closed 校验, 详见 handler_automation.go):
//   - sensor_threshold: TriggerSensorName/Comparator/Threshold 必填, DurationSec ≥ 0
//   - time_window: TriggerWindowStart/End (HH:MM) 必填, TriggerWindowEdge ∈ {enter,exit,inside}
//   - device_action: ActionDeviceID/ActionID 必填, ActionParamsJSON 必须过 CanonicalizeParams
//   - notification: ActionLevel 必填
type AutomationRule struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Enabled 默认启用语义: GORM 的 default:true tag 会把 bool 零值 false 在 INSERT
	// 时序列化为 SQLite 字面量 true (2026-08-23 探针实锤, alert.go 同缺陷) ——
	// 禁用规则被静默存为启用。因此此处不带 default tag, 默认 true 由应用层
	// (handler_automation.go Create) 显式赋值, fail-closed 不依赖 DB 默认值。
	Enabled bool `gorm:"index" json:"enabled"`

	// ── Trigger ──
	TriggerType         string  `gorm:"size:24;not null;index" json:"trigger_type"`
	TriggerSensorName   string  `gorm:"size:64;index" json:"trigger_sensor_name,omitempty"`
	TriggerComparator   string  `gorm:"size:8" json:"trigger_comparator,omitempty"`
	TriggerThreshold    float64 `json:"trigger_threshold,omitempty"`
	TriggerDurationSec  int     `gorm:"default:0" json:"trigger_duration_sec"`
	TriggerWindowStart  string  `gorm:"size:5" json:"trigger_window_start,omitempty"` // "HH:MM"
	TriggerWindowEnd    string  `gorm:"size:5" json:"trigger_window_end,omitempty"`   // "HH:MM"
	TriggerWindowEdge   string  `gorm:"size:8" json:"trigger_window_edge,omitempty"`
	TriggerEdgeDeviceID uint    `gorm:"index" json:"trigger_edge_device_id,omitempty"` // sensor_threshold 目标设备 (0=任意设备上报该字段即触发, 不推荐)

	// ── Conditions (全部 AND) ──
	ConditionsJSON string `gorm:"type:text" json:"conditions_json,omitempty"`

	// ── Action ──
	ActionType       string `gorm:"size:20;not null" json:"action_type"`
	ActionDeviceID   uint   `gorm:"index" json:"action_device_id,omitempty"`
	ActionID         string `gorm:"size:96" json:"action_id,omitempty"`
	ActionParamsJSON string `gorm:"type:text" json:"action_params_json,omitempty"`
	ActionLevel      string `gorm:"size:10" json:"action_level,omitempty"` // notification 级别

	// ── 执行约束 ──
	//
	// CooldownSec 冷却期 (防抖第二层), 0=不冷却 (与 MaxDailyExec 的 0=不限同族)。
	//
	// 不得带 gorm:"default:300": 带 default 的 int 列无法表达"显式零值" ——
	// GORM 的 Create 把零值当"未设置"省略该列, 由 schema 默认值回填 300, 用户
	// 显式填的 0 根本写不进库 (同款陷阱: notification_channel.go 的 max_retries、
	// alert.go 的 enabled, 见 docs/设计/外发通知通道.md §7.6)。
	// "未配置 = 300" 的默认值职责由应用层承担 (handler_automation.go Create 显式
	// 赋值), 与 Enabled 同理: fail-closed 不依赖 DB 默认值。
	CooldownSec      int  `gorm:"not null;default:0" json:"cooldown_sec"`
	RequireConfirmed bool `gorm:"default:false" json:"require_confirmed"` // true=仅生成待确认通知
	MaxDailyExec     int  `gorm:"default:0" json:"max_daily_exec"`        // 每日执行上限, 0=不限 (熔断)

	// ── 冷却锚点 (清理前置条件 A, docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md) ──
	//
	// 冷却窗 (armed→triggered) 的持久锚点: 该规则"上一次真正触发"的时刻。
	//
	// 为什么必须是独立列而不是继续靠 automation_events 回填: 冷却态只在
	// evaluator 内存里 (evaluator.go triggered map), 重启后靠
	// "SELECT MAX(triggered_at) FROM automation_events WHERE result='executed'"
	// 回填。事件表一旦启用保留策略清理, 旧 executed 行被删 => 重启后该规则被当作
	// "从未触发"(armed) => 条件仍满足时立即重触发 => 真实设备动作多发。
	// 锚点必须与事件表【物理解耦】, 否则"删审计"会变成"改变系统行为"。
	//
	// 类型为 *time.Time (可空): 必须能表达"从未触发"。零值 time.Time 会让
	// "从未触发"与"1970 年触发过"不可区分, 且 now.Sub(零值) 是巨大正数,
	// 一旦写成 "now.Sub(anchor) < cooldown" 就会把新规则静默永久冷却。
	//
	// 不加 gorm default (同 CooldownSec/Enabled 的既有教训): 默认值会吞掉显式零值语义。
	// 语义边界: 记录的是【触发】时刻 (evaluator armed→triggered 的 at), 不是命令下发成功时刻
	// —— 旧回填用 executed 行只是近似, 该列把近似变成确定值。
	LastTriggeredAt *time.Time `gorm:"index" json:"last_triggered_at,omitempty"` // 只读: 由触发路径同事务写入, API 不接受外部赋值

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `gorm:"index" json:"-"` // 软删, 保留历史归因
}

func (AutomationRule) TableName() string { return "automation_rules" }

// AutomationEvent 策略触发/执行审计 (一行 = 一次触发决策)。
// CommandID 回填关联 command_executions, 策略执行历史 = automation_events JOIN command_executions。
// TriggerSource 区分自动触发 (auto, 求值器/ticker) 与手动触发 (manual, 用户点击"立即触发")。
type AutomationEvent struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	RuleID        uint      `gorm:"not null;index" json:"rule_id"`
	TriggeredAt   time.Time `gorm:"not null;index" json:"triggered_at"`
	TriggerValue  *float64  `json:"trigger_value,omitempty"`                                  // sensor_threshold 触发时值
	TriggerSource string    `gorm:"size:8;not null;default:auto;index" json:"trigger_source"` // auto|manual
	Result        string    `gorm:"size:32;not null;index" json:"result"`
	CommandID     string    `gorm:"size:36;index" json:"command_id,omitempty"` // FK→command_executions (执行时回填)
	Detail        string    `gorm:"size:512" json:"detail,omitempty"`          // 失败/抑制原因
	CreatedAt     time.Time `json:"created_at"`
}

func (AutomationEvent) TableName() string { return "automation_events" }
