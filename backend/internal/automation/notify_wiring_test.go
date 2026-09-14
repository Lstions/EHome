package automation

import (
	"context"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// ============================================================================
// D-1 步骤 3: 自动化通知写入点经 Notifier (notify.Dispatcher) 落库的接线测试
// ============================================================================
//
// planner.go 有 4 个通知写入点 (日熔断 / 系统主体不可用 / 待确认 / 纯通知动作)。
// 本文件断言它们全部改经注入的 Notifier, 且未注入时回落为直接落库。

// plannerFakeNotifier 记录经它写入的通知并真落库 (保持自增 ID 语义 —— 写入点
// 依赖返回后 n.ID 非零, 见 createNotification 注释)。
type plannerFakeNotifier struct {
	mu    sync.Mutex
	got   []models.Notification
	db    *gorm.DB
	calls int
}

func (f *plannerFakeNotifier) Create(ctx context.Context, n *models.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if n == nil {
		return
	}
	if err := f.db.WithContext(ctx).Create(n).Error; err != nil {
		return
	}
	f.got = append(f.got, *n)
}

func (f *plannerFakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *plannerFakeNotifier) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.got))
	for _, n := range f.got {
		out = append(out, n.Title)
	}
	return out
}

// triggerNotificationAction 走真实触发路径: action=notification 的规则。
func triggerNotificationAction(t *testing.T, p *Planner, edge *models.EdgeDevice) {
	t.Helper()
	r := models.AutomationRule{
		ID: 9001, Name: "纯通知规则", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID, TriggerSensorName: "rainfall",
		TriggerComparator: "gt", TriggerThreshold: 1,
		ActionType: models.AutomationActionNotification, ActionLevel: models.AlertLevelWarning,
	}
	if err := p.db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: time.Now(), Value: 5})
}

// TestPlannerNotifyActionGoesThroughNotifier 断言: 注入 Notifier 后,
// action=notification 的通知**经它落库** (而不是写入点自己 db.Create)。
func TestPlannerNotifyActionGoesThroughNotifier(t *testing.T) {
	p, edge := setupPlannerWithActor(t, 900)
	fake := &plannerFakeNotifier{db: p.db}
	p.SetNotifier(fake)

	triggerNotificationAction(t, p, edge)

	if fake.count() != 1 {
		t.Fatalf("Notifier 落库 %d 条, 期望 1 条 (通知必须经它落库)", fake.count())
	}
	if title := fake.titles()[0]; title != "策略通知: 纯通知规则" {
		t.Fatalf("经 Notifier 的通知 Title = %q", title)
	}
	// 通知在 notifications 表里真实可见 (单一入口不改变语义)。
	var rows []models.Notification
	p.db.Where("source = ?", "automation_rule").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("notifications 行数 = %d, 期望 1", len(rows))
	}
}

// TestPlannerNotifyConfirmationGoesThroughNotifier 断言 require_confirmed 的
// "建议执行"通知同样经 Notifier (铁律 4 的待确认路径 —— 无人值守下最需要外发的一类)。
func TestPlannerNotifyConfirmationGoesThroughNotifier(t *testing.T) {
	p, edge := setupPlannerWithActor(t, 900)
	fake := &plannerFakeNotifier{db: p.db}
	p.SetNotifier(fake)

	r := models.AutomationRule{
		ID: 9002, Name: "待确认规则", Enabled: true, RequireConfirmed: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID, TriggerSensorName: "rainfall",
		ActionType: models.AutomationActionDeviceAction, ActionID: "low_read", ActionDeviceID: edge.ID,
	}
	if err := p.db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: time.Now(), Value: 5})

	if fake.count() != 1 {
		t.Fatalf("Notifier 落库 %d 条, 期望 1 条", fake.count())
	}
	if title := fake.titles()[0]; title != "策略待确认: 待确认规则" {
		t.Fatalf("经 Notifier 的通知 Title = %q", title)
	}
}

// TestPlannerNotifyDailyLimitGoesThroughNotifier 断言日熔断通知经 Notifier。
func TestPlannerNotifyDailyLimitGoesThroughNotifier(t *testing.T) {
	p, edge := setupPlannerWithActor(t, 900)
	fake := &plannerFakeNotifier{db: p.db}
	p.SetNotifier(fake)

	r := models.AutomationRule{
		ID: 9003, Name: "日熔断规则", Enabled: true, MaxDailyExec: 1,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID, TriggerSensorName: "rainfall",
		TriggerComparator: "gt", TriggerThreshold: 1,
		ActionType: models.AutomationActionNotification, ActionLevel: models.AlertLevelWarning,
	}
	if err := p.db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// 预置一条当日 executed 事件, 使日限额已满 → 走熔断分支。
	if err := p.db.Create(&models.AutomationEvent{
		RuleID: r.ID, TriggeredAt: now, Result: models.AutomationResultExecuted, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: now, Value: 5})

	if fake.count() != 1 {
		t.Fatalf("Notifier 落库 %d 条, 期望 1 条 (日熔断通知)", fake.count())
	}
	if title := fake.titles()[0]; title != "策略日熔断: 日熔断规则" {
		t.Fatalf("经 Notifier 的通知 Title = %q", title)
	}
}

// TestPlannerNotifyNilNotifierFallsBackToDB 断言 nil 回落是显式设计:
// 既有测试 NewPlanner(db, svc, nil, id) 不注入 Notifier, 通知仍落库且不 panic。
func TestPlannerNotifyNilNotifierFallsBackToDB(t *testing.T) {
	p, edge := setupPlannerWithActor(t, 900) // 不注入 Notifier

	triggerNotificationAction(t, p, edge)

	var rows []models.Notification
	p.db.Where("source = ?", "automation_rule").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("nil Notifier 回落落库 %d 行, 期望 1 行 (行为必须与改造前一致)", len(rows))
	}
	if rows[0].Title != "策略通知: 纯通知规则" {
		t.Fatalf("Title = %q", rows[0].Title)
	}
}
