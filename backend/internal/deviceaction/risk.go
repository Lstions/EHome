package deviceaction

// RiskLevel 是边缘设备操作风险等级的**唯一权威类型** (方案 §3.3.3 落点 1)。
//
// 为什么只有两级: 用户裁决把风险收敛为"普通/风险"两级 —— 是否要求人工确认
// 只需要二值信息。驱动层的 4 级 (low/medium/high/critical) 是**出厂方言**,
// 承载严重度排序 (UI 文案 "严重风险" vs "高风险" 用它), 不是系统对外契约;
// 归一化层把方言收敛为契约, 同时不丢失驱动层的严重度信息 (方案 §3.3.3)。
//
// 全仓唯一判定点: "是否需要人工确认"只由 RequiresConfirmation() 回答,
// 其它任何位置都不得再写 risk 字面量比较 (方案 §3.3.4 R3 / §8 A-9 防漂移门禁)。
type RiskLevel string

const (
	// RiskNormal 普通动作: 无需人工确认, 可直接执行。
	RiskNormal RiskLevel = "normal"
	// RiskElevated 风险动作: 默认需要人工确认; 自动化路径可经规则级显式授权
	// (automation_rules.risk_acknowledged_at) 跳过确认门 (O-2 裁决, 实施计划 §0.1)。
	RiskElevated RiskLevel = "elevated"
)

// RequiresConfirmation 是**全仓唯一**的确认要求判定点。
//
// 判定源收敛后, commandexec 的确认门与令牌铸造门都必须读它, 不得再各自
// 内联一份 "medium|high|critical" 字面量比较 (那正是 R3 门禁要抓的漂移)。
func (r RiskLevel) RequiresConfirmation() bool { return r == RiskElevated }

// RiskLevelFromDriver 把驱动的 4 级出厂方言归一化为系统对外的 2 级契约。
//
// 映射表 (方案 §3.3.2, O-5 裁决 medium→风险):
//
//	low      → normal    (无需确认)
//	medium   → elevated  (风险; 既有语义: 人工路径本就要确认)
//	high     → elevated
//	critical → elevated
//
// 完备性 (方案 §8 A-8 / §3.3.4 R4): 四个驱动字面量**全部显式枚举**, 且
// **没有 default 兜底**。未知字面量一律返回 ok=false, 由调用方 fail-closed
// 拒绝 —— 绝不静默归一化为 normal (那会把一个未声明的风险等级变成"无需确认")。
// 因此删掉任一分支都会让映射测试失败, 而不是被 default 悄悄吸收。
func RiskLevelFromDriver(raw string) (RiskLevel, bool) {
	switch raw {
	case "low":
		return RiskNormal, true
	case "medium", "high", "critical":
		return RiskElevated, true
	}
	// 未声明的字面量: fail-closed (这里不是 default 兜底, 是"拒绝"出口)。
	return "", false
}
