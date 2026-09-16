package api

// 外发通知通道 HTTP 层行为测试 (设计/外发通知通道.md §4 冻结契约 / §9.1 验收)。
//
// 测试纪律: 每条都断言**可观测的行为事实** —— HTTP 响应字节、数据库里的真实列值、
// mock 接收端真正收到的 HTTP 请求 —— 而不是"路由注册了"这类源码断言。
//
// 三个必须覆盖的陷阱:
//   - secret 只写不读: 响应体里不得出现明文密钥 (含企业微信 target_url 查询串里的 key);
//   - PUT 未传 secret ≠ 清空 secret (设计 §7.6 零值语义, 用 *string 区分);
//   - DELETE 通道不得连带删除投递审计 (设计 §4 明文要求)。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// =====================================================================
// 夹具
// =====================================================================

// recordingTester 是 notificationChannelTester 的记录型假实现。
// 它证明 handler **确实调用了投递** (而不是"自己写了行就算数")。
type recordingTester struct {
	mu    sync.Mutex
	calls []recordedChannelCall
	ch    chan recordedChannelCall
}

type recordedChannelCall struct {
	Channel models.NotificationChannel
	Message notify.Message
}

func newRecordingTester() *recordingTester {
	return &recordingTester{ch: make(chan recordedChannelCall, 16)}
}

func (r *recordingTester) DeliverToChannelAsync(_ context.Context, channel models.NotificationChannel, msg notify.Message) {
	call := recordedChannelCall{Channel: channel, Message: msg}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	select {
	case r.ch <- call:
	default:
	}
}

func (r *recordingTester) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingTester) waitCall(t *testing.T, timeout time.Duration) recordedChannelCall {
	t.Helper()
	select {
	case call := <-r.ch:
		return call
	case <-time.After(timeout):
		t.Fatalf("等待投递调用超时 (%s): 端点没有发起投递", timeout)
		return recordedChannelCall{}
	}
}

// newChannelRouter 装配 6 个端点, 注入真实 Dispatcher (与生产同路径)。
func newChannelRouter(t *testing.T) (*gin.Engine, *gorm.DB, *notify.Dispatcher) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	if sqlDB, err := db.DB(); err == nil {
		// 内存 SQLite 每条新连接都是空库; 投递在独立 goroutine 里读通道,
		// 必须把连接池钉在 1 上, 否则后台 goroutine 可能连到一个空库。
		sqlDB.SetMaxOpenConns(1)
	}
	dispatcher := notify.NewDispatcher(db)
	r := setupRouter()
	registerNotificationChannelRoutes(r.Group("/api/v1"), db, dispatcher)
	return r, db, dispatcher
}

// newChannelRouterWithTester 装配一个注入了记录型假投递器的路由。
func newChannelRouterWithTester(t *testing.T) (*gin.Engine, *gorm.DB, *recordingTester) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	tester := newRecordingTester()
	r := setupRouter()
	registerNotificationChannelRoutes(r.Group("/api/v1"), db, tester)
	return r, db, tester
}

func doChannelRequest(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type channelPageData struct {
	Items    []map[string]any `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

func decodePage(t *testing.T, w *httptest.ResponseRecorder) channelPageData {
	t.Helper()
	data := requireEnvelopeOK(t, w, http.StatusOK)
	var page channelPageData
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("解析分页数据失败: %v (data=%s)", err, data)
	}
	if page.Items == nil {
		t.Fatalf("items 为 null, 空集必须序列化为 [] (data=%s)", data)
	}
	return page
}

func decodeObject(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, 期望 200: %s", w.Code, w.Body.String())
	}
	data := requireEnvelopeOK(t, w, http.StatusOK)
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("解析 data 对象失败: %v (data=%s)", err, data)
	}
	return object
}

func requireErrorStatus(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("HTTP = %d, 期望 %d: %s", w.Code, wantStatus, w.Body.String())
	}
	var env struct {
		Code    int             `json:"code"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误响应不是 JSON 信封: %v (%s)", err, w.Body.String())
	}
	if env.Code != wantStatus {
		t.Fatalf("信封 code = %d, 期望 %d: %s", env.Code, wantStatus, w.Body.String())
	}
	if string(env.Data) != "null" {
		t.Fatalf("错误响应 data 必须为 null, 实际 %s", env.Data)
	}
}

// rawChannelSecret 直读数据库列, 绕开所有 API 投影 —— 只有它才能证明
// "密钥确实存进了库" 与 "密钥确实没被改"。
func rawChannelSecret(t *testing.T, db *gorm.DB, id uint) (string, string) {
	t.Helper()
	var row struct {
		Secret     string
		SecretHint string
	}
	if err := db.Model(&models.NotificationChannel{}).
		Select("secret, secret_hint").Where("id = ?", id).Scan(&row).Error; err != nil {
		t.Fatalf("读取通道 %d 的密钥列失败: %v", id, err)
	}
	return row.Secret, row.SecretHint
}

func createChannelThroughAPI(t *testing.T, r *gin.Engine, body string) map[string]any {
	t.Helper()
	w := doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels", body)
	return decodeObject(t, w)
}

// uitoa 是本文件的 uint → string 助手 (不要复用别处的 itoa(int), 签名不同)。
func uitoa(v uint) string { return strconv.FormatUint(uint64(v), 10) }

func channelIDOf(t *testing.T, object map[string]any) uint {
	t.Helper()
	raw, ok := object["id"].(float64)
	if !ok || raw == 0 {
		t.Fatalf("响应缺少可用 id: %v", object)
	}
	return uint(raw)
}

// loadChannelRow 从库里回读通道 (断言真实列值, 不看 API 投影)。
func loadChannelRow(t *testing.T, db *gorm.DB, id uint) models.NotificationChannel {
	t.Helper()
	var row models.NotificationChannel
	if err := db.First(&row, id).Error; err != nil {
		t.Fatalf("回读通道 %d 失败: %v", id, err)
	}
	return row
}

// =====================================================================
// ① 创建后能查到 + 列表/详情永不返回明文 secret
// =====================================================================

func TestNotificationChannel_CreateThenList_HidesSecret(t *testing.T) {
	r, db, _ := newChannelRouter(t)

	const apiSecret = "ONEBOT-ACCESS-TOKEN-abcd1234"
	const urlKey = "WECOM-URL-KEY-wxyz9876"
	body := `{"name":"家庭群","type":"wecom","target_url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=` + urlKey + `","secret":"` + apiSecret + `","min_level":"warning"}`

	w := doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels", body)
	if w.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	// 明文密钥一次都不许出现在响应字节里 (含 target_url 查询串里的 key)。
	if raw := w.Body.String(); strings.Contains(raw, apiSecret) || strings.Contains(raw, urlKey) {
		t.Fatalf("创建响应泄露明文密钥: %s", raw)
	}
	if raw := w.Body.String(); strings.Contains(raw, `"secret":`) {
		t.Fatalf("创建响应出现了 secret 键: %s", raw)
	}
	created := decodeObject(t, w)
	id := channelIDOf(t, created)
	if created["secret_hint"] != "1234" {
		t.Fatalf("secret_hint = %v, 期望末 4 位 1234", created["secret_hint"])
	}
	if created["has_secret"] != true {
		t.Fatalf("has_secret = %v, 期望 true", created["has_secret"])
	}
	// target_url 的查询串必须被脱敏 (企业微信的 key 就在 URL 里)。
	//
	// 占位符形态: `key=***`。models.RedactTargetURL 与 notify.RedactURL 走同一条
	// 流水线 (Encode() 转义出的 %2A%2A%2A 由收尾的文本级兜底正则收回成 ***),
	// 两者对任意输入**逐字节相同**, 由 internal/notify/redact_contract_test.go 的
	// TestRedactTargetURLMatchesNotify 守护 (该测试只能放 notify 包: models 不能
	// import notify, 否则 import 环)。
	//
	// 层次说明: 那条契约测试锁的是"两侧一致 + 占位符形态", 这里锁的是**端到端
	// 安全性质** —— 明文密钥绝不出现。两者互补: 契约测试在包级, 这条在 HTTP 响应级
	// (脱敏若在 handler 里被绕过, 包级测试照样绿, 只有这条会红)。
	if got, _ := created["target_url"].(string); !strings.Contains(got, "key=") || strings.Contains(got, urlKey) {
		t.Fatalf("target_url 查询串里的 key 未被脱敏: %q", got)
	}

	// 库里确实存了明文 (否则"没泄露"只是因为"压根没存", 是假绿)。
	secret, hint := rawChannelSecret(t, db, id)
	if secret != apiSecret {
		t.Fatalf("数据库未存下密钥: %q", secret)
	}
	if hint != "1234" {
		t.Fatalf("数据库 secret_hint = %q, 期望 1234", hint)
	}

	// 列表能查到, 且同样不含明文密钥。
	listW := doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels", "")
	listRaw := listW.Body.String()
	if strings.Contains(listRaw, apiSecret) || strings.Contains(listRaw, urlKey) {
		t.Fatalf("列表响应泄露明文密钥: %s", listRaw)
	}
	page := decodePage(t, listW)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("列表 total/len = %d/%d, 期望 1/1", page.Total, len(page.Items))
	}
	if page.Items[0]["name"] != "家庭群" || page.Items[0]["type"] != "wecom" {
		t.Fatalf("列表内容不符: %v", page.Items[0])
	}
	if page.Items[0]["secret_hint"] != "1234" {
		t.Fatalf("列表 secret_hint = %v, 期望 1234", page.Items[0]["secret_hint"])
	}
	if _, exists := page.Items[0]["secret"]; exists {
		t.Fatalf("列表项出现了 secret 键: %v", page.Items[0])
	}
	if page.Page != 1 || page.PageSize != 20 {
		t.Fatalf("默认分页回显 = %d/%d, 期望 1/20", page.Page, page.PageSize)
	}
}

// TestNotificationChannel_Create_IgnoresProtectedFields 是 DTO 白名单的行为断言:
// 请求体里的 id / secret_hint / created_at 必须被忽略 (mass-assignment 防护)。
func TestNotificationChannel_Create_IgnoresProtectedFields(t *testing.T) {
	r, db, _ := newChannelRouter(t)
	body := `{"name":"白名单","type":"webhook","target_url":"https://example.com/hook","id":9999,"secret_hint":"zzzz","created_at":"2000-01-01T00:00:00Z","updated_at":"2000-01-01T00:00:00Z"}`
	created := createChannelThroughAPI(t, r, body)
	id := channelIDOf(t, created)
	if id == 9999 {
		t.Fatalf("请求体的 id 被采纳了: %v", created["id"])
	}
	// 未传 secret → hint 必须为空 (不能采纳请求体里的 secret_hint)。
	if created["secret_hint"] != "" || created["has_secret"] != false {
		t.Fatalf("secret 未传时 hint/has_secret 应为空/false: %v", created)
	}
	row := loadChannelRow(t, db, id)
	if row.SecretHint != "" {
		t.Fatalf("请求体的 secret_hint 被写进了库: %q", row.SecretHint)
	}
	if row.CreatedAt.Year() == 2000 {
		t.Fatalf("请求体的 created_at 被采纳了: %v", row.CreatedAt)
	}
}

// =====================================================================
// ② 设计 §7.6: max_retries 的显式零值必须能表达
// =====================================================================

func TestNotificationChannel_Create_ExplicitZeroMaxRetriesPreserved(t *testing.T) {
	r, db, _ := newChannelRouter(t)

	// 显式 0 = 用户明确要求"不重试"。
	zero := createChannelThroughAPI(t, r, `{"name":"不重试","type":"webhook","target_url":"https://example.com/a","max_retries":0,"timeout_sec":0}`)
	zeroID := channelIDOf(t, zero)
	if zero["max_retries"] != float64(0) {
		t.Fatalf("响应 max_retries = %v, 期望显式 0 (设计 §7.6)", zero["max_retries"])
	}
	if zero["timeout_sec"] != float64(0) {
		t.Fatalf("响应 timeout_sec = %v, 期望显式 0", zero["timeout_sec"])
	}
	zeroRow := loadChannelRow(t, db, zeroID)
	if zeroRow.MaxRetries == nil || *zeroRow.MaxRetries != 0 {
		t.Fatalf("库里的 max_retries = %v, 被 GORM default 静默改写 (设计 §7.6 缺陷复现)", zeroRow.MaxRetries)
	}
	if got := notify.NormalizeRetries(zeroRow.MaxRetries); got != 0 {
		t.Fatalf("运行期归一化后重试次数 = %d, 期望 0 (不重试)", got)
	}

	// 未传 = nil = 用应用层默认 (2 次)。
	omitted := createChannelThroughAPI(t, r, `{"name":"默认重试","type":"webhook","target_url":"https://example.com/b"}`)
	if omitted["max_retries"] != nil {
		t.Fatalf("未传 max_retries 时响应应为 null, 实际 %v", omitted["max_retries"])
	}
	omittedRow := loadChannelRow(t, db, channelIDOf(t, omitted))
	if omittedRow.MaxRetries != nil {
		t.Fatalf("未传时应落 NULL, 实际 %v", *omittedRow.MaxRetries)
	}
	if got := notify.NormalizeRetries(omittedRow.MaxRetries); got != notify.DefaultMaxRetries {
		t.Fatalf("未配置时的默认重试次数 = %d, 期望 %d", got, notify.DefaultMaxRetries)
	}
}

// =====================================================================
// ③ PUT 未传 secret = 不改 (最关键的一条, 直接对应设计 §7.6 的零值陷阱)
// =====================================================================

func TestNotificationChannel_Update_SecretOmittedKeepsExisting(t *testing.T) {
	r, db, _ := newChannelRouter(t)
	const secret = "KEEP-ME-TOKEN-5678"
	created := createChannelThroughAPI(t, r, `{"name":"原名字","type":"onebot","target_url":"http://192.168.1.10:5700/send_msg","secret":"`+secret+`","allow_private":true}`)
	id := channelIDOf(t, created)

	// 只改名字: 请求体里没有 secret 键。
	w := doChannelRequest(t, r, http.MethodPut, "/api/v1/notification-channels/"+uitoa(id), `{"name":"改过的名字"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", w.Code, w.Body.String())
	}
	if raw := w.Body.String(); strings.Contains(raw, secret) {
		t.Fatalf("更新响应泄露明文密钥: %s", raw)
	}
	updated := decodeObject(t, w)
	if updated["name"] != "改过的名字" {
		t.Fatalf("name 未更新: %v", updated)
	}
	if updated["secret_hint"] != "5678" || updated["has_secret"] != true {
		t.Fatalf("未传 secret 时不得改变密钥状态: %v", updated)
	}
	// 数据库层面的铁证: 未传 ≠ 清空。
	gotSecret, gotHint := rawChannelSecret(t, db, id)
	if gotSecret != secret {
		t.Fatalf("PUT 未传 secret 却把密钥改成了 %q (设计 §7.6 零值陷阱)", gotSecret)
	}
	if gotHint != "5678" {
		t.Fatalf("PUT 未传 secret 却改了 secret_hint: %q", gotHint)
	}
}

// TestNotificationChannel_Update_SecretEmptyClears 与上一条构成对照:
// 传了空字符串 = 用户明确要清空。
func TestNotificationChannel_Update_SecretEmptyClears(t *testing.T) {
	r, db, _ := newChannelRouter(t)
	created := createChannelThroughAPI(t, r, `{"name":"清空测试","type":"webhook","target_url":"https://example.com/hook","secret":"TO-BE-CLEARED-4321"}`)
	id := channelIDOf(t, created)

	// 先验证"替换"也走同一条路径。
	replaced := decodeObject(t, doChannelRequest(t, r, http.MethodPut, "/api/v1/notification-channels/"+uitoa(id), `{"secret":"REPLACED-TOKEN-9999"}`))
	if replaced["secret_hint"] != "9999" || replaced["has_secret"] != true {
		t.Fatalf("替换密钥失败: %v", replaced)
	}
	if got, _ := rawChannelSecret(t, db, id); got != "REPLACED-TOKEN-9999" {
		t.Fatalf("库里的密钥 = %q", got)
	}

	cleared := decodeObject(t, doChannelRequest(t, r, http.MethodPut, "/api/v1/notification-channels/"+uitoa(id), `{"secret":""}`))
	if cleared["secret_hint"] != "" || cleared["has_secret"] != false {
		t.Fatalf("传空串应清空密钥: %v", cleared)
	}
	if got, hint := rawChannelSecret(t, db, id); got != "" || hint != "" {
		t.Fatalf("库里的密钥未被清空: secret=%q hint=%q", got, hint)
	}
}

// TestNotificationChannel_Update_PartialFieldsAndExplicitZero 断言部分更新语义:
// 未传的字段不动, 传了的零值 (max_retries=0 / enabled=false) 真的落库。
func TestNotificationChannel_Update_PartialFieldsAndExplicitZero(t *testing.T) {
	r, db, _ := newChannelRouter(t)
	created := createChannelThroughAPI(t, r, `{"name":"部分更新","type":"webhook","target_url":"https://example.com/keep","min_level":"warning","timeout_sec":30,"max_retries":3}`)
	id := channelIDOf(t, created)

	updated := decodeObject(t, doChannelRequest(t, r, http.MethodPut, "/api/v1/notification-channels/"+uitoa(id), `{"max_retries":0,"enabled":false}`))
	if updated["max_retries"] != float64(0) {
		t.Fatalf("PUT 显式 max_retries=0 未生效: %v", updated["max_retries"])
	}
	if updated["enabled"] != false {
		t.Fatalf("PUT 显式 enabled=false 未生效: %v", updated["enabled"])
	}
	// 未传的字段必须原样保留。
	if updated["target_url"] != "https://example.com/keep" || updated["min_level"] != "warning" {
		t.Fatalf("未传字段被改动: %v", updated)
	}
	if updated["timeout_sec"] != float64(30) {
		t.Fatalf("未传 timeout_sec 被改动: %v", updated["timeout_sec"])
	}
	row := loadChannelRow(t, db, id)
	if row.MaxRetries == nil || *row.MaxRetries != 0 {
		t.Fatalf("库里的 max_retries = %v, 期望显式 0", row.MaxRetries)
	}
	if row.Enabled {
		t.Fatalf("库里的 enabled = true, 期望 false")
	}
	if row.TimeoutSec == nil || *row.TimeoutSec != 30 {
		t.Fatalf("库里的 timeout_sec = %v, 期望 30", row.TimeoutSec)
	}
}

// =====================================================================
// ④ DELETE 不连带删除 deliveries (设计 §4: 审计保留)
// =====================================================================

func TestNotificationChannel_Delete_RetainsDeliveries(t *testing.T) {
	r, db, _ := newChannelRouter(t)
	created := createChannelThroughAPI(t, r, `{"name":"待删除","type":"webhook","target_url":"https://example.com/hook"}`)
	id := channelIDOf(t, created)

	for i := 0; i < 3; i++ {
		row := models.NotificationDelivery{
			NotificationID: uint(100 + i),
			ChannelID:      id,
			State:          models.DeliveryStateDelivered,
			AttemptNo:      1,
			StatusCode:     200,
			CreatedAt:      time.Now().UTC(),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("造审计行失败: %v", err)
		}
	}

	w := doChannelRequest(t, r, http.MethodDelete, "/api/v1/notification-channels/"+uitoa(id), "")
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	if got := decodeObject(t, w)["deliveries_retained"]; got != true {
		t.Fatalf("响应未声明审计保留: %v", got)
	}

	// 通道确实没了。
	var channel models.NotificationChannel
	if err := db.First(&channel, id).Error; err == nil {
		t.Fatalf("通道仍存在, 删除未生效")
	}
	// 审计必须还在, 且条数与内容不变。
	var count int64
	if err := db.Model(&models.NotificationDelivery{}).Where("channel_id = ?", id).Count(&count).Error; err != nil {
		t.Fatalf("统计审计行失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("删除通道后审计行 = %d 条, 期望 3 条 (设计 §4: 审计保留)", count)
	}
	// 通过 API 也必须仍然查得到 (审计对用户可见, 不是"藏在库里")。
	page := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?channel_id="+uitoa(id), ""))
	if page.Total != 3 {
		t.Fatalf("审计 API total = %d, 期望 3", page.Total)
	}
}

// =====================================================================
// ⑤ /test: 真的发起投递 + 真的落投递审计
// =====================================================================

type recordedNotifyCall struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type notifyTestReceiver struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []recordedNotifyCall
}

func newNotifyTestReceiver(t *testing.T) *notifyTestReceiver {
	t.Helper()
	recv := &notifyTestReceiver{}
	recv.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recv.mu.Lock()
		recv.calls = append(recv.calls, recordedNotifyCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		recv.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(recv.server.Close)
	return recv
}

func (recv *notifyTestReceiver) waitCalls(want int, timeout time.Duration) []recordedNotifyCall {
	deadline := time.Now().Add(timeout)
	for {
		recv.mu.Lock()
		got := append([]recordedNotifyCall(nil), recv.calls...)
		recv.mu.Unlock()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNotificationChannel_Test_SendsRealDeliveryAndAudits 用真实 Dispatcher +
// 真实 httptest 接收端 (本仓"被测代码主动外发到 mock 接收端"范式, 见
// notify/dispatcher_test.go), 断言端点确实把 HTTP POST 发了出去, 并在审计表
// 落下一条 delivered 的记录。
func TestNotificationChannel_Test_SendsRealDeliveryAndAudits(t *testing.T) {
	recv := newNotifyTestReceiver(t)
	r, db, _ := newChannelRouter(t)

	const secret = "TEST-TOKEN-2468"
	created := createChannelThroughAPI(t, r, `{"name":"测试通道","type":"webhook","target_url":"`+recv.server.URL+`/hook","secret":"`+secret+`","allow_private":true}`)
	id := channelIDOf(t, created)

	w := doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels/"+uitoa(id)+"/test", "")
	if w.Code != http.StatusOK {
		t.Fatalf("测试投递失败: %d %s", w.Code, w.Body.String())
	}
	result := decodeObject(t, w)
	notificationID := uint(result["notification_id"].(float64))
	if notificationID == 0 {
		t.Fatalf("响应缺少 notification_id: %v", result)
	}

	// 1) 接收端真的收到了请求 (不是"handler 说发了")。
	got := recv.waitCalls(1, 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("mock 接收端收到 %d 个请求, 期望 1 个", len(got))
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/hook" {
		t.Fatalf("收到的请求 = %s %s, 期望 POST /hook", got[0].Method, got[0].Path)
	}
	if auth := got[0].Header.Get("Authorization"); auth != "Bearer "+secret {
		t.Fatalf("Authorization = %q, 期望 Bearer <secret>", auth)
	}
	var payload map[string]any
	if err := json.Unmarshal(got[0].Body, &payload); err != nil {
		t.Fatalf("收到的 body 不是 JSON: %v (%s)", err, got[0].Body)
	}
	if payload["title"] != testNotificationTitle {
		t.Fatalf("body.title = %v, 期望 %q", payload["title"], testNotificationTitle)
	}

	// 2) 投递审计真的落了一行 delivered, 且状态码/尝试次数正确。
	var delivery models.NotificationDelivery
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := db.Where("channel_id = ?", id).First(&delivery).Error
		if err == nil && delivery.State == models.DeliveryStateDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("投递审计未落到 delivered: err=%v state=%q", err, delivery.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if delivery.NotificationID != notificationID {
		t.Fatalf("审计行的 notification_id = %d, 期望 %d", delivery.NotificationID, notificationID)
	}
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("审计行 status_code = %d, 期望 200", delivery.StatusCode)
	}
	if delivery.AttemptNo != 1 {
		t.Fatalf("审计行 attempt_no = %d, 期望 1", delivery.AttemptNo)
	}
	if delivery.ErrorMessage != "" {
		t.Fatalf("成功投递的 error_message 应为空: %q", delivery.ErrorMessage)
	}
	if delivery.DurationMs < 0 {
		t.Fatalf("审计行 duration_ms = %d", delivery.DurationMs)
	}

	// 3) 审计能通过 API 查到 (用户可见), 且不泄露密钥。
	page := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?channel_id="+uitoa(id), ""))
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("投递审计 API total/len = %d/%d, 期望 1/1", page.Total, len(page.Items))
	}
	if page.Items[0]["state"] != models.DeliveryStateDelivered {
		t.Fatalf("审计 API state = %v", page.Items[0]["state"])
	}
}

// TestNotificationChannel_Test_NoEngineAndUnknownChannel 覆盖两个错误路径。
func TestNotificationChannel_Test_NoEngineAndUnknownChannel(t *testing.T) {
	// 未装配投递引擎 (与 routes.go 里 datasourceSvc==nil 的既有处理同思路)。
	db := testutil.OpenTestDB(t)
	r := setupRouter()
	registerNotificationChannelRoutes(r.Group("/api/v1"), db, nil)
	created := createChannelThroughAPI(t, r, `{"name":"无引擎","type":"webhook","target_url":"https://example.com/hook"}`)
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels/"+uitoa(channelIDOf(t, created))+"/test", ""), http.StatusServiceUnavailable)

	// 通道不存在 → 404 (即使引擎在场), 且不得触发投递。
	r2, _, tester := newChannelRouterWithTester(t)
	requireErrorStatus(t, doChannelRequest(t, r2, http.MethodPost, "/api/v1/notification-channels/424242/test", ""), http.StatusNotFound)
	if tester.count() != 0 {
		t.Fatalf("不存在的通道不应触发投递, 实际 %d 次", tester.count())
	}
}

// TestNotificationChannel_Test_DisabledChannelStillReachable 断言 /test 对
// enabled=false / min_level=critical 的通道仍然真的发起投递。
//
// 这条测试锁的是一个**易被写错且难以察觉**的语义: 若 /test 复用常规投递路径
// (DeliverAsync), 调度器的 enabledChannels 查询会把 disabled 通道直接筛掉,
// 端点仍会返回 200 并落一条 notifications 行, 却**一个字节都不发出去** ——
// 用户点"测试"看到"已发出"而实际什么都没发生 (假绿)。设计 §4 要的是
// "让用户能立即验证配置对不对", 因此这里必须断言投递**确实发生**。
func TestNotificationChannel_Test_DisabledChannelStillReachable(t *testing.T) {
	r, db, tester := newChannelRouterWithTester(t)
	created := createChannelThroughAPI(t, r, `{"name":"已禁用","type":"webhook","target_url":"https://example.com/hook","enabled":false,"min_level":"critical"}`)
	id := channelIDOf(t, created)
	if created["enabled"] != false {
		t.Fatalf("创建时显式 enabled=false 未生效: %v", created)
	}

	// 反面证据: 同一个通道对 info 级的**常规**通知确实会被级别过滤挡掉 ——
	// 证明"能收到测试消息"不是因为 min_level 被忽略了。
	row := loadChannelRow(t, db, id)
	if notify.ShouldDeliver(row, notify.Message{Type: models.AlertLevelInfo}) {
		t.Fatalf("min_level=critical 的通道不应接收 info 级常规通知")
	}

	w := doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels/"+uitoa(id)+"/test", "")
	if w.Code != http.StatusOK {
		t.Fatalf("禁用通道的测试投递失败: %d %s", w.Code, w.Body.String())
	}
	call := tester.waitCall(t, 2*time.Second)
	if call.Channel.ID != id {
		t.Fatalf("投递目标通道 = %d, 期望 %d (必须直达用户点的那条)", call.Channel.ID, id)
	}
	if call.Message.Title != testNotificationTitle {
		t.Fatalf("投递的消息标题 = %q", call.Message.Title)
	}
	if call.Message.Source != testNotificationSource {
		t.Fatalf("投递的消息 source = %q", call.Message.Source)
	}
	// 通道的持久化配置必须原样交给投递器 (否则自检就不是真实的出站形状)。
	if call.Channel.TargetURL != "https://example.com/hook" {
		t.Fatalf("投递用的 target_url = %q", call.Channel.TargetURL)
	}
	if call.Channel.Enabled {
		t.Fatalf("测试投递应使用库里真实的 enabled=false 通道")
	}
}

// =====================================================================
// ⑥ 分页: page / page_size 必须真的落到后端 SQL (LIMIT/OFFSET)
// =====================================================================

// sqlCaptureLogger 捕获 GORM 实际发出的 SQL, 用来证明分页不是"取全量再内存切片"。
type sqlCaptureLogger struct {
	mu  sync.Mutex
	sql []string
}

func (l *sqlCaptureLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }
func (l *sqlCaptureLogger) Info(context.Context, string, ...interface{})     {}
func (l *sqlCaptureLogger) Warn(context.Context, string, ...interface{})     {}
func (l *sqlCaptureLogger) Error(context.Context, string, ...interface{})    {}

func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	l.mu.Lock()
	l.sql = append(l.sql, query)
	l.mu.Unlock()
}

func (l *sqlCaptureLogger) queries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sql...)
}

func (l *sqlCaptureLogger) reset() {
	l.mu.Lock()
	l.sql = nil
	l.mu.Unlock()
}

func (l *sqlCaptureLogger) findWithLimitOffset() (string, bool) {
	for _, query := range l.queries() {
		upper := strings.ToUpper(query)
		if strings.Contains(upper, "LIMIT") && strings.Contains(upper, "OFFSET") {
			return query, true
		}
	}
	return "", false
}

func seedChannels(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		channel := models.NewNotificationChannel("通道"+strconv.Itoa(i), models.ChannelTypeWebhook, "https://example.com/hook", nil, nil)
		if err := db.Create(&channel).Error; err != nil {
			t.Fatalf("造通道失败: %v", err)
		}
	}
}

func TestNotificationChannels_PaginationIsBackendPaged(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	capture := &sqlCaptureLogger{}
	r := setupRouter()
	registerNotificationChannelRoutes(r.Group("/api/v1"), db.Session(&gorm.Session{Logger: capture}), nil)
	seedChannels(t, db, 25)

	// 默认页: 20 条, total 如实反映全量 25 条。
	first := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels", ""))
	if len(first.Items) != 20 || first.Total != 25 {
		t.Fatalf("默认页 items/total = %d/%d, 期望 20/25", len(first.Items), first.Total)
	}
	if first.Page != 1 || first.PageSize != 20 {
		t.Fatalf("默认页回显 = %d/%d, 期望 1/20", first.Page, first.PageSize)
	}
	// id DESC 契约: 首页第一条是最大 id。
	if got := uint(first.Items[0]["id"].(float64)); got != 25 {
		t.Fatalf("首页首条 id = %d, 期望 25 (id DESC)", got)
	}

	// 第二页: 5 条, total 不变, 且与首页无交集 —— 只有真分页才可能。
	capture.reset()
	second := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels?page=2&page_size=20", ""))
	if len(second.Items) != 5 || second.Total != 25 {
		t.Fatalf("第二页 items/total = %d/%d, 期望 5/25", len(second.Items), second.Total)
	}
	if got := uint(second.Items[0]["id"].(float64)); got != 5 {
		t.Fatalf("第二页首条 id = %d, 期望 5", got)
	}
	firstIDs := map[float64]bool{}
	for _, item := range first.Items {
		firstIDs[item["id"].(float64)] = true
	}
	for _, item := range second.Items {
		if firstIDs[item["id"].(float64)] {
			t.Fatalf("第二页与首页出现重复项 id=%v", item["id"])
		}
	}

	// 后端分页的铁证: 第二页的 SQL 里必须同时出现 LIMIT 与 OFFSET
	// (内存切片式"假分页"不会有 OFFSET)。
	query, ok := capture.findWithLimitOffset()
	if !ok {
		t.Fatalf("第二页请求未产生带 LIMIT+OFFSET 的 SQL, 疑似假分页; 已捕获: %v", capture.queries())
	}
	if !strings.Contains(strings.ToUpper(query), "FROM") {
		t.Fatalf("捕获到的分页 SQL 形态异常: %s", query)
	}

	// 越界页: 空集 + total 不变 (不得退化成第一页, 也不得报错)。
	overflow := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels?page=99&page_size=20", ""))
	if len(overflow.Items) != 0 || overflow.Total != 25 {
		t.Fatalf("越界页 items/total = %d/%d, 期望 0/25", len(overflow.Items), overflow.Total)
	}
	// 非法参数回落: page_size 超上限 / 非数字。
	clamped := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels?page=0&page_size=9999", ""))
	if clamped.Page != 1 || clamped.PageSize != 20 {
		t.Fatalf("非法参数回显 = %d/%d, 期望 1/20", clamped.Page, clamped.PageSize)
	}
	if len(clamped.Items) != 20 {
		t.Fatalf("非法参数下条数 = %d, 期望 20", len(clamped.Items))
	}
}

// =====================================================================
// ⑦ 投递审计: 过滤 + 分页
// =====================================================================

func seedDeliveries(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		state := models.DeliveryStateDelivered
		if i%5 == 0 {
			state = models.DeliveryStateFailed
		}
		row := models.NotificationDelivery{
			NotificationID: uint(i + 1), ChannelID: 7, State: state,
			AttemptNo: 1, StatusCode: 200, CreatedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("造审计行失败: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		row := models.NotificationDelivery{
			NotificationID: uint(100 + i), ChannelID: 8, State: models.DeliveryStateFailed,
			AttemptNo: 1, StatusCode: 500, CreatedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("造审计行失败: %v", err)
		}
	}
}

func TestNotificationDeliveries_FilterAndPagination(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	capture := &sqlCaptureLogger{}
	r := setupRouter()
	registerNotificationChannelRoutes(r.Group("/api/v1"), db.Session(&gorm.Session{Logger: capture}), nil)
	seedDeliveries(t, db)

	// 全量: 28 条, 默认页 20 条。
	all := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries", ""))
	if all.Total != 28 || len(all.Items) != 20 {
		t.Fatalf("全量 total/len = %d/%d, 期望 28/20", all.Total, len(all.Items))
	}

	// 显式 page/page_size: 第三页 8 条 (28 = 10+10+8)。
	capture.reset()
	third := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?page=3&page_size=10", ""))
	if third.Total != 28 || len(third.Items) != 8 || third.Page != 3 || third.PageSize != 10 {
		t.Fatalf("第三页 total/len/page/page_size = %d/%d/%d/%d, 期望 28/8/3/10",
			third.Total, len(third.Items), third.Page, third.PageSize)
	}
	if _, ok := capture.findWithLimitOffset(); !ok {
		t.Fatalf("审计列表未产生带 LIMIT+OFFSET 的 SQL, 疑似假分页: %v", capture.queries())
	}

	// channel_id 过滤: 25 条; 分页与过滤叠加时 total 必须如实反映过滤后的条数。
	byChannel := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?channel_id=7&page=2&page_size=10", ""))
	if byChannel.Total != 25 || len(byChannel.Items) != 10 {
		t.Fatalf("channel_id=7 第二页 total/len = %d/%d, 期望 25/10", byChannel.Total, len(byChannel.Items))
	}
	for _, item := range byChannel.Items {
		if uint(item["channel_id"].(float64)) != 7 {
			t.Fatalf("channel_id 过滤失效: %v", item)
		}
	}

	// state 过滤: 失败 5 (channel 7) + 3 (channel 8) = 8。
	failed := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?state=failed", ""))
	if failed.Total != 8 {
		t.Fatalf("state=failed total = %d, 期望 8", failed.Total)
	}
	for _, item := range failed.Items {
		if item["state"] != models.DeliveryStateFailed {
			t.Fatalf("state 过滤失效: %v", item)
		}
	}
	// 组合过滤: channel 7 的失败 = 5。
	combo := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?channel_id=7&state=failed", ""))
	if combo.Total != 5 {
		t.Fatalf("组合过滤 total = %d, 期望 5", combo.Total)
	}

	// 越界页: 空集, total 不变。
	overflow := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?page=99", ""))
	if len(overflow.Items) != 0 || overflow.Total != 28 {
		t.Fatalf("越界页 items/total = %d/%d, 期望 0/28", len(overflow.Items), overflow.Total)
	}

	// 非法过滤值 → 400 (不静默忽略, 否则用户以为过滤生效了)。
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?channel_id=abc", ""), http.StatusBadRequest)
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries?state=bogus", ""), http.StatusBadRequest)
}

// =====================================================================
// ⑧ 校验与 404
// =====================================================================

func TestNotificationChannel_ValidationAndNotFound(t *testing.T) {
	r, _, _ := newChannelRouter(t)

	requireErrorStatus(t, doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels", `{"type":"webhook","target_url":"https://example.com/h"}`), http.StatusBadRequest)
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels", `{"name":"x","type":"telegram","target_url":"https://example.com/h"}`), http.StatusBadRequest)
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodPost, "/api/v1/notification-channels", `{"name":"x","type":"webhook","target_url":"ftp://example.com/h"}`), http.StatusBadRequest)

	requireErrorStatus(t, doChannelRequest(t, r, http.MethodPut, "/api/v1/notification-channels/424242", `{"name":"nope"}`), http.StatusNotFound)
	requireErrorStatus(t, doChannelRequest(t, r, http.MethodDelete, "/api/v1/notification-channels/424242", ""), http.StatusNotFound)

	// 空库列表: items 必须是 [] 而不是 null。
	empty := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-channels", ""))
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("空库 total/len = %d/%d", empty.Total, len(empty.Items))
	}
	emptyDeliveries := decodePage(t, doChannelRequest(t, r, http.MethodGet, "/api/v1/notification-deliveries", ""))
	if emptyDeliveries.Total != 0 || len(emptyDeliveries.Items) != 0 {
		t.Fatalf("空库审计 total/len = %d/%d", emptyDeliveries.Total, len(emptyDeliveries.Items))
	}
}
