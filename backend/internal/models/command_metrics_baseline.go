package models

import "time"

// CommandMetricsBaseline 命令域监控计数基线 (单行表, 恒 id=1)。
//
// 清理前置条件 B, 见 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §2.2。
//
// 为什么需要它: api/handler_metrics.go 的 control.* 指标全是【对 command_executions
// 做无时间窗 COUNT(*)】(整段无 created_at/Since/Between 谓词)。命令域一旦启用保留策略,
// 删掉旧的 FAILED/UNKNOWN 行会让面板上"操作失败 9"变"0"、Monitor.vue:141/160 的
// attention 告警高亮消失 —— 看起来像故障自愈, 实际是证据被删。
//
// 语义: 本表保存【已被清理器删除的那部分行】的累计计数; 面板读 = 存活行 COUNT(*) + 基线。
// 因此"累计"这个语义原样保住 (与面板文案、前端契约、既有测试断言逐字一致)。
//
// 只覆盖 5 个【事件累计量】: operations_total / succeeded / failed / unknown / cancelled。
// 【不覆盖 unresolved_unknown】: 它是【集合成员数】(当前 UNKNOWN 且尚未被人工处置),
// 本来就会因为有人处置而下降, 不是累计量。给它做基线会让"未处置异常"永久虚高
// (被删的行再也无法被处置 —— commandexec/service.go:594-600 的处置入口必须先加载该行),
// 用户怎么处置都降不下来。正确做法是【不删未处置的 UNKNOWN】(上游 §2.4)。
//
// 生命周期: 单行 upsert (id 恒 1), 表不可能增长。清理器未上线时行为恒等于今天 (基线为 0)。
type CommandMetricsBaseline struct {
	// ID 恒为 1 —— 单行表, 由 upsert 保证; 不用自增主键避免出现第二行。
	ID uint `gorm:"primaryKey" json:"id"`

	// OperationsTotal 已删除的执行行总数 (清理器每次删除时按行数累加)。
	OperationsTotal int64 `gorm:"not null;default:0" json:"operations_total"`
	// Succeeded/Failed/Unknown/Cancelled 已删除行按终态分桶的累计数。
	// 非终态行 (QUEUED/DISPATCHED/DEVICE_ACCEPTED/VERIFYING) 【永不删】, 故不设对应列:
	// 清理器只删终态行 (上游 INV-4), 无桶可累加。
	Succeeded int64 `gorm:"not null;default:0" json:"succeeded"`
	Failed    int64 `gorm:"not null;default:0" json:"failed"`
	Unknown   int64 `gorm:"not null;default:0" json:"unknown"`
	Cancelled int64 `gorm:"not null;default:0" json:"cancelled"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (CommandMetricsBaseline) TableName() string { return "command_metrics_baselines" }
