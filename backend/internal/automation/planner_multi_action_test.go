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
	"context"
	"encoding/json"
	"sort"
	"strconv"
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

// ─── P2-E: 人工确认路径必须逐动作串行 (确认 2 动作 → 2 行 command_executions) ──
//
// 反向验证: 把 ConfirmEvent 退回单动作 (幂等键去掉 action_seq 段且只 Create 一次) →
// 本用例红 (只下发 1 行, 第 2 个动作被静默丢弃)。
func TestConfirmEventExecutesAllActionsInOrder(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	newAdminOperator(t, pl.db, 7)
	rule := confirmedMultiActionRule(t, pl, edge.ID, 2)

	// 造一行 pending_confirm 事件 (人工确认的入口)。
	ev := pendingEvent(t, pl, rule.ID, 600, pl.nowFn())

	got, err := pl.ConfirmEvent(context.Background(), ev.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("confirm 失败: %v", err)
	}
	if got.Result != models.AutomationResultExecuted {
		t.Fatalf("confirm 后 result=%q want executed (detail=%s)", got.Result, got.Detail)
	}
	if n := len(execRows(t, pl)); n != 2 {
		t.Fatalf("2 动作规则人工确认后必须下发 2 行 command_executions, 实际 %d —— 第 2 个动作被静默丢弃 (P2-E)", n)
	}
	// 每个动作各一个幂等键 (含 action_seq 段), 不得重合。
	keys := map[string]bool{}
	for _, row := range execRows(t, pl) {
		if keys[row.IdempotencyKey] {
			t.Fatalf("幂等键重复 (缺 action_seq 段): %s", row.IdempotencyKey)
		}
		keys[row.IdempotencyKey] = true
	}
	// 冷却锚点 = 原事件的触发时刻 (pending_confirm 落库时刻)。
	var after models.AutomationRule
	pl.db.First(&after, rule.ID)
	if after.LastTriggeredAt == nil || !after.LastTriggeredAt.UTC().Equal(ev.TriggeredAt.UTC()) {
		t.Fatalf("冷却锚点=%v want %v", after.LastTriggeredAt, ev.TriggeredAt)
	}
}

// P2-E 反向对照: 第 2 个动作失败 → partial_failure + action_seq=2 + 第 3 个未执行。
func TestConfirmEventPartialFailureAbortsRemaining(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	newAdminOperator(t, pl.db, 7)
	rule := confirmedMultiActionRule(t, pl, edge.ID, 3)
	// seq=2 用目录里不存在的动作 → Create 失败。
	if err := pl.db.Model(&models.AutomationRuleAction{}).
		Where("rule_id = ? AND seq = ?", rule.ID, 2).
		Update("action_id", "no_such_action").Error; err != nil {
		t.Fatal(err)
	}
	ev := pendingEvent(t, pl, rule.ID, 600, pl.nowFn())

	got, err := pl.ConfirmEvent(context.Background(), ev.ID, 7, "127.0.0.1")
	if err == nil {
		t.Fatal("第 2 个动作失败时必须透传错误 (handler 据此映射状态码)")
	}
	if got.Result != models.AutomationResultPartialFailure {
		t.Fatalf("result=%q want partial_failure (detail=%s)", got.Result, got.Detail)
	}
	if got.ActionSeq == nil || *got.ActionSeq != 2 {
		t.Fatalf("action_seq=%v want 2", got.ActionSeq)
	}
	if n := len(execRows(t, pl)); n != 1 {
		t.Fatalf("第 3 个动作必须未执行 (失败中止剩余), 实际下发 %d 行", n)
	}
	// 部分失败不得写冷却锚点 (P2-C 裁决)。
	var after models.AutomationRule
	pl.db.First(&after, rule.ID)
	if after.LastTriggeredAt != nil {
		t.Fatalf("部分失败不得写冷却锚点, 实际 %v", after.LastTriggeredAt)
	}
}

// confirmedMultiActionRule 造一条 require_confirmed=true 的多动作规则 (必须过 V7 语义:
// require_confirmed=true 时风险动作无需风险授权)。
func confirmedMultiActionRule(t *testing.T, pl *Planner, edgeID uint, actionCount int) models.AutomationRule {
	t.Helper()
	for pin := 5; pin < 5+actionCount; pin++ {
		if err := pl.db.Create(&models.GPIOConfig{
			NodeID: "node-conf", Pin: pin, Direction: 1, Enabled: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rule := models.AutomationRule{
		Name:                "确认多动作",
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edgeID,
		TriggerSensorName:   "illuminance",
		TriggerComparator:   "gt",
		TriggerThreshold:    500,
		CooldownSec:         0,
		RequireConfirmed:    true,
		ActionType:          models.AutomationActionDeviceAction,
		ActionDeviceID:      edgeID,
		ActionID:            "gpio_set",
		ActionParamsJSON:    `{"pin":5,"level":1}`,
	}
	if err := pl.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	for seq := 1; seq <= actionCount; seq++ {
		pin := 5 + seq - 1
		params := `{"pin":` + strconv.Itoa(pin) + `,"level":1}`
		if err := pl.db.Create(&models.AutomationRuleAction{
			RuleID: rule.ID, Seq: seq, ActionType: models.AutomationActionDeviceAction,
			ActionDeviceID: edgeID, ActionID: "gpio_set", ActionParamsJSON: params, Enabled: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return rule
}

// ─── P1-A / P2-B: 部分失败后重试必须真的重新下发 ─────────────────────────
//
// 反向验证: 把 nextTriggerSeq 退回"当日 executed 行数 + 1" →
// TestMultiActionRetryAfterPartialFailureRedispatches 红 (第 1 个动作被 replay
// 吞掉: 新增行数不足, 且日志记 executed)。
func TestMultiActionRetryAfterPartialFailureRedispatches(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "部分失败后重试", 2)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "gpio_set", `{"pin":6,"level":1}`)

	// 第 1 次触发: 两个动作都成功 → 动作 1/2 各一行 command_executions。
	at1 := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at1})
	if got := len(execRows(t, pl)); got != 2 {
		t.Fatalf("第 1 次触发应下发 2 个动作, 实际 %d", got)
	}

	// 把第 2 个动作改成目录里不存在的动作 → 模拟"配置坏了"的部分失败。
	if err := pl.db.Model(&models.AutomationRuleAction{}).
		Where("rule_id = ? AND seq = ?", rule.ID, 2).
		Update("action_id", "no_such_action").Error; err != nil {
		t.Fatal(err)
	}
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at1.Add(time.Minute)})
	var partial models.AutomationEvent
	pl.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultPartialFailure).
		Order("id DESC").First(&partial)
	if partial.ID == 0 {
		t.Fatal("前置条件失败: 第 2 次触发应落 partial_failure")
	}
	// 此时共 3 行 (第 1 次两个 + 第 2 次的动作 1)。
	if got := len(execRows(t, pl)); got != 3 {
		t.Fatalf("部分失败后应有 3 行 command_executions, 实际 %d", got)
	}

	// 修好配置并重试: 动作 1 必须**真的重新下发**, 而不是被 replay 复用。
	if err := pl.db.Model(&models.AutomationRuleAction{}).
		Where("rule_id = ? AND seq = ?", rule.ID, 2).
		Update("action_id", "gpio_set").Error; err != nil {
		t.Fatal(err)
	}
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at1.Add(2 * time.Minute)})

	if got := len(execRows(t, pl)); got != 5 {
		t.Fatalf("重试后 command_executions 应新增 2 行 (共 5 行), 实际 %d —— 动作 1 被幂等 replay 吞掉了"+
			" (P1-A: 部分失败后重试静默复用上次结果)", got)
	}
	// 最新事件必须是 executed (两个动作都真的成功)。
	var last models.AutomationEvent
	pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&last)
	if last.Result != models.AutomationResultExecuted {
		t.Fatalf("重试后 result=%q want executed (detail=%s)", last.Result, last.Detail)
	}
	// 三个触发决策 = 三个不同的 trigger_seq: 幂等键不得重合。
	keys := map[string]bool{}
	for _, row := range execRows(t, pl) {
		if keys[row.IdempotencyKey] {
			t.Fatalf("幂等键重复 (trigger_seq 未推进): %s", row.IdempotencyKey)
		}
		keys[row.IdempotencyKey] = true
	}
}

// P2-B: 部分失败后**改动作**重试, 不得因 RequestHash 不同落 ErrIdempotencyCollision。
func TestMultiActionRetryAfterChangingActionDoesNotCollide(t *testing.T) {
	pl, edge := setupConfirmPlanner(t)
	rule := multiActionRule(t, pl, edge.ID, "改动作重试", 2)
	addAction(t, pl, rule.ID, 1, "gpio_set", `{"pin":5,"level":1}`)
	addAction(t, pl, rule.ID, 2, "no_such_action", `{}`)

	at := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at})
	var partial models.AutomationEvent
	pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&partial)
	if partial.Result != models.AutomationResultPartialFailure {
		t.Fatalf("前置条件失败: result=%q want partial_failure", partial.Result)
	}

	// 修好第 2 个动作 (换回 gpio_set 并给出合法参数) + **改掉第 1 个动作的参数**
	// (让它的 RequestHash 与上次不同 —— 这正是 P2-B 的碰撞触发条件)。
	if err := pl.db.Model(&models.AutomationRuleAction{}).Where("rule_id = ? AND seq = ?", rule.ID, 2).
		Updates(map[string]interface{}{
			"action_id":          "gpio_set",
			"action_params_json": `{"pin":6,"level":1}`,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := pl.db.Model(&models.AutomationRuleAction{}).Where("rule_id = ? AND seq = ?", rule.ID, 1).
		Update("action_params_json", `{"pin":5,"level":0}`).Error; err != nil {
		t.Fatal(err)
	}

	pl.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: at.Add(time.Minute)})

	var last models.AutomationEvent
	pl.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&last)
	if last.Result == models.AutomationResultFailedDispatch {
		t.Fatalf("改动作重试不得落 failed_dispatch (P2-B: trigger_seq 冻结导致 ErrIdempotencyCollision); detail=%s",
			last.Detail)
	}
	if last.Result != models.AutomationResultExecuted {
		t.Fatalf("result=%q want executed (detail=%s)", last.Result, last.Detail)
	}
	if got := len(execRows(t, pl)); got != 3 {
		t.Fatalf("重试后 command_executions 应为 3 行 (第 1 次动作1 + 重试 2 个), 实际 %d", got)
	}
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

// 部分失败时**不得**写冷却锚点。
//
// 这是显式裁决（方案 §3.2.4「部分失败的锚点」，P2-C），不是实现细节：锚点表达
// "该规则完整触发过"；只执行一部分动作时写锚点会让规则被静默冷却整个冷却窗，
// 用户既看不到也无法在窗口内重试恢复（堵死 P1-A 的恢复路径）。失败方向应为
// "宁可让用户重试（审计可见 partial_failure）"，不可"静默冷却掉一条规则"。
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
