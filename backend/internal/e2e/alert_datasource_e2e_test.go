//go:build e2e

// 场景 6：阈值告警引擎端到端；场景 7：数据源主备切换端到端。
//
// 两者都是"同一帧驱动多个下游"的分支：
//   - 告警引擎挂在 parser 消费者的 alertSink 上（与 automationSink 并列）；
//   - 数据源主备在同一 category 下有两个来源，失败达阈值后切换。
//
// 这类接线缺陷（sink 未注入 / 阈值不达 / 候选选取错）只有端到端能暴露。

package e2e

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/alert"
	"ehome/backend/internal/databus"
	// M3（2026-10-23）：legacy datasource.Service 主备状态机已删除，
	// 本文件不再引用 datasource 包（原先用到 StatusError/StatusStandby 常量）。
	"ehome/backend/internal/models"
	"ehome/backend/pkg/parser"
)

// notifierFunc 适配 notify.Notifier（Create(ctx, *Notification)）。
type notifierFunc func(models.Notification)

func (f notifierFunc) Create(_ context.Context, n *models.Notification) { f(*n) }

// attachAlertEvaluator 把 alert.Evaluator 接到数据面（与 main.go 的 alertSink 同构）。
func (c *e2eChain) attachAlertEvaluator(ev *alert.Evaluator) {
	c.alertEval = ev
	sc, ok := c.consumer.inner.(*databus.SensorParserConsumer)
	if !ok {
		panic("e2e: inner consumer is not *databus.SensorParserConsumer")
	}
	sc.SetAlertSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		ev.Evaluate(edgeDeviceID, fields, at)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 场景 6：阈值告警引擎
// ─────────────────────────────────────────────────────────────────────────────

// TestAlertEngine_ThresholdFireAndResolve 验证告警状态机：
// normal → firing → resolved（单行状态机），以及通知与静默窗。
func TestAlertEngine_ThresholdFireAndResolve(t *testing.T) {
	c := newE2EChain(t)

	rule := &models.AlertRule{
		TargetType: "edge_device", TargetID: c.device.ID,
		SensorName: "temperature_1", Comparator: "gt", Threshold: 40,
		DurationSec: 0, SilenceSec: 300, Level: "warning", Enabled: true,
	}
	if err := c.db.Create(rule).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}

	var alerts []models.Notification
	alertEval := alert.NewEvaluator(c.db, func(string, any) {})
	alertEval.SetNotifier(notifierFunc(func(n models.Notification) { alerts = append(alerts, n) }))
	c.attachAlertEvaluator(alertEval)

	hot := bmsSample{totalVoltageDeciV: 5120, remainingCentiAh: 1100, nominalCentiAh: 5000,
		rsoc: 80, cellCount: 4, temps: []int16{3191, 2992}} // 45.95℃ / 26.05℃
	c.publish(t, hot.frame(), 1)

	var firing models.AlertEvent
	if err := c.db.Where("rule_id = ? AND state = ?", rule.ID, "firing").First(&firing).Error; err != nil {
		t.Fatalf("未产生 firing 事件（告警引擎未接线？）: %v", err)
	}
	if firing.Value < 40 {
		t.Errorf("firing value = %v, want > 40", firing.Value)
	}
	if firing.FiredAt == nil || firing.NotifiedAt == nil {
		t.Error("firing 事件应同时有 fired_at/notified_at")
	}
	if len(alerts) != 1 {
		t.Fatalf("通知 = %d 条, want 1: %+v", len(alerts), alerts)
	}
	if alerts[0].Source != "alert_rule" {
		t.Errorf("通知 source = %q, want alert_rule", alerts[0].Source)
	}

	// 连续第 2、3 帧仍超温 ⇒ 仍在 firing，SilenceSec 内**不重复通知**（也不新建事件）
	c.publish(t, hot.frame(), 2)
	c.publish(t, hot.frame(), 3)
	if len(alerts) != 1 {
		t.Errorf("连续 firing 期间通知数 = %d, want 1（SilenceSec 抑制重复通知）", len(alerts))
	}
	var fired int64
	c.db.Model(&models.AlertEvent{}).Where("rule_id = ?", rule.ID).Count(&fired)
	if fired != 1 {
		t.Errorf("alert_events 行数 = %d, want 1（firing 持续期不新建事件，只更新 value/notified_at）", fired)
	}

	// 恢复 ⇒ resolved（单行就地更新）
	normal := bmsSample{totalVoltageDeciV: 5120, remainingCentiAh: 1100, nominalCentiAh: 5000,
		rsoc: 80, cellCount: 4, temps: []int16{2981, 2992}} // 24.95℃
	c.publish(t, normal.frame(), 4)

	var last models.AlertEvent
	if err := c.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&last).Error; err != nil {
		t.Fatalf("reload alert event: %v", err)
	}
	if last.State != "resolved" {
		t.Errorf("恢复后 state = %q, want resolved", last.State)
	}
	if last.ResolvedAt == nil {
		t.Error("resolved 事件应有 resolved_at")
	}
	c.db.Model(&models.AlertEvent{}).Where("rule_id = ?", rule.ID).Count(&fired)
	if fired != 1 {
		t.Errorf("alert_events 行数 = %d, want 1（单行状态机）", fired)
	}

	// 恢复 ⇒ 也发一条 resolved 通知（onResolve → notify，与 firing 同一实现）
	// ⇒ 此时两侧各 1 条：firing + resolved
	if len(alerts) != 2 {
		t.Errorf("firing+resolved 通知数 = %d, want 2", len(alerts))
	}

	// 恢复后再次超温 ⇒ 新一轮 firing，应重新通知（SilenceSec 只抑制持续期内重复）
	c.publish(t, hot.frame(), 5)
	if len(alerts) != 3 {
		t.Errorf("再次触发后通知总数 = %d, want 3（firing + resolved + 新 fired）", len(alerts))
	}
}
