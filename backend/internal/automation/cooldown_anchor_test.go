package automation

// 清理前置条件 A 的不变式守护 (见 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §3)。
//
// A-INV-1: 删掉 automation_events 的 executed 行后, 冷却窗【不得】提前解除。
// 缺陷形态 (本文件落地时实测为红): 冷却态只在 evaluator 内存里, 重启后靠
// rebuildCooldowns() 从 automation_events "MAX(triggered_at) WHERE result='executed'"
// 回填 —— 事件行被清理器删掉 => 重启后该规则被当作"从未触发"(armed)
// => 条件仍满足时立即重触发 => 真实设备动作多发 (改变系统行为, 不是丢审计)。
//
// A-INV-2: 锚点写入必须与 executed 事件同事务 (不允许"事件落了锚点没落")。
// A-INV-3: 启动回填只填"锚点为空"的规则, 绝不覆盖已有锚点。
//
// 变异自证: 把 rebuildCooldowns 改回只读 automation_events, A-INV-1 必红;
// 把 planner 的锚点更新挪出事务 (先 UPDATE 再 Create), A-INV-2 必红;
// 把回填改成无条件覆盖, A-INV-3 必红。

import (
	"context"
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// runCooldownProbeAfterRestart 造一条已在冷却中的规则, 模拟"清理器删除 executed 行",
// 再新建 Evaluator (模拟重启) 并在同一时刻求值, 返回"是否触发了"。
//
// 冷却基线由真实触发路径 (planner.TriggerRule → device_action → executed 事件) 建立,
// 不使用直接改库的捷径 —— 这样"锚点是否被真正写入"才是被检验的对象。
func runCooldownProbeAfterRestart(t *testing.T, cooldownSec int, deleteExecuted bool) bool {
	t.Helper()
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)

	rule := cooldownManualRule(edge.ID, cooldownSec)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则: %v", err)
	}
	first, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("首次手动触发报错: %v", err)
	}
	if first.Result != models.AutomationResultExecuted {
		t.Fatalf("前置条件失败: 首次触发 result=%s, 期望 executed (冷却基线未建立)", first.Result)
	}

	if deleteExecuted {
		// 模拟清理器: 删除该规则的 executed 行 (档位 B 的时间窗删除)。
		if err := p.db.Where("rule_id = ? AND result = ?", rule.ID,
			models.AutomationResultExecuted).Delete(&models.AutomationEvent{}).Error; err != nil {
			t.Fatalf("删除 executed 行: %v", err)
		}
		var left int64
		p.db.Model(&models.AutomationEvent{}).Where("rule_id = ?", rule.ID).Count(&left)
		if left != 0 {
			t.Fatalf("前置条件失败: 事件行未删净, 剩 %d 行", left)
		}
	}

	// 重启: 新建 Evaluator (触发时刻距今 < cooldown, 条件仍满足)。
	h := &captureHandler{}
	ev := NewEvaluator(p.db, h)
	ev.Evaluate(edge.ID, illuminanceField(600), time.Now())
	return len(h.events) > 0
}

// TestCooldownAnchorSurvivesExecutedEventDeletion A-INV-1。
//
// 正对照 (事件行未删) 与主断言 (事件行被删) 必须给出同一结论: 冷却中, 不触发。
// 正对照的存在是为了证明"不触发"这个断言非空洞 —— 否则一个恒不触发的求值器
// 也能让主断言变绿。
func TestCooldownAnchorSurvivesExecutedEventDeletion(t *testing.T) {
	const cooldownSec = 3600
	t.Run("正对照: executed 行保留 ⇒ 重启后仍在冷却", func(t *testing.T) {
		if runCooldownProbeAfterRestart(t, cooldownSec, false) {
			t.Fatal("事件行保留时重启后触发了 —— 冷却回填本身已失效, 本测试无从检验清理影响")
		}
	})
	t.Run("主断言: executed 行被删 ⇒ 重启后不得提前解除冷却", func(t *testing.T) {
		if runCooldownProbeAfterRestart(t, cooldownSec, true) {
			t.Fatal("删除 executed 行后重启立即重触发 —— 冷却窗被清理提前解除, " +
				"条件仍满足的规则会多发真实设备动作 (A-INV-1 被破坏)")
		}
	})
}

// TestCooldownAnchorBackfillOnlyFillsEmpty A-INV-3:
// 启动回填只填"锚点为空"的规则; 已有锚点的规则不得被事件表 MAX 覆盖 (锚点自持),
// 否则事件表的清理会再次影响冷却。
func TestCooldownAnchorBackfillOnlyFillsEmpty(t *testing.T) {
	db := newTestDB(t)
	anchor := time.Now().Add(-10 * time.Hour).UTC().Truncate(time.Second)

	withAnchor := automationRule(1, "illuminance", "gt", 500, 0)
	withAnchor.CooldownSec = 3600
	withAnchor.LastTriggeredAt = &anchor
	if err := db.Create(&withAnchor).Error; err != nil {
		t.Fatal(err)
	}
	empty := automationRule(2, "illuminance", "gt", 500, 0)
	empty.CooldownSec = 3600
	if err := db.Create(&empty).Error; err != nil {
		t.Fatal(err)
	}

	newer := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)
	for _, ruleID := range []uint{withAnchor.ID, empty.ID} {
		ev := models.AutomationEvent{RuleID: ruleID, TriggeredAt: newer,
			Result: models.AutomationResultExecuted, CreatedAt: newer}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}

	NewEvaluator(db, &captureHandler{}) // 构造即触发回填

	var got models.AutomationRule
	if err := db.First(&got, withAnchor.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.LastTriggeredAt == nil {
		t.Fatal("已有锚点的规则被回填清空 —— 锚点自持性被破坏")
	}
	if !got.LastTriggeredAt.Equal(anchor) {
		t.Fatalf("已有锚点被事件表 MAX 覆盖: 锚点=%s, 期望保持 %s (A-INV-3 被破坏)",
			got.LastTriggeredAt, anchor)
	}

	var backfilled models.AutomationRule
	if err := db.First(&backfilled, empty.ID).Error; err != nil {
		t.Fatal(err)
	}
	if backfilled.LastTriggeredAt == nil {
		t.Fatal("锚点为空的规则未被一次性回填 —— 既有数据(事件表有历史)兼容性缺失")
	}
	if !backfilled.LastTriggeredAt.Equal(newer) {
		t.Fatalf("回填值=%s, 期望事件表 MAX=%s", backfilled.LastTriggeredAt, newer)
	}
}

// TestExecutedEventAndAnchorAreAtomic A-INV-2:
// 让 executed 事件落库失败, 断言锚点【不】被更新 —— 证明两者在同一事务里。
// 若有人把锚点更新挪到事务外 (先 UPDATE 再 Create), 本测试必红。
func TestExecutedEventAndAnchorAreAtomic(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)

	rule := cooldownManualRule(edge.ID, 3600)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 注入失败: 所有对 automation_events 的 INSERT 直接报错。
	injected := errors.New("injected automation_events write failure")
	if err := p.db.Callback().Create().Before("gorm:create").
		Register("test:fail_automation_events", func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "automation_events" {
				tx.AddError(injected)
			}
		}); err != nil {
		t.Fatal(err)
	}

	ev, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("手动触发返回错误: %v", err)
	}
	if ev.ID != 0 {
		t.Fatalf("事件落库被注入失败后仍返回 ID=%d", ev.ID)
	}

	var stored models.AutomationRule
	if err := p.db.First(&stored, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastTriggeredAt != nil {
		t.Fatalf("事件落库失败但锚点已被更新为 %s —— 锚点写入不在同一事务内 (A-INV-2 被破坏)",
			stored.LastTriggeredAt)
	}
}

// ─── A-INV-4 (清理前置条件 A): 确认制路径翻转 pending_confirm → executed 时同事务写锚点 ───
//
// 缺陷形态: executed 行有 3 个生产写入点 (automation/planner.go:210 自动 /
// :661 手动 / :757-766 ConfirmEvent 的条件 UPDATE), 前两个走 planner.recordExecuted,
// 第三个是【UPDATE 而非 INSERT】。若只在 recordExecuted 里写锚点,
// require_confirmed=true 的规则锚点【永远不会被写】, 重启后冷却照样提前解除。
//
// 变异自证: 去掉 ConfirmEvent 里对 last_triggered_at 的 Update, 本测试必红。
// setupPeriphConfirmPlanner 造可走通【确认制成功路径】的最小链路:
// node + GPIO pin5 配置 + system_admin 操作者 + periph 动作 gpio_set
// (set/medium/observation/PeriphCmd ⇒ CurrentEngineAllows 放行, confirmationRequired=true)。
// 不能复用 confirmedRule + confirm_reset: 那条路是 read+medium+single, 被引擎 gate
// 恒拦 (planner_test.go:161-164 已记录该架构事实), 测不到 confirm 成功。
func setupPeriphConfirmPlanner(t *testing.T) (*Planner, uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-anchor-periph", Name: "anchor", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 5, Direction: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	newAdminOperator(t, db, 7)
	svc := commandexec.NewService(db, deviceaction.NewBuiltInRegistry(nil))
	svc.SetDispatchEnabled(true)
	return NewPlanner(db, svc, nil, 900), node.ID
}

func TestConfirmEventWritesCooldownAnchor(t *testing.T) {
	p, nodeID := setupPeriphConfirmPlanner(t)
	rule := models.AutomationRule{
		Name: "确认制冷却锚点", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: nodeID, TriggerSensorName: "illuminance",
		TriggerComparator: "gt", TriggerThreshold: 500,
		CooldownSec: 3600, RequireConfirmed: true,
		ActionType: models.AutomationActionDeviceAction, ActionDeviceID: nodeID,
		ActionID: "gpio_set", ActionParamsJSON: `{"pin":5,"level":1}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	triggeredAt := p.nowFn().Add(-2 * time.Minute) // 触发时刻早于确认时刻, 用于区分锚点取值
	ev := pendingEvent(t, p, rule.ID, 600, triggeredAt)

	got, err := p.ConfirmEvent(context.Background(), ev.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("confirm 成功路径报错: %v", err)
	}
	if got.Result != models.AutomationResultExecuted {
		t.Fatalf("confirm 后 result=%s, 期望 executed", got.Result)
	}

	var stored models.AutomationRule
	if err := p.db.First(&stored, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastTriggeredAt == nil {
		t.Fatal("确认制路径翻转 executed 后锚点仍为空 —— require_confirmed 规则的冷却锚点永远不会被写 (A-INV-4 被破坏)")
	}
	// 容差比较 (而非 Equal): PG 的 timestamp 只到微秒, SQLite 保留纳秒 —— 这是
	// 跨库精度差异, 不是缺陷。容差取 1ms 仍然能抓住"写成了确认时刻"这类错误
	// (本用例里触发时刻比确认时刻早 2 分钟, 远超容差)。
	if diff := stored.LastTriggeredAt.Sub(triggeredAt); diff > time.Millisecond || diff < -time.Millisecond {
		t.Fatalf("锚点=%s, 期望≈原事件 triggered_at %s (差 %s; 冷却窗起点应是触发时刻, 不是确认时刻)",
			stored.LastTriggeredAt, triggeredAt, diff)
	}

	// 端到端: 重启后该规则仍处于冷却 (与 A-INV-1 同一判据, 但走确认制路径建立的锚点)。
	if err := p.db.Where("id = ?", ev.ID).Delete(&models.AutomationEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	h := &captureHandler{}
	ev2 := NewEvaluator(p.db, h)
	ev2.Evaluate(nodeID, illuminanceField(600), p.nowFn())
	if len(h.events) > 0 {
		t.Fatal("确认制规则在 executed 事件行被删后重启立即重触发 —— 锚点未生效")
	}
}

// TestBackfillConcurrentWriteIsNotOverwritten A-INV-3 (并发面): 回填的 UPDATE 必须自带
// "last_triggered_at IS NULL" 条件 —— 否则会覆盖"回填读取之后、UPDATE 执行之前"由
// 并发触发写入的新锚点, 把冷却倒退回更早的时刻 (也是一种"冷却提前解除")。
//
// 为什么单靠调用方判断不够: backfillCooldownAnchors 先 Scan 出"空锚点"清单再逐条
// UPDATE, 两步之间规则可能已经触发。只有 UPDATE 自身带条件才是原子的。
func TestBackfillConcurrentWriteIsNotOverwritten(t *testing.T) {
	db := newTestDB(t)
	anchor := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second) // 并发触发刚写入的新锚点
	rule := automationRule(1, "illuminance", "gt", 500, 0)
	rule.CooldownSec = 3600
	rule.LastTriggeredAt = &anchor
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	// 事件表里有一条【更早】的历史行: 无条件回填会用它覆盖上面那个更新的锚点。
	older := time.Now().Add(-5 * time.Hour).UTC().Truncate(time.Second)
	if err := db.Create(&models.AutomationEvent{RuleID: rule.ID, TriggeredAt: older,
		Result: models.AutomationResultExecuted, CreatedAt: older}).Error; err != nil {
		t.Fatal(err)
	}

	// 直接调用回填 (绕过 rebuildCooldowns 的调用方判断), 检验 UPDATE 自身的守卫。
	ev := NewEvaluator(db, &captureHandler{})
	ev.backfillCooldownAnchors([]uint{rule.ID})

	var stored models.AutomationRule
	if err := db.First(&stored, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastTriggeredAt == nil || !stored.LastTriggeredAt.Equal(anchor) {
		t.Fatalf("锚点=%v, 期望保持并发写入的 %s —— 回填覆盖了更新的锚点, A-INV-3 被破坏",
			stored.LastTriggeredAt, anchor)
	}
}

// TestAutoTriggerEventAndAnchorAreAtomic A-INV-2 (自动路径): recordExecuted 是
// 自动触发 (HandleTrigger → executeDeviceAction) 的落库函数, 与手动路径是两段独立
// 实现。上面那条只覆盖手动路径 —— 若只在 recordExecuted 里把锚点更新挪出事务,
// 手动路径的测试【不会变红】(实测: 变异 A4 曾经漏网)。本测试补上自动路径。
func TestAutoTriggerEventAndAnchorAreAtomic(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rule := cooldownManualRule(edge.ID, 3600) // 自动路径: device_action + low_read
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected automation_events write failure")
	if err := p.db.Callback().Create().Before("gorm:create").
		Register("test:fail_automation_events_auto", func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "automation_events" {
				tx.AddError(injected)
			}
		}); err != nil {
		t.Fatal(err)
	}

	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})

	var stored models.AutomationRule
	if err := p.db.First(&stored, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastTriggeredAt != nil {
		t.Fatalf("自动路径: 事件落库失败但锚点已被更新为 %s —— recordExecuted 的锚点写入不在同一事务内 (A-INV-2 被破坏)", stored.LastTriggeredAt)
	}
}
