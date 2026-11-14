package api

// handler_geo_test.go — /system/geo/* 的契约（2026-11-14）。
//
// 这两个端点是"坐标只能来自 IP 或城市"的落点：前端拿到结果后把经纬度/时区
// 一起写进规则，时区不再有输入框。因此这里必须验：
//   1. 城市搜索能按中文/拼音命中，并且**时区随城市返回**；
//   2. IP 定位成功时返回 ip 来源 + 合法时区；
//   3. 外部定位全部失败时返回 503 且文案引导用户改用手动城市（不能 500 吓人）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ehome/backend/internal/geo"

	"github.com/gin-gonic/gin"
)

func newGeoRouter() *gin.Engine {
	r := gin.New()
	registerGeoRoutes(r.Group("/api/v1"))
	return r
}

func decodeGeoEnvelope(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var env struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, body)
	}
	if env.Code != 200 {
		t.Fatalf("code = %d (%s)", env.Code, body)
	}
	return env.Data
}

func TestGeoCitiesEndpoint(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/geo/cities?q="+url.QueryEscape("上海")+"&limit=5", nil)
	newGeoRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	data := decodeGeoEnvelope(t, w.Body.Bytes())
	items, _ := data["items"].([]interface{})
	if len(items) == 0 {
		t.Fatalf("上海没有搜索结果: %s", w.Body.String())
	}
	first := items[0].(map[string]interface{})
	if first["name"] != "上海" {
		t.Fatalf("首条 = %v", first["name"])
	}
	if first["timezone"] != "Asia/Shanghai" {
		t.Fatalf("时区没有随城市返回: %v", first["timezone"])
	}
	// 时区必须是可加载的 IANA 名（否则保存后日出计算会在运行期失败）。
	if !geo.ValidTimezone(first["timezone"].(string)) {
		t.Fatalf("非法时区: %v", first["timezone"])
	}
}

func TestGeoIPEndpointSuccess(t *testing.T) {
	old := geoIPResolver
	geoIPResolver = func(context.Context) (geo.IPLocation, error) {
		return geo.IPLocation{Latitude: 31.22, Longitude: 121.46, Timezone: "Asia/Shanghai",
			City: "上海", Country: "中国", Source: "ip"}, nil
	}
	defer func() { geoIPResolver = old }()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/geo/ip", nil)
	newGeoRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	data := decodeGeoEnvelope(t, w.Body.Bytes())
	if data["source"] != "ip" || data["timezone"] != "Asia/Shanghai" {
		t.Fatalf("data = %v", data)
	}
}

func TestGeoIPEndpointUnavailable(t *testing.T) {
	old := geoIPResolver
	geoIPResolver = func(context.Context) (geo.IPLocation, error) {
		return geo.IPLocation{}, errors.New("all providers failed")
	}
	defer func() { geoIPResolver = old }()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/geo/ip", nil)
	newGeoRouter().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "手动选择城市") {
		t.Fatalf("文案没有引导手动城市: %s", w.Body.String())
	}
}
