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
	// AutomationResultConfirmationRequired 是"被确认门拦住"这一**预期安全行为**
	// 的专属结果码 (方案 §3.2.5 第 1 步 / §8 A-11)。
	//
	// 为什么必须与 failed_dispatch 分开: 改造前自动路径遇确认门会落 failed_dispatch,
	// 与"传输失败"在审计上无法区分, 前端也拿不到任何可行动信号 (C5 缺陷)。
	// 确认门拒绝不是故障, 而是"需要人来确认"—— 分开后前端可据此弹提示并跳转确认入口。
	AutomationResultConfirmationRequired = "confirmation_required" // 被确认门拦住 (需人工确认或规则未授权)
	// AutomationResultPartialFailure 是"多动作执行到第 k 个失败、剩余动作已中止"的
	// 专属结果码 (方案 §3.2.4 整体结果 / §6.3 多动作执行状态机, 实施计划 §2 B2)。
	//
	// 为什么必须与 failed_dispatch 分开: 部分成功是**危险状态**——设备已被改了一半
	// (例如"关充电 MOS"成功、"关放电 MOS"失败), 需要人工介入。归入 failed_dispatch
	// 会让前端把它显示成"完全失败", 用户误以为设备没被动过。事件行的 action_seq
	// 记录失败发生在第几个动作, 供前端精确展示。
	AutomationResultPartialFailure = "partial_failure" // 第 k 个动作失败且 k<N (已中止剩余动作)
	AutomationResultNotification   = "notification"    // 纯通知动作已发出
	AutomationResultExpired        = "expired"         // pending_confirm 超时未确认 (24h 清扫置位)
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

	// ── 风险动作的规则级显式授权 (用户裁决 O-2, 实施计划 §0.1) ──
	//
	// 语义: 该规则**已经过用户在保存时显式确认**，允许其风险动作在自动路径上
	// 跳过人工确认直接执行。NULL = 未授权 —— 风险动作在自动路径上仍会落
	// confirmation_required，绝不会静默执行。
	//
	// 为什么是时间戳而不是 bool: 审计需要回答"何时被授权的"。bool 只能回答
	// "是否授权过"，且 GORM 的 default 陷阱 (见 Enabled/CooldownSec 的既有教训)
	// 会让 false 无法表达"显式拒绝"。
	//
	// 授权失效 (安全关键, 实施计划 §0.1): 规则的**动作集合发生变化**时必须清空该列
	// (见 handler_automation.go 的 updateAutomationRule)，强制用户重新确认。否则
	// 用户授权了动作 A, 之后把动作改成 B, 旧授权会静默地把 B 也放行。
	//
	// 只读语义: 由保存路径写入/清空, 不接受请求体直接赋值 (API 请求结构体里没有
	// 该字段, 因此客户端无法自行授权)。
	RiskAcknowledgedAt *time.Time `json:"risk_acknowledged_at,omitempty"`

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
	// ActionSeq 记录多动作执行中"失败/部分失败发生在第几个动作" (方案 §5.1.4)。
	//
	// 为什么可空: 规则级事件 (suppressed_daily_limit / pending_confirm / 纯通知等)
	// 没有动作维度, NULL 表达"无动作维度", 而不是用 0 冒充"第 0 个动作"。
	// 多动作下的失败事件才填 seq (1 起, 与 automation_rule_actions.seq 同域)。
	ActionSeq *int      `json:"action_seq,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (AutomationEvent) TableName() string { return "automation_events" }

// AutomationRuleAction 规则动作子表 (方案 §3.2.3 推荐 M1 独立子表 / §5.1.1)。
//
// 为什么用子表而不是 JSON 列: 顺序语义 (seq) 需要 DB 级唯一约束兜底, 部分失败
// 归因需要"第 k 个动作"可被 SQL 直接查询, 幂等键需要动作维度天然唯一
// (方案 §3.2.2 逐项对比)。子表同时让每个动作独立过 CanonicalizeParams 校验。
//
// 兼容期 (方案 §3.4 D2): automation_rules 的单动作四列**保留但降级为只读镜像**,
// 由写入路径双写, 读路径以本表为准。回滚时镜像列仍持有最后一次写入的动作。
type AutomationRuleAction struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	RuleID uint `gorm:"not null;index;uniqueIndex:idx_automation_rule_actions_rule_seq,priority:1" json:"rule_id"`
	// Seq 动作序号 (1 起, 同规则内唯一, 升序串行执行)。
	// 唯一索引 (rule_id, seq) 是"顺序"这一语义的物理保证 (方案 §3.2.3 推荐理由 2)。
	Seq              int    `gorm:"not null;uniqueIndex:idx_automation_rule_actions_rule_seq,priority:2" json:"seq"`
	ActionType       string `gorm:"size:20;not null" json:"action_type"`
	ActionDeviceID   uint   `gorm:"index" json:"action_device_id,omitempty"`
	ActionID         string `gorm:"size:96" json:"action_id,omitempty"`
	ActionParamsJSON string `gorm:"type:text" json:"action_params_json,omitempty"`
	ActionLevel      string `gorm:"size:10" json:"action_level,omitempty"` // notification 级别
	// Enabled 单动作级开关 (方案 §5.1.1)。不带 default tag —— 与
	// AutomationRule.Enabled 同款 GORM 零值陷阱, 默认 true 由写入路径显式赋值。
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AutomationRuleAction) TableName() string { return "automation_rule_actions" }

// AutomationActionFromRule 把规则的单动作镜像列组装成 seq=1 的动作。
//
// 用途 (双写兼容期 + 读路径回落):
//   - 写入规则时若无 actions 子表输入, 用本函数得到"唯一那个动作"再落子表,
//     保证子表与镜像列一致;
//   - 读取时若子表为空 (回填尚未跑到的历史库), 用本函数得到等价动作集合,
//     读路径不依赖迁移是否已执行。
func AutomationActionFromRule(rule AutomationRule) AutomationRuleAction {
	return AutomationRuleAction{
		RuleID:           rule.ID,
		Seq:              1,
		ActionType:       rule.ActionType,
		ActionDeviceID:   rule.ActionDeviceID,
		ActionID:         rule.ActionID,
		ActionParamsJSON: rule.ActionParamsJSON,
		ActionLevel:      rule.ActionLevel,
		Enabled:          true,
	}
}

// ApplyMirrorAction 把动作写回规则的单动作镜像列 (双写兼容期, 方案 §3.4 D2)。
//
// 语义: 镜像列恒等于 seq 最小的动作 —— 老客户端/老读路径只看这四列时, 行为与
// 改造前一致。子表是权威, 镜像只是兼容期投影。
func (a AutomationRuleAction) ApplyMirrorAction(rule *AutomationRule) {
	if rule == nil {
		return
	}
	rule.ActionType = a.ActionType
	rule.ActionDeviceID = a.ActionDeviceID
	rule.ActionID = a.ActionID
	rule.ActionParamsJSON = a.ActionParamsJSON
	rule.ActionLevel = a.ActionLevel
}
