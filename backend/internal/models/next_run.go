package models

import "time"

// =====================================================================
// 自动化策略「下次执行时间」(只读计算字段, **不落库**)
// =====================================================================
//
// 需求: 已创建的规则在列表页直接显示"下次什么时候会执行"。
//
// 为什么是**计算字段**而不是 automation_rules 的新列:
//  1. 该值随时间推进自然变化, 落库就必须有 ticker 定期刷新 —— 每条规则每分钟
//     一次 UPDATE, 而 evaluator 的 LoadRules 每 30s 重载并做字段比对
//     (evaluator.go 的 sameWindowStateFields 白名单**排除** UpdatedAt 正是为了
//     挡住这种抖动), 写放大只会把"干净重载"变成"每 30s 清一次边沿状态";
//  2. 两次 tick 之间列里的值仍然是过期的, 精度并不比现算更高;
//  3. 现算是纯函数 + 一次分组查询, 成本可忽略, 且永远与求值器同一时刻口径。
//
// 为什么不放在 automation 包: models 不能反向依赖 automation(包环), 而
// AutomationRule 需要承载该字段(与 Actions 的 gorm:"-" 同款模式)。计算实现
// 在 automation/nextrun.go, 本文件只提供类型与状态/来源常量。

// NextRunStatus 是"下次执行时间"的状态机 (取值域)。
//
// 为什么必须显式区分状态而不是"时间 + 空值":
//   - sensor_threshold 是数据驱动, **根本不存在**可预测的时刻 —— 给一个时间
//     就是编数据; 给空值则无法区分"暂无"与"不可预测";
//   - 停用/未实现/极昼极夜/配置非法各自需要不同的界面措辞与处置建议,
//     合并成一个 None 会让用户无从判断该做什么。
const (
	// NextRunScheduled 有确定的下次时刻 (At 非空)。
	NextRunScheduled = "scheduled"
	// NextRunWaitingData 数据驱动触发器: 由设备上报决定, 无法预测时刻。
	// At 仅在"当前处于冷却期"时给出冷却到期时刻, 否则为空。
	NextRunWaitingData = "waiting_data"
	// NextRunNoEvent 极昼/极夜: 本日本地日不存在该日出/日落事件。
	NextRunNoEvent = "no_event"
	// NextRunDisabled 规则已停用 (enabled=false)。
	NextRunDisabled = "disabled"
	// NextRunUnsupported 触发器类型尚未实现 (event 占位)。
	NextRunUnsupported = "unsupported"
	// NextRunUnavailable 配置非法或解析失败 (坐标/时区/窗口格式)。
	NextRunUnavailable = "unavailable"
)

// NextRunBasis 说明 At 这个时刻是怎么来的 (前端据此区分"计划时刻"与"被顺延")。
const (
	NextRunBasisSun        = "sun"         // 日出/日落 + 偏移
	NextRunBasisWindow     = "window"      // 时间窗口边界
	NextRunBasisCooldown   = "cooldown"    // 被冷却顺延 (冷却到期才可能再触发)
	NextRunBasisDailyLimit = "daily_limit" // 被日熔断顺延 (次日 00:00 恢复)
)

// NextRun 是一条规则「下次执行时间」的计算结果。
//
// 契约 (与 frontend-shared/src/api/automation.ts 的 NextRun 逐字对齐):
//   - At 一律 RFC3339 **带偏移**, 绝不返回无时区的裸时间;
//   - At 非空仅在两种状态: scheduled (必有) 与 waiting_data (仅"当前在冷却期"
//     时给冷却到期时刻 —— 那是唯一能诚实说出的时刻)。其余状态一律 nil,
//     避免"已停用的规则也有下次执行时间"这种自相矛盾的展示;
//   - ComputedAt 必带，但**只用于排障与未来消费**: 前端用本地 30s ticker
//     推进「即将触发」，不读 computed_at（2026-11-18 实测确认，旧文档说反了）。
type NextRun struct {
	Status string `json:"status"`
	// At 是"下一次可能真正触发"的绝对时刻 (含冷却/日熔断顺延)。
	At *time.Time `json:"at,omitempty"`
	// Timezone 是 At 的展示时区 (IANA 名)。sun_event 与 time_window 都用
	// **规则时区**（2026-11-18 D7 起 time_window 也按规则时区执行）；只有
	// sensor_threshold 用后端进程时区（数据驱动，没有规则级时钟语义）。
	// 前端必须按它格式化，否则跨时区规则会显示成另一个钟面值。
	Timezone string `json:"timezone,omitempty"`
	// Basis 见 NextRunBasis*。
	Basis string `json:"basis,omitempty"`
	// Detail 是人类可读补充 (如 "日落 17:41"、"冷却中，顺延 25 秒")。
	Detail string `json:"detail,omitempty"`
	// ComputedAt 是计算时刻。
	ComputedAt time.Time `json:"computed_at"`
}
