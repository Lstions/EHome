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
	"ehome/backend/internal/datasource"
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

// ─────────────────────────────────────────────────────────────────────────────
// 场景 7：数据源主备切换（两个来源）
// ─────────────────────────────────────────────────────────────────────────────

// TestDatasource_FailoverToStandby 验证 R1–R4/R10：
// active 失败达 max_fail_count ⇒ error ⇒ 切换 standby ⇒ failover_logs + health + 通知。
func TestDatasource_FailoverToStandby(t *testing.T) {
	c := newE2EChain(t)

	// 来源组键 = (逻辑设备, 数据类别)；组内每个**边缘设备**是一条来源，
	// 这正是主备的真实语义：同一逻辑设备的同一类别可由多台边缘设备提供。
	backupDevice := c.addSecondEdgeDevice(t, "BMS-e2e-备") // 同节点同通道的第二台 BMS
	standby := models.DataSource{
		DeviceID: c.logical.ID, Category: "rsoc", EdgeDeviceID: backupDevice.ID,
		SourceType: "edge_device", Name: "BMS-RSOC-备", Priority: 50,
		MaxFailCount: 3, Status: datasource.StatusStandby,
	}
	if err := c.db.Create(&standby).Error; err != nil {
		t.Fatalf("create standby source: %v", err)
	}

	var notes []models.Notification
	c.dsSvc.SetNotifier(func(n models.Notification) { notes = append(notes, n) })

	// ① 主来源连续失败 3 次 ⇒ error + 自动切换
	for i := 0; i < 3; i++ {
		c.dsSvc.MarkFailure(c.device.ID, datasource.TriggerDeviceOffline)
	}

	var primary models.DataSource
	if err := c.db.First(&primary, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if primary.Status != datasource.StatusError {
		t.Errorf("主来源 status = %q, want error", primary.Status)
	}
	var back models.DataSource
	if err := c.db.First(&back, standby.ID).Error; err != nil {
		t.Fatalf("reload standby: %v", err)
	}
	if back.Status != datasource.StatusActive {
		t.Errorf("备来源 status = %q, want active（应已切换）", back.Status)
	}

	// ② failover_logs：恰好 1 条，方向与原因正确
	var logs []models.FailoverLog
	if err := c.db.Where("device_id = ? AND category = ?", c.logical.ID, "rsoc").Find(&logs).Error; err != nil {
		t.Fatalf("query failover logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("failover_logs = %d, want 1: %+v", len(logs), logs)
	}
	if logs[0].FromSourceID != c.dataSource.ID || logs[0].ToSourceID != standby.ID {
		t.Errorf("切换方向 = %d→%d, want %d→%d",
			logs[0].FromSourceID, logs[0].ToSourceID, c.dataSource.ID, standby.ID)
	}
	if logs[0].Reason != datasource.ReasonAuto {
		t.Errorf("reason = %q, want %q", logs[0].Reason, datasource.ReasonAuto)
	}

	// ③ health：failure ×3 + transition ≥1
	var health []models.DataSourceHealth
	if err := c.db.Where("device_id = ? AND category = ?", c.logical.ID, "rsoc").
		Order("id").Find(&health).Error; err != nil {
		t.Fatalf("query health: %v", err)
	}
	var failures, transitions int
	for _, h := range health {
		switch h.Status {
		case "failure":
			failures++
		case "transition":
			transitions++
		}
	}
	if failures != 3 {
		t.Errorf("health failure 行 = %d, want 3", failures)
	}
	if transitions < 1 {
		t.Errorf("health transition 行 = %d, want >=1（切换必须留痕）", transitions)
	}

	// ④ 通知：恰好 1 条"数据源切换"
	var switchNotes int
	for _, n := range notes {
		if n.Title == "数据源切换" {
			switchNotes++
		}
	}
	if switchNotes != 1 {
		t.Errorf("数据源切换通知 = %d 条, want 1（notes=%d）", switchNotes, len(notes))
	}

	// ⑤ 切换后恢复上报：备来源 last_success 更新；已 error 的主来源不被改回
	bms := bmsSample{totalVoltageDeciV: 5120, remainingCentiAh: 1100, nominalCentiAh: 5000,
		rsoc: 27, cellCount: 4, temps: []int16{2981, 2992}}

	// 切换后由**备设备**上报同一类别 ⇒ 更新备来源的 last_success
	c.publishFrom(t, backupDevice, bms.frame(), 2)
	if err := c.db.First(&back, standby.ID).Error; err != nil {
		t.Fatalf("reload standby: %v", err)
	}
	if back.LastSuccess == nil {
		t.Error("切换后恢复上报应更新**备来源**的 last_success")
	}
	if err := c.db.First(&primary, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if primary.Status != datasource.StatusError {
		t.Errorf("已 error 的主来源不应被成功上报改回，got %q", primary.Status)
	}

	// ⑥ R10 冷却：再次失败不产生第二条 failover_log
	for i := 0; i < 3; i++ {
		c.dsSvc.MarkFailure(c.device.ID, datasource.TriggerDeviceOffline)
	}
	var logs2 int64
	c.db.Model(&models.FailoverLog{}).Where("device_id = ? AND category = ?", c.logical.ID, "rsoc").Count(&logs2)
	if logs2 != 1 {
		t.Errorf("冷却期内 failover_logs = %d, want 1（R10 冷却抑制重复切换）", logs2)
	}
}
