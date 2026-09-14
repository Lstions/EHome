package api

// 这条测试锁的是**装配路径**本身, 而不是端点逻辑。
//
// 为什么必须有它: 本任务开工时的实测现状是 routes.go 里对 notification-channels
// **零命中** —— 6 个端点从未接到真实应用上, 于是"功能存在但用户永远到不了"。
// 若测试只调用 registerNotificationChannelRoutes (内部函数), 漏注册永远查不出来。
// 因此这里走**真实入口** SetupRoutes (main.go 用的就是它), 并用真实 HTTP 请求
// 逐条打过去: 404 即"没注册", 200 即"从路由到 DB 全链路可用"。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/notify"
	"ehome/backend/internal/websocket"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// seedInitializedAdminSession 造出"已初始化 + 系统管理员 + 有效会话"的最小状态,
// 因为 JWTAuthWithDB 走的是**权威会话校验** (auth_state + users.session_version),
// 光有 JWT 签名是过不去的。范式抄自 session_middleware_test.go。
func seedInitializedAdminSession(t *testing.T, db *gorm.DB) string {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now}).Error; err != nil {
		t.Fatalf("造 auth_state 失败: %v", err)
	}
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, InitializedAt: &now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}
	token, err := authservice.SignSessionToken(user, jwtSecret, time.Hour)
	if err != nil {
		t.Fatalf("签发会话令牌失败: %v", err)
	}
	return token
}

func bodyReader(body string) io.Reader {
	if body == "" {
		return nil
	}
	return strings.NewReader(body)
}

// TestNotificationChannelRoutesRegisteredInSetupRoutes 用真实装配入口逐个端点
// 打一遍, 断言 6 个端点全部可达且全链路 (路由 → 鉴权 → handler → DB) 正常。
func TestNotificationChannelRoutesRegisteredInSetupRoutes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if sqlDB, err := db.DB(); err == nil {
		// 内存 SQLite 每条新连接是空库; 投递在 goroutine 里读通道, 钉住连接池。
		sqlDB.SetMaxOpenConns(1)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 与 cmd/server/main.go 相同的装配路径: 最后一个 option 注入 Dispatcher。
	// nodeMgr 必须是真的 (registerDeviceRoutes 在注册期就会解引用它);
	// otaMgr/driverRegistry 传 nil 是安全的 (注册期不解引用)。
	nodeMgr := nodemgr.NewManager(db, nil, websocket.NewHub(), nil, nil, nil)
	SetupRoutes(r, db, websocket.NewHub(), nodeMgr, nil, nil, notify.NewDispatcher(db))
	token := seedInitializedAdminSession(t, db)

	// 先通过真实路由创建一条通道, 后续 PUT/DELETE/test 才有目标。
	steps := []struct {
		label  string
		method string
		path   string
		body   string
	}{
		{"列表(空库)", http.MethodGet, "/api/v1/notification-channels", ""},
		{"创建", http.MethodPost, "/api/v1/notification-channels", `{"name":"真实路由","type":"webhook","target_url":"https://example.com/hook","max_retries":0}`},
		{"更新", http.MethodPut, "/api/v1/notification-channels/1", `{"name":"改名"}`},
		{"测试投递", http.MethodPost, "/api/v1/notification-channels/1/test", ""},
		{"投递审计", http.MethodGet, "/api/v1/notification-deliveries?channel_id=1", ""},
		{"删除", http.MethodDelete, "/api/v1/notification-channels/1", ""},
	}
	for _, step := range steps {
		req := httptest.NewRequest(step.method, step.path, bodyReader(step.body))
		if step.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		// /api/v1 组上挂着 JWTAuthWithDB: 401/403 说明"路由存在但被鉴权挡住",
		// 404 才说明"路由根本没注册"。这里带真实 token 走完整链路。
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s %s %s 返回 404: 端点未在 SetupRoutes 中注册", step.label, step.method, step.path)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s %s = %d, 期望 200: %s", step.label, step.method, step.path, w.Code, w.Body.String())
		}
	}

	// 401 对照: 不带 token 时必须被鉴权挡住 (端点不是匿名可写的)。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notification-channels", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未带 token 的请求 = %d, 期望 401 (通道端点必须要求鉴权)", w.Code)
	}
}
