package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/testutil"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
)

// ============================================================================
// 复用范式: 被测代码**外发**到 mock 接收端
// ============================================================================
//
// 本仓此前的 httptest.NewServer 全是入站方向 (把被测自身挂成服务端, 再由测试
// 客户端去打)。外发方向必须反过来: 测试起接收端, 被测代码 (Dispatcher/Client)
// 主动 POST 过来, 断言**真实收到的 HTTP 请求**。
//
// 约定 (后续场景仿真 / SIM-NTFY 复用):
//   - receiver 记录每一次请求的 method/path/query/header/body/到达时间;
//   - 行为由传给 newReceiver 的 handler 决定 (200 / 500 / hang / 自定义);
//   - 断言只依赖 receiver 收到的事实 + 数据库审计行, 不依赖实现内部状态;
//   - 本地 httptest 服务端是 127.0.0.1, 因此通道必须 AllowPrivate=true ——
//     这本身就是 SSRF 例外开关的正面用法 (家庭内网 OneBot 场景)。

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	At     time.Time
}

type receiver struct {
	server *httptest.Server
	handle func(w http.ResponseWriter, r *http.Request)

	mu  sync.Mutex
	got []recordedRequest
}

func newReceiver(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *receiver {
	t.Helper()
	rec := &receiver{handle: handle}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.got = append(rec.got, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
			At:     time.Now(),
		})
		rec.mu.Unlock()
		if rec.handle != nil {
			rec.handle(w, r)
		}
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (rec *receiver) url(path string) string { return rec.server.URL + path }

func (rec *receiver) requests() []recordedRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]recordedRequest, len(rec.got))
	copy(out, rec.got)
	return out
}

func (rec *receiver) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.got)
}

// waitCount 等待接收端收到至少 want 个请求 (用于排除"投递在后台线程里"的假阴性)。
func (rec *receiver) waitCount(want int, timeout time.Duration) []recordedRequest {
	deadline := time.Now().Add(timeout)
	for {
		got := rec.requests()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// errDialObserved 是拨号观察点的哨兵错误。
var errDialObserved = errors.New("dial observed by test hook")

// intPtr 构造显式配置值 (设计 §7.6: nil = 未配置, 非 nil 的 0 = 用户显式零值)。
func intPtr(v int) *int { return &v }

// ============================================================================
// 夹具
// ============================================================================

// sleepRecorder 让重试不再真实等待, 但**记录**每次退避时长, 以便单独断言退避序列。
type sleepRecorder struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durations = append(s.durations, d)
	return nil
}

func (s *sleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.durations))
	copy(out, s.durations)
	return out
}

func newTestDispatcher(t *testing.T) (*Dispatcher, *gorm.DB, *sleepRecorder) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	d := NewDispatcher(db)
	sleeper := &sleepRecorder{}
	d.SetRetrySleep(sleeper.sleep)
	return d, db, sleeper
}

func seedNotification(t *testing.T, db *gorm.DB, notifType, title string) models.Notification {
	t.Helper()
	row := models.Notification{Type: notifType, Title: title, Message: "正文-" + title, Source: "unit_test", SourceID: "1"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入通知失败: %v", err)
	}
	return row
}

func seedChannel(t *testing.T, db *gorm.DB, channel models.NotificationChannel) models.NotificationChannel {
	t.Helper()
	// 走真实创建路径, 不做任何补偿: NotificationChannel 的三个"有默认值"列
	// (Enabled/TimeoutSec/MaxRetries) 都不带 gorm default, 因此显式零值
	// (false / 0) 能被如实落库 (设计 §7.6)。夹具若在这里补一次 UPDATE,
	// 就会把 TestDeliverSkipsDisabledChannels 要防的静默改写掩盖掉。
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("写入通道失败: %v", err)
	}
	return channel
}

func deliveriesOf(t *testing.T, db *gorm.DB) []models.NotificationDelivery {
	t.Helper()
	var rows []models.NotificationDelivery
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("读取投递审计失败: %v", err)
	}
	return rows
}

// captureLogs 把全局 logger 切到内存 buffer, 用于断言"日志里没有明文密钥"。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := logger.L
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(zapcore.EncoderConfig{MessageKey: "msg", LevelKey: "level", EncodeLevel: zapcore.CapitalLevelEncoder}),
		zapcore.AddSync(buf),
		zapcore.DebugLevel,
	)
	logger.L = zap.New(core).Sugar()
	t.Cleanup(func() { logger.L = previous })
	return buf
}

// ============================================================================
// 1. 投递成功 → delivered, 且接收端真实收到请求
// ============================================================================

func TestDeliverSuccessWritesDeliveredAudit(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "warning", "水泵温度过高")
	channel := seedChannel(t, db, models.NotificationChannel{
		Name: "测试 webhook", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		Secret: "s3cr3t-token", MinLevel: "info", Enabled: true, TimeoutSec: intPtr(5), MaxRetries: intPtr(2),
		AllowPrivate: true, // 接收端在 127.0.0.1
	})

	outcome := d.Deliver(context.Background(), notif)

	if outcome.Delivered != 1 || outcome.Failed != 0 {
		t.Fatalf("投递结果 = delivered:%d failed:%d, 期望 1/0", outcome.Delivered, outcome.Failed)
	}
	got := rec.waitCount(1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("接收端收到 %d 次请求, 期望恰好 1 次", len(got))
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/hook" {
		t.Fatalf("收到的请求 = %s %s, 期望 POST /hook", got[0].Method, got[0].Path)
	}
	if !strings.Contains(string(got[0].Body), "水泵温度过高") {
		t.Fatalf("请求体未包含通知标题: %s", got[0].Body)
	}
	if auth := got[0].Header.Get("Authorization"); auth != "Bearer s3cr3t-token" {
		t.Fatalf("Authorization 头 = %q, 期望 Bearer 密钥", auth)
	}
	if ct := got[0].Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, 期望 application/json", ct)
	}
	if !json.Valid(got[0].Body) {
		t.Fatalf("请求体不是合法 JSON: %s", got[0].Body)
	}

	rows := deliveriesOf(t, db)
	if len(rows) != 1 {
		t.Fatalf("审计行数 = %d, 期望 1", len(rows))
	}
	if rows[0].State != models.DeliveryStateDelivered {
		t.Fatalf("审计状态 = %q, 期望 delivered", rows[0].State)
	}
	if rows[0].StatusCode != http.StatusOK || rows[0].AttemptNo != 1 || rows[0].NotificationID != notif.ID || rows[0].ChannelID != channel.ID {
		t.Fatalf("审计字段不符: %+v", rows[0])
	}
	if rows[0].ErrorMessage != "" {
		t.Fatalf("成功投递的审计不该有错误文案: %q", rows[0].ErrorMessage)
	}
}

// ============================================================================
// 2. 失败 → 重试 N 次 → failed (断言实际收到的请求次数)
// ============================================================================

func TestDeliverRetriesThenFails(t *testing.T) {
	const maxRetries = 2
	const wantAttempts = maxRetries + 1

	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	d, db, sleeper := newTestDispatcher(t)
	notif := seedNotification(t, db, "error", "数据源离线")
	seedChannel(t, db, models.NotificationChannel{
		Name: "总是失败", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		MinLevel: "info", Enabled: true, TimeoutSec: intPtr(5), MaxRetries: intPtr(maxRetries), AllowPrivate: true,
	})

	outcome := d.Deliver(context.Background(), notif)

	// 不变式 1: 实际收到的请求次数 = 1 + max_retries。
	got := rec.waitCount(wantAttempts, 2*time.Second)
	if len(got) != wantAttempts {
		t.Fatalf("接收端收到 %d 次请求, 期望 %d 次 (1 次首发 + %d 次重试)", len(got), wantAttempts, maxRetries)
	}
	// 不变式 2: 终态 failed, 每次尝试一行审计, AttemptNo 递增。
	if outcome.Failed != 1 || outcome.Delivered != 0 {
		t.Fatalf("投递结果 = delivered:%d failed:%d, 期望 0/1", outcome.Delivered, outcome.Failed)
	}
	rows := deliveriesOf(t, db)
	if len(rows) != wantAttempts {
		t.Fatalf("审计行数 = %d, 期望 %d (每次尝试一行, 设计 §3 的 1:N:M)", len(rows), wantAttempts)
	}
	for i, row := range rows {
		if row.State != models.DeliveryStateFailed {
			t.Fatalf("第 %d 行状态 = %q, 期望 failed (终态)", i+1, row.State)
		}
		if row.AttemptNo != uint32(i+1) {
			t.Fatalf("第 %d 行 AttemptNo = %d, 期望 %d", i+1, row.AttemptNo, i+1)
		}
		if row.StatusCode != http.StatusInternalServerError {
			t.Fatalf("第 %d 行 StatusCode = %d, 期望 500", i+1, row.StatusCode)
		}
	}
	if strings.Contains(rows[len(rows)-1].ErrorMessage, "boom") {
		t.Fatalf("审计回显了端点响应体 (设计 §7.5 禁止): %q", rows[len(rows)-1].ErrorMessage)
	}
	// 不变式 3: 退避序列 1s, 2s (设计 §5 指数退避, 风格对齐 ota.go:107)。
	backoffs := sleeper.recorded()
	if len(backoffs) != maxRetries {
		t.Fatalf("退避次数 = %d, 期望 %d", len(backoffs), maxRetries)
	}
	wantBackoff := []time.Duration{1 * time.Second, 2 * time.Second}
	for i := 0; i < maxRetries; i++ {
		if backoffs[i] != wantBackoff[i] {
			t.Fatalf("第 %d 次退避 = %v, 期望 %v", i+1, backoffs[i], wantBackoff[i])
		}
	}
}

// TestDeliverDoesNotRetryDeterministicFailures: 4xx 重试无意义, 必须一次就落终态。
func TestDeliverDoesNotRetryDeterministicFailures(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	d, db, sleeper := newTestDispatcher(t)
	notif := seedNotification(t, db, "warning", "配置错误")
	seedChannel(t, db, models.NotificationChannel{
		Name: "400", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		MinLevel: "info", Enabled: true, MaxRetries: intPtr(3), AllowPrivate: true,
	})

	outcome := d.Deliver(context.Background(), notif)

	if got := rec.waitCount(1, time.Second); len(got) != 1 {
		t.Fatalf("400 响应收到 %d 次请求, 期望 1 次 (确定性失败不重试)", len(got))
	}
	if len(sleeper.recorded()) != 0 {
		t.Fatalf("400 响应不该进入退避等待")
	}
	if outcome.Failed != 1 {
		t.Fatalf("失败通道数 = %d, 期望 1", outcome.Failed)
	}
}

// TestBackoffSequenceIsExponentialAndCapped 固化退避序列与 30s 上限 (设计 §5)。
func TestBackoffSequenceIsExponentialAndCapped(t *testing.T) {
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, expect := range want {
		if got := BackoffFor(uint32(i + 1)); got != expect {
			t.Fatalf("第 %d 次退避 = %v, 期望 %v", i+1, got, expect)
		}
	}
}

// ============================================================================
// 3. 超时: 服务端 hang → 客户端在 timeout_sec 内放弃
// ============================================================================

func TestDeliverHonorsChannelTimeout(t *testing.T) {
	// 挂住不响应: 1.5s 后自行收尾, 保证 httptest.Server 的 Close 不会卡住;
	// 客户端的 timeout_sec=1 必须先于它放弃, 否则断言会红。
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	})
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "error", "端点不响应")
	seedChannel(t, db, models.NotificationChannel{
		Name: "hang", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		MinLevel: "info", Enabled: true, TimeoutSec: intPtr(1), MaxRetries: intPtr(0), AllowPrivate: true,
	})

	start := time.Now()
	outcome := d.Deliver(context.Background(), notif)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("投递耗时 %v, 远超配置的 1s 超时 (禁止无限等待, 设计 §7.2)", elapsed)
	}
	if outcome.Failed != 1 {
		t.Fatalf("超时必须落 failed 终态, 实际 failed=%d", outcome.Failed)
	}
	rows := deliveriesOf(t, db)
	if len(rows) != 1 || rows[0].State != models.DeliveryStateFailed {
		t.Fatalf("超时审计不符: %+v", rows)
	}
	if rows[0].StatusCode != 0 {
		t.Fatalf("超时不该有状态码, 实际 %d", rows[0].StatusCode)
	}
	// 接收端确实收到了这次请求 (证明是"发出去了但没等到响应", 不是没发)。
	if got := rec.waitCount(1, time.Second); len(got) != 1 {
		t.Fatalf("接收端收到 %d 次请求, 期望 1 次", len(got))
	}
}

// TestOutboundTimeoutIsBounded: 超时配置必须被夹到 [默认, 上限] (设计 §7.2)。
func TestOutboundTimeoutIsBounded(t *testing.T) {
	cases := []struct {
		name       string
		configured *int
		want       time.Duration
	}{
		{"未配置 (nil)", nil, 10 * time.Second},
		{"显式 0", intPtr(0), 10 * time.Second},
		{"负数", intPtr(-5), 10 * time.Second},
		{"1 秒", intPtr(1), 1 * time.Second},
		{"30 秒", intPtr(30), 30 * time.Second},
		{"上限 60 秒", intPtr(60), 60 * time.Second},
		{"超过上限截断", intPtr(3600), 60 * time.Second},
	}
	for _, tc := range cases {
		if got := OutboundTimeout(tc.configured); got != tc.want {
			t.Fatalf("timeout_sec=%s → %v, 期望 %v", tc.name, got, tc.want)
		}
	}
}

// TestNormalizeRetriesRespectsExplicitZero 守护设计 §7.6 的裁决本身 (与 DB 无关):
// "未配置" 与 "显式 0" 是两个不同的事实, 归一化必须区分。
func TestNormalizeRetriesRespectsExplicitZero(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{"未配置 (nil) → 默认 2", nil, DefaultMaxRetries},
		{"显式 0 → 不重试", intPtr(0), 0},
		{"显式 1", intPtr(1), 1},
		{"负数视为未配置", intPtr(-1), DefaultMaxRetries},
		{"超过上限截断", intPtr(99), MaxRetriesLimit},
	}
	for _, tc := range cases {
		if got := NormalizeRetries(tc.in); got != tc.want {
			t.Fatalf("%s: NormalizeRetries = %d, 期望 %d", tc.name, got, tc.want)
		}
	}
}

// TestExplicitZeroMaxRetriesSurvivesDatabaseRoundTrip 是设计 §7.6 缺陷的**回归测试**:
// 用户写 max_retries=0 时, 经过 DB 往返后仍必须是 0 (而不是被 gorm default 改成 2),
// 投递只发生一次, 且总耗时不超过 timeout + 余量。
//
// 变异自证: 把 MaxRetries 改回非指针 (或加回 gorm:"default:2"), 本测试必红 ——
// 那时 DB 会存回 2, 通道重试 1 次, 请求数与耗时同时超限。
func TestExplicitZeroMaxRetriesSurvivesDatabaseRoundTrip(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	})
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "error", "用户明确不想重试")
	channel := seedChannel(t, db, models.NotificationChannel{
		Name: "不重试", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		MinLevel: "info", Enabled: true, AllowPrivate: true,
		TimeoutSec: intPtr(1),
		MaxRetries: intPtr(0), // ← 显式零值, 设计 §7.6 的整条裁决就是为它
	})

	// 第一道证据: 读回来的就是用户配的 0, 不是 2。
	var persisted models.NotificationChannel
	if err := db.First(&persisted, channel.ID).Error; err != nil {
		t.Fatalf("读回通道失败: %v", err)
	}
	if persisted.MaxRetries == nil {
		t.Fatalf("max_retries=0 经 DB 往返后变成了 NULL —— 用户的显式配置被吞掉")
	}
	if *persisted.MaxRetries != 0 {
		t.Fatalf("max_retries 经 DB 往返后 = %d, 用户配的是 0 (被 gorm default 静默改写)", *persisted.MaxRetries)
	}

	// 第二道证据: 只发一次请求, 总耗时 ≈ timeout (1s) 而不是 3s。
	start := time.Now()
	outcome := d.Deliver(context.Background(), notif)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("投递耗时 %v —— 超过 timeout(1s)+余量, 说明发生了不该有的重试 (设计 §7.6)", elapsed)
	}
	got := rec.requests()
	if len(got) != 1 {
		t.Fatalf("接收端收到 %d 次请求, 期望恰好 1 次 (max_retries=0 不得重试)", len(got))
	}
	rows := deliveriesOf(t, db)
	if len(rows) != 1 {
		t.Fatalf("投递审计行数 = %d, 期望恰好 1 行 (不重试)", len(rows))
	}
	if rows[0].State != models.DeliveryStateFailed || rows[0].AttemptNo != 1 {
		t.Fatalf("审计行不符: %+v", rows[0])
	}
	if outcome.Failed != 1 {
		t.Fatalf("通道失败数 = %d, 期望 1", outcome.Failed)
	}
}

// ============================================================================
// 4. 重定向上限 (设计 §7.2)
// ============================================================================

func TestRedirectsAreCappedAtThreeHops(t *testing.T) {
	var target *receiver
	target = newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.url("/next"), http.StatusFound)
	})
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "info", "重定向")
	seedChannel(t, db, models.NotificationChannel{
		Name: "redirect", Type: models.ChannelTypeWebhook, TargetURL: target.url("/start"),
		MinLevel: "info", Enabled: true, MaxRetries: intPtr(0), TimeoutSec: intPtr(5), AllowPrivate: true,
	})

	d.Deliver(context.Background(), notif)

	// 实测: 1 次首发 + 2 次跟随 = 3 个请求, 第 3 跳时 CheckRedirect 收到
	// len(via)==3 并拒绝 (net/http 的 via 不含当前请求)。关键不变量是"有限":
	// 端点永远 302 到自身, 请求数必须停在一个小常数上而不是无限循环。
	got := target.waitCount(4, 2*time.Second)
	time.Sleep(200 * time.Millisecond) // 给"万一还在跟随"留出继续发请求的时间
	got = target.requests()
	if len(got) > 1+MaxRedirects {
		t.Fatalf("跟随了 %d 跳, 超过上限 %d (禁止无限重定向)", len(got)-1, MaxRedirects)
	}
	if len(got) != 3 {
		t.Fatalf("自引用 302 收到 %d 个请求, 期望恰好 3 个 (1 首发 + 2 跳后被拦)", len(got))
	}
	rows := deliveriesOf(t, db)
	if len(rows) != 1 || rows[0].State != models.DeliveryStateFailed {
		t.Fatalf("重定向超限必须落 failed: %+v", rows)
	}
	if !strings.Contains(rows[0].ErrorMessage, "redirect") {
		t.Fatalf("重定向失败原因不可辨识: %q", rows[0].ErrorMessage)
	}
}

// ============================================================================
// 5. 响应体上限 4KB (设计 §7.4)
// ============================================================================

func TestResponseBodyTruncatedAtLimit(t *testing.T) {
	const oversize = MaxResponseBytes + 1024
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("A"), oversize))
	})
	client := NewClient()
	channel := models.NotificationChannel{
		Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"), TimeoutSec: intPtr(5), AllowPrivate: true,
	}

	result := client.Post(context.Background(), channel, []byte(`{"a":1}`))

	if !result.OK {
		t.Fatalf("投递应成功, 实际: %v", result.Err)
	}
	if !result.Truncated {
		t.Fatalf("超过 %d 字节的响应体必须被标记为截断", MaxResponseBytes)
	}
	if len(result.Body) != MaxResponseBytes {
		t.Fatalf("响应体长度 = %d, 期望恰好 %d (读取上限)", len(result.Body), MaxResponseBytes)
	}
}

func TestReadBoundedBodyKeepsExactlyLimit(t *testing.T) {
	body, truncated, err := ReadBoundedBody(strings.NewReader(strings.Repeat("B", MaxResponseBytes)), MaxResponseBytes)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if truncated || len(body) != MaxResponseBytes {
		t.Fatalf("恰好等于上限的响应体不应被标记截断: truncated=%v len=%d", truncated, len(body))
	}
}

// ============================================================================
// 6. MinLevel 过滤 (设计 §9.1: info 通道不接收 warning)
// ============================================================================

func TestDeliverSkipsChannelsBelowMinLevel(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "warning", "中雨预警")

	seedChannel(t, db, models.NotificationChannel{
		Name: "只要严重", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/critical"),
		MinLevel: "critical", Enabled: true, AllowPrivate: true,
	})
	kept := seedChannel(t, db, models.NotificationChannel{
		Name: "warning 及以上", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/warning"),
		MinLevel: "warning", Enabled: true, AllowPrivate: true,
	})

	outcome := d.Deliver(context.Background(), notif)

	if outcome.Channels != 2 || outcome.Skipped != 1 {
		t.Fatalf("通道判定 = 参与 %d / 跳过 %d, 期望 2/1", outcome.Channels, outcome.Skipped)
	}
	if outcome.Delivered != 1 {
		t.Fatalf("投递成功通道数 = %d, 期望 1", outcome.Delivered)
	}
	got := rec.waitCount(1, 2*time.Second)
	if len(got) != 1 || got[0].Path != "/warning" {
		t.Fatalf("接收到的请求 = %+v, 期望只有 /warning 一条", got)
	}
	rows := deliveriesOf(t, db)
	if len(rows) != 1 || rows[0].ChannelID != kept.ID {
		t.Fatalf("被 MinLevel 过滤掉的通道不得产生审计行: %+v", rows)
	}
}

// TestMessageLevelMapping 固化设计 §11.3 要求的 error→critical 映射。
func TestMessageLevelMapping(t *testing.T) {
	cases := []struct{ notifType, want string }{
		{"info", "info"},
		{"warning", "warning"},
		{"error", "critical"},
		{"critical", "critical"},
		{"success", "info"},
		{"", "info"},
	}
	for _, tc := range cases {
		if got := (Message{Type: tc.notifType}).Level(); got != tc.want {
			t.Fatalf("通知 type=%q 的通道级别 = %q, 期望 %q", tc.notifType, got, tc.want)
		}
	}
	rankInfo, rankWarning, rankCritical := levelRank("info"), levelRank("warning"), levelRank("critical")
	if !(rankInfo < rankWarning && rankWarning < rankCritical) {
		t.Fatalf("级别大小关系错误: %d/%d/%d", rankInfo, rankWarning, rankCritical)
	}
}

// ============================================================================
// 7. 禁用通道 (设计 §5: enabled=false 不投递, 不是失败)
// ============================================================================

func TestDeliverSkipsDisabledChannels(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "error", "已禁用")
	seedChannel(t, db, models.NotificationChannel{
		Name: "禁用", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/off"),
		MinLevel: "info", Enabled: false, AllowPrivate: true,
	})

	outcome := d.Deliver(context.Background(), notif)

	if outcome.Channels != 0 || outcome.Delivered != 0 || outcome.Failed != 0 || outcome.Skipped != 0 {
		t.Fatalf("禁用通道不得参与投递: %+v", outcome)
	}
	if rec.count() != 0 {
		t.Fatalf("禁用通道收到了 %d 次请求", rec.count())
	}
	if rows := deliveriesOf(t, db); len(rows) != 0 {
		t.Fatalf("禁用通道不得产生审计行 (设计 §5): %+v", rows)
	}
}

// ============================================================================
// 8. SSRF: 默认拒绝内网, allow_private 才放行
// ============================================================================

func TestDeliverRejectsPrivateTargetUnlessAllowed(t *testing.T) {
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "warning", "内网目标")

	blocked := seedChannel(t, db, models.NotificationChannel{
		Name: "默认拒绝", Type: models.ChannelTypeOneBot, TargetURL: rec.url("/send_msg"),
		Secret: "onebot-token", MinLevel: "info", Enabled: true, TimeoutSec: intPtr(5), MaxRetries: intPtr(0), AllowPrivate: false,
	})
	allowed := seedChannel(t, db, models.NotificationChannel{
		Name: "显式放行", Type: models.ChannelTypeOneBot, TargetURL: rec.url("/send_msg"),
		Secret: "onebot-token", MinLevel: "info", Enabled: true, TimeoutSec: intPtr(5), MaxRetries: intPtr(0), AllowPrivate: true,
	})

	outcome := d.Deliver(context.Background(), notif)

	if outcome.Delivered != 1 || outcome.Failed != 1 {
		t.Fatalf("期望 1 成功 (allow_private) + 1 失败 (默认拒绝), 实际 %+v", outcome)
	}
	got := rec.waitCount(1, 2*time.Second)
	if len(got) != 1 || got[0].Path != "/send_msg" {
		t.Fatalf("只有 allow_private 通道能真的发出请求, 实际收到 %+v", got)
	}
	if auth := got[0].Header.Get("Authorization"); auth != "Bearer onebot-token" {
		t.Fatalf("OneBot access_token 未按 Bearer 发送: %q", auth)
	}

	rows := deliveriesOf(t, db)
	if len(rows) != 2 {
		t.Fatalf("审计行数 = %d, 期望 2", len(rows))
	}
	var blockedRow, allowedRow models.NotificationDelivery
	for _, row := range rows {
		switch row.ChannelID {
		case blocked.ID:
			blockedRow = row
		case allowed.ID:
			allowedRow = row
		}
	}
	if blockedRow.State != models.DeliveryStateFailed {
		t.Fatalf("默认配置下内网目标必须失败: %+v", blockedRow)
	}
	if !strings.Contains(blockedRow.ErrorMessage, "SSRF") {
		t.Fatalf("拒绝原因必须可辨识为 SSRF 策略: %q", blockedRow.ErrorMessage)
	}
	if blockedRow.StatusCode != 0 {
		t.Fatalf("被策略拒绝不该有状态码 (没发出字节): %d", blockedRow.StatusCode)
	}
	if allowedRow.State != models.DeliveryStateDelivered {
		t.Fatalf("allow_private 通道必须成功: %+v", allowedRow)
	}
}

// ============================================================================
// 9. DNS rebinding: 判定必须发生在**解析之后**, 且按解析出的 IP 拨号
// ============================================================================

// TestDialHookObservesPostResolutionDecision 证明判定点位于 DNS 解析之后:
// 假 DNS 被真正查询过 (拿到了 IP 列表), 而"域名解析后是 loopback"的名字被拒绝。
func TestDialHookObservesPostResolutionDecision(t *testing.T) {
	fake := newFakeDNS(t)
	installFakeResolver(t, fake)
	fake.script("internal.example.test", mustAddr(t, "127.0.0.1"))

	d, db, _ := newTestDispatcher(t)
	client := NewClient()
	dialCalls := 0
	client.SetDialHook(func(ctx context.Context, network, address string, allowPrivate bool, ips []netip.Addr) (net.Conn, error) {
		dialCalls++
		return nil, errDialObserved
	})
	d.SetClient(client)

	notif := seedNotification(t, db, "error", "rebinding")
	seedChannel(t, db, models.NotificationChannel{
		Name: "rebinding", Type: models.ChannelTypeWebhook, TargetURL: "http://internal.example.test:8080/hook",
		MinLevel: "info", Enabled: true, TimeoutSec: intPtr(5), MaxRetries: intPtr(0), AllowPrivate: false,
	})

	d.Deliver(context.Background(), notif)

	if fake.queryCount("internal.example.test") == 0 {
		t.Fatalf("假 DNS 从未被查询: 说明判定不是发生在解析之后")
	}
	// 客户端在发送前就会拒绝 loopback, 根本不会走到拨号 —— 这正是期望行为。
	var blocked *ErrBlockedAddress
	if err := AssertPublicHost(context.Background(), "internal.example.test", "8080"); !errors.As(err, &blocked) {
		t.Fatalf("域名解析到 127.0.0.1 必须被拒绝, 实际: %v", err)
	}
	if dialCalls != 0 {
		t.Fatalf("发送前的解析判定应在拨号前就拒绝, 实际拨号 %d 次", dialCalls)
	}

	rows := deliveriesOf(t, db)
	if len(rows) != 1 || rows[0].State != models.DeliveryStateFailed {
		t.Fatalf("rebinding 目标必须失败: %+v", rows)
	}
}

// TestDialHookSeesRebindingSecondAnswer 证明"第二次解析"也被强制判定:
// 同一个域名第一次答公网 IP、第二次答 127.0.0.1 (典型 TTL≈0 的 rebinding),
// 拨号回调里拿到的是**第二次解析的结果**, 判定在该结果上执行, 且绝不连接它。
func TestDialHookSeesRebindingSecondAnswer(t *testing.T) {
	fake := newFakeDNS(t)
	installFakeResolver(t, fake)
	fake.script("rebind.example.test", mustAddr(t, "93.184.216.34"), mustAddr(t, "127.0.0.1"))

	client := NewClient()
	var (
		mu         sync.Mutex
		observedIP []string
		hookCalls  int
	)
	client.SetDialHook(func(ctx context.Context, network, address string, allowPrivate bool, ips []netip.Addr) (net.Conn, error) {
		mu.Lock()
		hookCalls++
		for _, ip := range ips {
			observedIP = append(observedIP, ip.String())
		}
		mu.Unlock()
		return nil, errDialObserved
	})

	channel := models.NotificationChannel{
		Type: models.ChannelTypeWebhook, TargetURL: "http://rebind.example.test:8080/hook",
		TimeoutSec: intPtr(5), AllowPrivate: false,
	}
	result := client.Post(context.Background(), channel, []byte(`{"a":1}`))

	mu.Lock()
	defer mu.Unlock()
	if hookCalls == 0 {
		t.Fatalf("拨号回调从未被调用 (无法证明判定发生在解析之后)")
	}
	// 拨号回调拿到的必须是假 DNS **第二次**给的 127.0.0.1, 而不是第一次的公网 IP。
	if len(observedIP) == 0 || observedIP[len(observedIP)-1] != "127.0.0.1" {
		t.Fatalf("拨号时拿到的解析结果 = %v, 期望第二次应答 127.0.0.1", observedIP)
	}
	if result.OK {
		t.Fatalf("rebinding 到 loopback 必须失败")
	}
}

// TestDialBlockedAddressIsNeverConnected 断言被判定禁止的 IP 不会被尝试连接:
// 即便已经解析出来, 也只走拒绝分支 (否则 DNS rebinding 防护就只是"先检查后使用")。
func TestDialBlockedAddressIsNeverConnected(t *testing.T) {
	fake := newFakeDNS(t)
	installFakeResolver(t, fake)
	fake.script("mixed.example.test", mustAddr(t, "10.1.2.3"))

	client := NewClient()
	dial := client.dialContext(false)
	_, err := dial(context.Background(), "tcp", "mixed.example.test:8080")
	if err == nil {
		t.Fatalf("解析到 10.1.2.3 的拨号必须被拒绝")
	}
	var blocked *ErrBlockedAddress
	if !errors.As(err, &blocked) {
		t.Fatalf("拨号拒绝原因 = %v, 期望 *ErrBlockedAddress", err)
	}
	// allow_private=true 时同一个地址放行到"尝试连接"阶段 (连接失败是网络事实,
	// 不是策略拒绝) —— 证明这个开关只放宽地址判定。
	// 10.1.2.3 不可路由, 用 2s 的 ctx 兜住这次真实连接尝试, 不让测试等满拨号超时。
	dialAllowed := client.dialContext(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = dialAllowed(ctx, "tcp", "mixed.example.test:8080")
	if errors.As(err, &blocked) {
		t.Fatalf("allow_private=true 时不该再给出策略拒绝: %v", err)
	}
}

// ============================================================================
// 10. 密钥脱敏: 日志与审计里不含明文
// ============================================================================

func TestDeliverRedactsSecretsInLogsAndAudit(t *testing.T) {
	const secret = "PLAINTEXT-ACCESS-TOKEN-9876"
	const wecomKey = "WECOM-KEY-ABCDEF123456"

	logs := captureLogs(t)
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("echo your key: " + wecomKey))
	})
	d, db, _ := newTestDispatcher(t)
	notif := seedNotification(t, db, "error", "脱敏")
	seedChannel(t, db, models.NotificationChannel{
		Name: "企微", Type: models.ChannelTypeWeCom,
		TargetURL: rec.url("/cgi-bin/webhook/send?key=" + wecomKey),
		Secret:    secret, MinLevel: "info", Enabled: true, MaxRetries: intPtr(1), AllowPrivate: true,
	})

	d.Deliver(context.Background(), notif)

	// 接收端收到的确实是带 key 的 URL 与 Bearer 密钥 (证明"真的发了", 而不是没发)。
	got := rec.waitCount(2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("接收端收到 %d 次请求, 期望 2", len(got))
	}
	if got[0].Query.Get("key") != wecomKey {
		t.Fatalf("URL 查询串应携带原始 key (脱敏只针对日志/审计)")
	}

	// 不变式: 日志与审计里都不得出现明文。
	logText := logs.String()
	for _, leak := range []string{secret, wecomKey} {
		if strings.Contains(logText, leak) {
			t.Fatalf("日志泄露明文密钥 %q:\n%s", leak, logText)
		}
	}
	for _, row := range deliveriesOf(t, db) {
		for _, leak := range []string{secret, wecomKey} {
			if strings.Contains(row.ErrorMessage, leak) {
				t.Fatalf("审计泄露明文密钥 %q: %q", leak, row.ErrorMessage)
			}
		}
	}
	// 同时必须真的记了失败 (不能因为脱敏把可观测性也抹掉)。
	if !strings.Contains(logText, "failed") {
		t.Fatalf("投递失败必须留下日志 (可观测性), 实际日志:\n%s", logText)
	}
}

// ============================================================================
// 11. fail-open: 投递失败不影响主流程 (设计 §2.2)
// ============================================================================

func TestCreatePersistsNotificationEvenWhenDeliveryFails(t *testing.T) {
	logs := captureLogs(t)
	rec := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	d, db, _ := newTestDispatcher(t)
	seedChannel(t, db, models.NotificationChannel{
		Name: "失败端点", Type: models.ChannelTypeWebhook, TargetURL: rec.url("/hook"),
		MinLevel: "info", Enabled: true, MaxRetries: intPtr(1), TimeoutSec: intPtr(2), AllowPrivate: true,
	})

	notif := models.Notification{Type: "error", Title: "旁路语义", Message: "业务已提交", Source: "unit_test", SourceID: "42"}
	d.Create(context.Background(), &notif) // 无返回值: 失败不得影响调用方

	if notif.ID == 0 {
		t.Fatalf("通知必须已落库")
	}
	var persisted models.Notification
	if err := db.First(&persisted, notif.ID).Error; err != nil {
		t.Fatalf("通知未写入数据库: %v", err)
	}
	if !strings.Contains(logs.String(), "failed") {
		t.Fatalf("投递失败必须可观测, 日志:\n%s", logs.String())
	}
	if rec.count() < 1 {
		t.Fatalf("接收端未收到请求")
	}
}

// TestCreateSurvivesNilNotification: 通知路径上的任何输入都不得让调用方 panic。
func TestCreateSurvivesNilNotification(t *testing.T) {
	d, _, _ := newTestDispatcher(t)
	d.Create(context.Background(), nil) // 不得 panic
}
