package automation

// 多动作执行的门禁 (方案 §3.2.4 运行时语义 / §6.3 状态机 / §8 A-4/A-5/A-6/A-15/A-16)。
//
// 每条都写明反向验证 (注入 → 必须红), 并在 commit 正文里给出命令与输出:
//
//	A-4 顺序: 把 ruleActions 的 Order("seq ASC") 改成 DESC → TestMultiActionExecutesInSeqOrder 红
//	A-5 幂等: 去掉 idemKey 的 action_seq 段 → TestMultiActionSameActionIDNotSwallowed 红
//	A-6 部分失败: 去掉 recordActionFailure 的 executed>0 分支 → TestMultiActionPartialFailureAbortsRemaining 红
//	A-15 日熔断: 把阈值改成 MaxDailyExec*len(actions) → TestMultiActionDailyLimitCountsByRule 红
//	A-16 冷却锚点: 把 recordExecuted 的 at 改成 time.Now() → TestMultiActionCooldownAnchorIsTriggerTime 红

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// multiActionRule 造一条自动触发 + 多动作的规则 (无冷却/无限额, 走真实 commandexec)。
// 需要 periph 链路: gpio_set 目标解析用 node id (setupConfirmPlanner 里 edge.ID == node.ID)。
func multiActionRule(t *testing.T, pl *Planner, edgeID uint, name string, actionCount int) models.AutomationRule {
	t.Helper()
	// gpio_set 的 periph gate 要求目标 node 上有对应 pin 的 GPIOConfig。
	for pin := 5; pin < 5+actionCount; pin++ {
		if err := pl.db.Create(&models.GPIOConfig{
			NodeID: "node-conf", Pin: pin, Direction: 1, Enabled: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ackAt := time.Now().UTC()
	rule := models.AutomationRule{
		Name:                name,
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edgeID,
		TriggerSensorName:   "humidity",
		TriggerComparator:   "gt",
		TriggerThreshold:    80,
		CooldownSec:         0,
		RequireConfirmed:    false,
		// 风险动作的规则级授权 (O-2): gpio_set 是 medium, 未授权会被确认门拦住。
		RiskAcknowledgedAt: &ackAt,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edgeID,
		ActionID:           "gpio_set",
		ActionParamsJSON:   `{"pin":5,"level":1}`,
	}
	if err := pl.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return rule
}

// addAction 给规则追加一个 seq 的动作 (绕过 API, 直接落子表 —— 本文件测 planner)。
func addAction(t *testing.T, pl *Planner, ruleID uint, seq int, actionID, params string) {
	t.Helper()
	if err := pl.db.Create(&models.AutomationRuleAction{
		RuleID: ruleID, Seq: seq, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: actionID, ActionParamsJSON: params, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

// recordPinOrder 记录 action_executions 的**真实调用顺序** (每次 Create 成功时追加)。
//
// 为什么必须记录调用顺序, 而不是查库后排序: 三个动作在同一毫秒内落库, 查询侧
// 没有任何可靠的全序判据 (created_at 精度不够; rowid 是 SQLite 方言; 幂等键里的
// action_seq 是 planner 写进去的, 与"谁先被调用"无关)。第一版判据正是排序式的,
// 反向验证时把 Order("seq ASC") 改成 DESC 仍然绿 —— 那是假绿, 已废弃。
//
// 用 GORM 的 Create 回调记录: 回调按语句执行顺序同步触发, 因此追加顺序就是
// planner 的调用顺序。
func recordPinOrder(t *testing.T, pl *Planner) *[]int {
	t.Helper()
	order := &[]int{}
	err := pl.db.Callback().Create().After("gorm:create").Register("test:record-pin-order",
		func(tx *gorm.DB) {
			if tx.Statement.Table != "command_executions" {
				return
			}
			row, ok := tx.Statement.Dest.(*models.CommandExecution)
			if !ok {
				return
			}
			var params struct {
				Pin int `json:"pin"`
			}
			if err := json.Unmarshal([]byte(row.ParamsJSON), &params); err != nil {
				return
			}
			*order = append(*order, params.Pin)
		})
	if err != nil {
		t.Fatal(err)
	}
	return order
}

// execRows 取回全部 command_executions (无序 —— 需要顺序的断言请用 recordPinOrder)。
func execRows(t *testing.T, pl *Planner) []models.CommandExecution {
	t.Helper()
	var rows []models.CommandExecution
	if err := pl.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// commandPinOrder 返回该规则触发生成的 command_executions 的 pin 集合, 顺序不定。
func commandPinOrder(t *testing.T, pl *Planner) []int {
	t.Helper()
	rows := execRows(t, pl)
	order := make([]int, 0, len(rows))
	for _, r := range rows {
		var params struct {
			Pin int `json:"pin"`
		}
		if err := json.Unmarshal([]byte(r.ParamsJSON), &params); err != nil {
			t.Fatalf("解析 params 失败: %v (%s)", err, r.ParamsJSON)
		}
		order = append(order, params.Pin)
	}
	sort.Ints(order)
	return order
}

// ─── A-4: 多动作按 seq 升序串行执行 ───────────────────────────────────────
func TestMultiActionExecutesInSeqOrder(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "三动作顺序", 3)
	// 注意: 子表 seq 与 pin 刻意**错位** (seq 1→pin 7, seq 2→pin 6, seq 3→pin 5),
	// 这样"按 seq 升序"与"按 pin 升序"是两个可区分的判据 —— 否则顺序错了
	// 也可能因参数恰好递增而假绿。
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":7,"level":1}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":6,"level":1}`)
	addAction(t, pl, rule.ID, 3, "gpio_set", `{"pin":5,"level":1}`)

	// 记录**真实调用顺序** (回调按语句执行顺序追加), 而不是查库后排序 —— 见
	// recordPinOrder 的说明: 同毫秒落库让查询侧没有可靠的全序判据, 第一版
	// 排序式判据在"倒序执行"的注入下仍然绿, 属假绿。
	callOrder := recordPinOrder(t, pl)
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	want := []int{7, 6, 5}
	if len(*callOrder) != len(want) {
		t.Fatalf("实际执行的动作数=%d want %d (%v)", len(*callOrder), len(want), *callOrder)
	}
	for i := range want {
		if (*callOrder)[i] != want[i] {
			t.Fatalf("第 %d 个被调用执行的 pin=%d want %d —— 必须严格按 seq 升序串行 (%v)",
				i+1, (*callOrder)[i], want[i], *callOrder)
		}
	}
	if got := commandPinOrder(t, pl); len(got) != len(want) {
		t.Fatalf("command_executions 行数=%d want %d", len(got), len(want))
	}
	// 规则级只落一条事件 (方案 §3.2.4 审计)。
	var events int64
	pl.db.Model(&models.AutomationEvent{}).Where("rule_id = ?", rule.ID).Count(&events)
	if events != 1 {
		t.Fatalf("automation_events 行数=%d want 1 (一行 = 一次触发决策)", events)
	}
}

// ─── A-5: 同设备同 action_id 的两个动作不被幂等 replay 吞掉 ───────────────
//
// 方案 §3.2.4 的完整反例: 两个动作都指向同设备同 action_id (gpio_set), 参数不同
// (pin 5 / pin 6)。幂等 scope 完全相同 —— 若幂等键没有 action_seq 段, 第二个动作
// 会命中第一行的幂等键且 RequestHash 不同 → ErrIdempotencyCollision, 规则误报失败
// 且只执行了一个动作。必须产生 2 行 command_executions。
func TestMultiActionSameActionIDNotSwallowed(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "同动作两次", 2)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":6,"level":1}`)

	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	got := commandPinOrder(t, pl)
	if len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("同设备同 action_id 的两个动作必须各产生一行 command_executions (pin 集合 [5 6]), 实际 %v —— 幂等键缺 action_seq 段会吞掉第二个", got)
	}
	var ev models.AutomationEvent
	pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev)
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("result=%q want executed (detail=%s) —— 第二个动作被幂等吞掉时会落失败", ev.Result, ev.Detail)
	}
}

// ─── A-6: 部分失败落 partial_failure 且 action_seq 正确, 剩余动作不执行 ───
func TestMultiActionPartialFailureAbortsRemaining(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "部分失败", 3)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	// seq=2 用目录里不存在的 action_id → Create 返回 ErrActionUnavailable (failed_gate)。
	addAction(t, pl, rule.ID, 2, "no_such_action", `{}`)
	addAction(t, pl, rule.ID, 3, "gpio_set", `{"pin":6,"level":1}`)

	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	var ev models.AutomationEvent
	if err := pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Result != models.AutomationResultPartialFailure {
		t.Fatalf("result=%q want partial_failure (第 2/3 个动作失败, detail=%s)", ev.Result, ev.Detail)
	}
	if ev.ActionSeq == nil || *ev.ActionSeq != 2 {
		t.Fatalf("action_seq=%v want 2", ev.ActionSeq)
	}
	// 第 1 个动作已执行 (1 行), 第 3 个动作必须**未执行** (失败中止剩余)。
	got := commandPinOrder(t, pl)
	if len(got) != 1 || got[0] != 5 {
		t.Fatalf("command_executions=%v want [5] —— 失败后必须中止剩余动作 (第 3 个的 pin=6 绝不出现), 且已执行的不回滚", got)
	}
}

// 反向对照: 第 1 个动作就失败 → 完全失败 (按原因), 不是 partial_failure。
func TestMultiActionFirstFailureIsNotPartial(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "首个失败", 2)
	addAction(t, pl, rule.ID, 1, "no_such_action", `{}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":5,"level":1}`)

	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	var ev models.AutomationEvent
	pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev)
	if ev.Result != models.AutomationResultFailedGate {
		t.Fatalf("result=%q want failed_gate (第 1 个动作失败 = 完全失败, 按原因)", ev.Result)
	}
	if ev.ActionSeq == nil || *ev.ActionSeq != 1 {
		t.Fatalf("action_seq=%v want 1", ev.ActionSeq)
	}
	if got := commandPinOrder(t, pl); len(got) != 0 {
		t.Fatalf("首个动作失败不得产生任何 command_executions, 实际 %v", got)
	}
}

// ─── A-15: 日熔断仍按 rule 计数 (O-1 裁决 (a)) ───────────────────────────
//
// MaxDailyExec=1 + 2 个动作: 第 1 次触发成功 (规则级 1 次), 第 2 次触发必须被
// 抑制。若把计数改成"按 action"(阈值 × 动作数), 第 2 次就会被放行 → 本用例红。
func TestMultiActionDailyLimitCountsByRule(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "日熔断按规则", 2)
	if err := pl.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("max_daily_exec", 1).Error; err != nil {
		t.Fatal(err)
	}
	rule.MaxDailyExec = 1
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":6,"level":1}`)

	// 第 1 次: 两个动作都执行, 规则级占 1 个名额。
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})
	var executed int64
	pl.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultExecuted).Count(&executed)
	if executed != 1 {
		t.Fatalf("第 1 次触发后 executed 事件=%d want 1 (规则级一行)", executed)
	}

	// 第 2 次: 日熔断按 rule 计数 → 抑制。
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})
	var suppressed int64
	pl.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultSuppressedDailyLimit).Count(&suppressed)
	if suppressed != 1 {
		t.Fatalf("第 2 次触发 suppressed_daily_limit=%d want 1 —— 日熔断必须按 rule 计数 (MaxDailyExec=1)", suppressed)
	}
	// 名额用尽后不得再产生新的 command_executions (仍只有第 1 次的两个动作)。
	if got := commandPinOrder(t, pl); len(got) != 2 {
		t.Fatalf("command_executions=%v want 两个 (被熔断的那次不得下发)", got)
	}
}

// ─── A-16: 冷却锚点语义不因多动作改变 (触发时刻, 非动作完成时刻) ─────────
//
// 注入固定时钟: 触发时刻 at 是一个**过去**的确定值。锚点必须等于 at;
// 若改成"动作完成时刻"(time.Now()), 锚点会跳到真实的现在 → 本用例红。
func TestMultiActionCooldownAnchorIsTriggerTime(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "冷却锚点", 2)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":6,"level":1}`)

	at := time.Date(2026, 3, 1, 6, 30, 0, 0, time.UTC)
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at})

	var after models.AutomationRule
	if err := pl.db.First(&after, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastTriggeredAt == nil {
		t.Fatal("全部动作成功后必须写冷却锚点 (last_triggered_at)")
	}
	if !after.LastTriggeredAt.UTC().Equal(at) {
		t.Fatalf("last_triggered_at=%v want %v —— 锚点必须是【触发时刻】, 不是动作完成时刻",
			after.LastTriggeredAt.UTC(), at)
	}
}

// 部分失败时**不得**写冷却锚点: 规则没有完整执行完, 锚点会静默把规则冷却掉。
func TestMultiActionPartialFailureDoesNotWriteAnchor(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "部分失败不写锚点", 2)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "no_such_action", `{}`)

	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	var after models.AutomationRule
	pl.db.First(&after, rule.ID)
	if after.LastTriggeredAt != nil {
		t.Fatalf("部分失败不得写冷却锚点, 实际 %v", after.LastTriggeredAt)
	}
}
