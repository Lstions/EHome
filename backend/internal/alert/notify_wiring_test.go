package alert

import (
	"context"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/parser"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================================
// D-1 步骤 3: 通知写入点经 Notifier (notify.Dispatcher) 落库的接线测试
// ============================================================================
//
// 本文件只验证"写入点接线"这一件事, 不重复 Dispatcher 自身的投递状态机
// (那由 internal/notify/dispatcher_test.go 覆盖)。端到端 (真实 HTTP 投递 +
// notification_deliveries 审计) 见同包的 e2e_notify_wiring_test.go。

// fakeNotifier 记录所有经它写入的通知, 并真落库以保持自增 ID 语义。
//
// 为什么 db 非可选: 写入点依赖"返回后 n.ID 非零"才广播 WS 载荷 (负债 D-3 契约),
// 因此 fake 必须模拟 Dispatcher.Create 的落库语义, 否则测不到广播路径。
type fakeNotifier struct {
	mu    sync.Mutex
	got   []models.Notification
	db    *gorm.DB
	calls int
}

func (f *fakeNotifier) Create(ctx context.Context, n *models.Notification) {
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

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakeNotifier) first() models.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) == 0 {
		return models.Notification{}
	}
	return f.got[0]
}

// fire 触发一次直通告警 (DurationSec=0), 返回测试时钟。
func fire(t *testing.T, ev *Evaluator, db *gorm.DB) time.Time {
	t.Helper()
	r := rule(1, "temperature", "gt", 50, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	now := time.Now()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, now)
	return now
}

// TestAlertNotifyGoesThroughNotifier 断言: 注入 Notifier 后, 告警通知**经它落库**,
// 而不是写入点自己 db.Create。
func TestAlertNotifyGoesThroughNotifier(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeNotifier{db: db}
	ev := newTestEvaluator(t, db, nil)
	ev.SetNotifier(fake)

	fire(t, ev, db)

	if fake.count() != 1 {
		t.Fatalf("Notifier 被调用并落库 %d 次, 期望 1 次 (通知必须经它落库)", fake.count())
	}
	got := fake.first()
	if got.Title != "告警: 测试规则" {
		t.Fatalf("经 Notifier 的通知 Title = %q, 期望 %q", got.Title, "告警: 测试规则")
	}
	if got.Source != "alert_rule" || got.SourceID != "1" {
		t.Fatalf("Source/SourceID = %q/%q, 期望 alert_rule/1", got.Source, got.SourceID)
	}
	if got.Type != models.NotificationType(models.AlertLevelWarning) {
		t.Fatalf("Type = %q, 期望 %q", got.Type, models.AlertLevelWarning)
	}
	// 经 Notifier 的通知也必须是同一个 db 里真实可见的行 (单一入口不改变语义)。
	var rows []models.Notification
	db.Where("source = ?", "alert_rule").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("notifications 表有 %d 行, 期望 1 行", len(rows))
	}
}

// TestAlertNotifyNotifierGetsIDForBroadcast 断言注入路径仍满足广播前提:
// 落库拿到自增 ID 后才广播 (负债 D-3 契约)。
func TestAlertNotifyNotifierGetsIDForBroadcast(t *testing.T) {
	db := newTestDB(t)
	fake := &fakeNotifier{db: db}
	var payloads []any
	ev := newTestEvaluator(t, db, func(_ string, payload any) {
		payloads = append(payloads, payload)
	})
	ev.SetNotifier(fake)

	fire(t, ev, db)

	if len(payloads) != 1 {
		t.Fatalf("广播 %d 次, 期望 1 次", len(payloads))
	}
	// 广播载荷必须是**落库后**的通知实体: id 非 0 才广播 (负债 D-3 契约),
	// 否则前端会插入一条库里不存在的通知且永远无法标记已读。
	// 用 gin.H (广播载荷的实际类型) 断言。
	h, ok := payloads[0].(gin.H)
	if !ok {
		t.Fatalf("广播载荷类型 = %T, 期望 gin.H", payloads[0])
	}
	if id, _ := h["id"].(uint); id == 0 {
		t.Fatal("广播载荷 id = 0: 经 Notifier 落库后未拿到自增 ID (负债 D-3 被破坏)")
	}
	if fake.first().ID == 0 {
		t.Fatal("经 Notifier 落库后通知 ID = 0, 广播前提被破坏")
	}
}

// TestAlertNotifyNilNotifierFallsBackToDB 断言 nil 回落是**显式设计**:
// 不注入 Notifier 时仍能落库 (既有行为不变), 且不 panic。
//
// 这是"既有测试全绿"的根据 —— 那些测试正是走这条路。
func TestAlertNotifyNilNotifierFallsBackToDB(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil) // 不注入 Notifier

	fire(t, ev, db)

	var rows []models.Notification
	db.Where("source = ?", "alert_rule").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("nil Notifier 回落落库 %d 行, 期望 1 行 (行为必须与改造前一致)", len(rows))
	}
	if rows[0].Title != "告警: 测试规则" {
		t.Fatalf("Title = %q, 期望 %q", rows[0].Title, "告警: 测试规则")
	}
}
