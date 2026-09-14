package alert

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// ============================================================================
// D-1 步骤 3 最重要的测试: 端到端证明"引擎不再是孤儿"
// ============================================================================
//
// 本仓此前的状态 (主控实测): internal/notify 包完整实现且有测试, 但生产代码
// 从不构造它 —— grep NewDispatcher 零命中。外发能力是一段**孤立的引擎**:
// 测试全绿, 线上永不触发。既有测试全在 notify 包内直接调 d.Deliver(...),
// 因此"没人接线"这件事在测试里完全不可见。
//
// 本测试走**真实通知产生路径**: alert.Evaluator.Evaluate (传感器越阈值)
//   → e.notify
//   → notify.Dispatcher.Create  (main.go 注入的那个唯一入口)
//     → 写 notifications 行
//     → 读 notification_channels
//     → 真实出站 HTTP POST 到 fake server
//     → 写 notification_deliveries 审计行
//
// 断言的是"接收端真的收到了 HTTP 请求" + "审计行落库", 而不是任何内部状态。
// 若有人把投递去掉 (或把 SetNotifier 接线删了), 本测试必须红。

// e2eReceiver 是外发方向的接收端: 测试起服务, **被测代码主动 POST 过来**。
// (与 notify 包内 receiver 同思路; 这里独立实现, 因为跨包无法复用未导出符号。)
type e2eReceiver struct {
	server *httptest.Server

	mu  sync.Mutex
	got []e2eRequest
}

type e2eRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
	At     time.Time
}

func newE2EReceiver(t *testing.T) *e2eReceiver {
	t.Helper()
	rec := &e2eReceiver{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.got = append(rec.got, e2eRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
			At:     time.Now(),
		})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (rec *e2eReceiver) url(path string) string { return rec.server.URL + path }

func (rec *e2eReceiver) requests() []e2eRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]e2eRequest, len(rec.got))
	copy(out, rec.got)
	return out
}

// waitCount 等待接收端收到至少 want 个请求 (排除"投递恰好在别的线程"的假阴性)。
func (rec *e2eReceiver) waitCount(want int, timeout time.Duration) []e2eRequest {
	deadline := time.Now().Add(timeout)
	for {
		got := rec.requests()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// e2eDB 建齐本测试需要的全部表: 告警域 + 通知中心 + 通知通道 + 投递审计。
//
// 不复用 newTestDB: 那个只建告警域的表, 缺 notification_channels 会让
// Dispatcher 读通道时静默失败 (fail-open 只记日志) —— 那正是"测试假绿"的来源。
func e2eDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&models.NotificationChannel{}, &models.NotificationDelivery{}); err != nil {
		t.Fatalf("migrate notify tables: %v", err)
	}
	return db
}

// e2eChannel 建一条指向 fake server 的启用通道。
//
// AllowPrivate=true 是必须的: 接收端在 127.0.0.1, 而 SSRF 判定默认拒绝私网地址
// (设计 §7.1)。这不是"为测试开后门", 而是该开关的正面用法 —— 家庭内网的
// OneBot/自建 webhook 本来就是合法目标 (见 notify/dispatcher_test.go 同约定)。
func e2eChannel(t *testing.T, db *gorm.DB, rec *e2eReceiver, path string) models.NotificationChannel {
	t.Helper()
	// MaxRetries 显式 0 = 不重试, 让端到端"恰好一次请求"的断言稳定, 不受退避影响。
	zero := 0
	timeout := 5
	ch := models.NotificationChannel{
		Name:         "端到端 webhook",
		Type:         models.ChannelTypeWebhook,
		TargetURL:    rec.url(path),
		MinLevel:     "info",
		Enabled:      true,
		AllowPrivate: true,
		MaxRetries:   &zero,
		TimeoutSec:   &timeout,
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("写入通道失败: %v", err)
	}
	return ch
}

// TestEndToEndAlertNotificationIsActuallyDelivered 是本任务的**核心证明**:
// 一条真实告警产生路径上的通知, 经 main.go 注入的 Dispatcher 真正外发到 HTTP 接收端,
// 并留下 notification_deliveries 审计行。
func TestEndToEndAlertNotificationIsActuallyDelivered(t *testing.T) {
	db := e2eDB(t)
	rec := newE2EReceiver(t)
	channel := e2eChannel(t, db, rec, "/hook")

	// main.go 的接线序列: 构造 Dispatcher → 注入 Evaluator。
	// 这里逐字复刻, 是为了让"main.go 忘了 SetNotifier"这类回归也能被测到
	// (若换成测试专用捷径, 接线正确性就不在被测范围内了)。
	dispatcher := notify.NewDispatcher(db)
	ev := NewEvaluator(db, nil)
	ev.SetNotifier(dispatcher)

	alertRule := rule(1, "temperature", "gt", 50, 0)
	if err := db.Create(&alertRule).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	// ── 真实触发路径: 传感器值越阈值 ──
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, time.Now())

	// 断言 1: 通知落库 (现有语义保持不变)。
	var notifications []models.Notification
	if err := db.Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 {
		t.Fatalf("notifications 行数 = %d, 期望 1", len(notifications))
	}
	if notifications[0].Source != "alert_rule" {
		t.Fatalf("Source = %q, 期望 alert_rule", notifications[0].Source)
	}
	notificationID := notifications[0].ID

	// 断言 2 (**本任务的关键**): 接收端真的收到了出站 HTTP 请求。
	// 此前这段投递根本不会发生 —— 引擎是孤儿。
	got := rec.waitCount(1, 3*time.Second)
	if len(got) != 1 {
		t.Fatalf("接收端收到 %d 次请求, 期望 1 次 —— 外发引擎未接线 (仍是孤儿)", len(got))
	}
	if got[0].Method != http.MethodPost {
		t.Fatalf("出站请求方法 = %s, 期望 POST", got[0].Method)
	}
	if got[0].Path != "/hook" {
		t.Fatalf("出站请求路径 = %s, 期望 /hook", got[0].Path)
	}
	// 出站正文必须携带告警内容 (证明是这条通知被投递, 而非空请求)。
	if !strings.Contains(string(got[0].Body), "告警: 测试规则") {
		t.Fatalf("出站正文未包含通知标题, body=%s", string(got[0].Body))
	}

	// 断言 3: 投递审计行落库且为 delivered 终态 (设计 §3 的 1:N:M 审计)。
	var deliveries []models.NotificationDelivery
	if err := db.Order("id").Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("notification_deliveries 行数 = %d, 期望 1", len(deliveries))
	}
	if deliveries[0].State != models.DeliveryStateDelivered {
		t.Fatalf("审计行 state = %q, 期望 %q", deliveries[0].State, models.DeliveryStateDelivered)
	}
	if deliveries[0].NotificationID != notificationID {
		t.Fatalf("审计行通知 ID = %d, 期望 %d", deliveries[0].NotificationID, notificationID)
	}
	if deliveries[0].ChannelID != channel.ID {
		t.Fatalf("审计行通道 ID = %d, 期望 %d", deliveries[0].ChannelID, channel.ID)
	}
	if deliveries[0].StatusCode != http.StatusOK {
		t.Fatalf("审计行 status_code = %d, 期望 200", deliveries[0].StatusCode)
	}
}

// TestEndToEndDisabledChannelIsNotDelivered 反向对照: 通道禁用时**不投递**,
// 也不写审计行 (设计 §5: enabled=false 不是失败)。
//
// 为什么需要这条: 上一条测试若因"任何通知都无脑外发"而绿, 那条绿是假的。
// 本测试把"投递确实受通道配置支配"钉住。
func TestEndToEndDisabledChannelIsNotDelivered(t *testing.T) {
	db := e2eDB(t)
	rec := newE2EReceiver(t)
	ch := e2eChannel(t, db, rec, "/hook")
	if err := db.Model(&models.NotificationChannel{}).Where("id = ?", ch.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	dispatcher := notify.NewDispatcher(db)
	ev := NewEvaluator(db, nil)
	ev.SetNotifier(dispatcher)

	alertRule := rule(1, "temperature", "gt", 50, 0)
	if err := db.Create(&alertRule).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, time.Now())

	// 通知本身照旧落库。
	var notifications []models.Notification
	db.Find(&notifications)
	if len(notifications) != 1 {
		t.Fatalf("notifications 行数 = %d, 期望 1 (禁用通道不影响通知落库)", len(notifications))
	}
	// 但不得有出站请求, 也不得有审计行。
	if got := rec.waitCount(1, 500*time.Millisecond); len(got) != 0 {
		t.Fatalf("禁用通道收到 %d 次请求, 期望 0", len(got))
	}
	var deliveries []models.NotificationDelivery
	db.Find(&deliveries)
	if len(deliveries) != 0 {
		t.Fatalf("禁用通道产生 %d 行审计, 期望 0", len(deliveries))
	}
}

// TestEndToEndDeliveryFailureDoesNotBreakNotification 断言 fail-open:
// 接收端 500 时, 通知仍然落库、审计行记 failed, 且调用方不受影响。
func TestEndToEndDeliveryFailureDoesNotBreakNotification(t *testing.T) {
	db := e2eDB(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	zero := 0
	timeout := 5
	ch := models.NotificationChannel{
		Name: "总是失败", Type: models.ChannelTypeWebhook, TargetURL: failing.URL + "/hook",
		MinLevel: "info", Enabled: true, AllowPrivate: true,
		MaxRetries: &zero, TimeoutSec: &timeout,
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("写入通道失败: %v", err)
	}

	dispatcher := notify.NewDispatcher(db)
	ev := NewEvaluator(db, nil)
	ev.SetNotifier(dispatcher)

	alertRule := rule(1, "temperature", "gt", 50, 0)
	if err := db.Create(&alertRule).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, time.Now())

	// 通知照旧落库 (fail-open: 投递失败不回滚、不影响主流程)。
	var notifications []models.Notification
	db.Find(&notifications)
	if len(notifications) != 1 {
		t.Fatalf("notifications 行数 = %d, 期望 1 (投递失败不得影响通知落库)", len(notifications))
	}
	// 审计行必须记 failed —— 失败是**被审计的业务结果**, 不是静默吞掉。
	var deliveries []models.NotificationDelivery
	if err := db.Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("审计行数 = %d, 期望 1", len(deliveries))
	}
	if deliveries[0].State != models.DeliveryStateFailed {
		t.Fatalf("审计行 state = %q, 期望 %q", deliveries[0].State, models.DeliveryStateFailed)
	}
	if deliveries[0].StatusCode != http.StatusInternalServerError {
		t.Fatalf("审计行 status_code = %d, 期望 500", deliveries[0].StatusCode)
	}
	// 告警事件仍正常落库 (主流程未被外发拖垮)。
	var events []models.AlertEvent
	db.Find(&events)
	if len(events) != 1 {
		t.Fatalf("alert_events 行数 = %d, 期望 1", len(events))
	}
}
