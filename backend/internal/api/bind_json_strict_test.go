package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestBindJSONStrict_RejectsTheProductionTypo 是 2026-10-04 的回归锁。
//
// 事故：/log-config 的字段名是 stream_enabled，但姊妹端点 /log-persist 用的是
// enabled。调用方顺手写成 {"enabled":false} 时，gin 默认静默忽略未知字段，
// 于是接口返回 HTTP 200 与 "log config updated, config sync triggered"，
// 其中 stream_enabled 回显为 null —— 开关根本没进 updates。
//
// 而开关根本没被改 —— 调用方以为日志已关，设备仍在推送。
//
// 本用例锁两件事：
//  1. 合法键名必须通过（不能把正常请求也挡掉）；
//  2. 写错的键名必须被拒绝并指出非法字段名 —— 这正是当初缺的那一层。
func TestBindJSONStrict_RejectsTheProductionTypo(t *testing.T) {
	type logConfigReq struct {
		StreamEnabled *bool `json:"stream_enabled"`
		Level         *int  `json:"level"`
	}

	bind := func(body string) error {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		var req logConfigReq
		return bindJSONStrict(c, &req)
	}

	// 1) 合法请求必须通过。
	if err := bind(`{"stream_enabled":false,"level":2}`); err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}

	// 2) 产线上的错键名必须被拒 —— 本缺陷的核心。
	err := bind(`{"enabled":false,"level":2}`)
	if err == nil {
		t.Fatal("未知字段 enabled 被静默接受：接口会返回 200 但开关不生效，调用方以为已关闭")
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Errorf("错误信息未指出非法字段名，调用方无法定位: %v", err)
	}

	// 3) 拼错的键同样拒绝。
	if err := bind(`{"stream_enabld":true}`); err == nil {
		t.Error("拼错的键名被静默接受")
	}

	// 4) 空对象仍交给调用方判断（本函数不负责必填校验）。
	if err := bind(`{}`); err != nil {
		t.Errorf("空对象不应报错（必填校验属于端点逻辑）: %v", err)
	}

	// 5) 尾部多余内容必须报错，否则会被静默丢弃。
	if err := bind(`{"level":2}{"level":3}`); err == nil {
		t.Error("尾部多余 JSON 被静默丢弃")
	}
}
