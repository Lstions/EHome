package api

// 规则表单「日出/日落预览」契约门禁（P3，2026-11-18）。
//
// 钉三件事：
//  1. 正常坐标返回当天日出/日落（时刻取规则时区钟面值）；
//  2. 带 kind/offset 时算出真实 fireAt，并标出偏移跨本地午夜；
//  3. 极昼/极夜 At 必须为 nil（不得用零值时间冒充），非法参数 400。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func doSunPreview(t *testing.T, r *gin.Engine, query string) (int, models.SunTimesPreview) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/automation-rules/sun-times?"+query, nil)
	r.ServeHTTP(w, req)
	var env struct {
		Code    int                    `json:"code"`
		Data    models.SunTimesPreview `json:"data"`
		Message string                 `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, w.Body.String())
	}
	return w.Code, env.Data
}

func TestPreviewSunTimes(t *testing.T) {
	r, _ := newAutomationCooldownRouter(t)

	t.Run("北京当天日出日落", func(t *testing.T) {
		code, data := doSunPreview(t, r,
			"latitude=39.9042&longitude=116.4074&timezone=Asia/Shanghai&date=2026-11-18")
		if code != http.StatusOK {
			t.Fatalf("code = %d, want 200", code)
		}
		if data.Date != "2026-11-18" || data.Timezone != "Asia/Shanghai" {
			t.Fatalf("date/timezone = %q/%q", data.Date, data.Timezone)
		}
		if data.Sunrise.At == nil || data.Sunrise.At.Format("15:04") != "07:02" {
			t.Fatalf("sunrise = %v, want 07:02（规则时区）", data.Sunrise.At)
		}
		if data.Sunset.At == nil || data.Sunset.At.Format("15:04") != "16:56" {
			t.Fatalf("sunset = %v, want 16:56（规则时区）", data.Sunset.At)
		}
		if data.Selected != nil {
			t.Fatalf("未传 kind 时不得返回 selected: %+v", data.Selected)
		}
	})

	t.Run("偏移跨本地午夜要标出来", func(t *testing.T) {
		code, data := doSunPreview(t, r,
			"latitude=39.9042&longitude=116.4074&timezone=Asia/Shanghai&date=2026-06-21&kind=sunset&offset_min=300")
		if code != http.StatusOK {
			t.Fatalf("code = %d, want 200", code)
		}
		if data.Selected == nil {
			t.Fatal("传了 kind 必须返回 selected")
		}
		if !data.Selected.CrossesMidnight {
			t.Fatalf("crosses_midnight = false, want true（fire=%v event=%v）",
				data.Selected.FireAt, data.Selected.EventAt)
		}
		if data.Selected.FireAt == nil || data.Selected.FireAt.Format("2006-01-02 15:04") != "2026-06-22 00:46" {
			t.Fatalf("fire_at = %v, want 2026-06-22 00:46", data.Selected.FireAt)
		}
	})

	t.Run("极夜不得用零值时间冒充", func(t *testing.T) {
		code, data := doSunPreview(t, r,
			"latitude=78.22&longitude=15.65&timezone=Arctic/Longyearbyen&date=2026-12-21")
		if code != http.StatusOK {
			t.Fatalf("code = %d, want 200", code)
		}
		if data.Sunrise.At != nil || data.Sunrise.Status != "always_down" {
			t.Fatalf("sunrise = %+v, want status=always_down 且 at=nil", data.Sunrise)
		}
	})

	for _, c := range []struct {
		name  string
		query string
	}{
		{"纬度非数字", "latitude=abc&longitude=116.4&timezone=Asia/Shanghai"},
		{"纬度越界", "latitude=91&longitude=116.4&timezone=Asia/Shanghai"},
		{"时区非法", "latitude=39.9&longitude=116.4&timezone=Mars/Base"},
		{"kind 非法", "latitude=39.9&longitude=116.4&timezone=Asia/Shanghai&kind=noon"},
		{"偏移越界", "latitude=39.9&longitude=116.4&timezone=Asia/Shanghai&kind=sunrise&offset_min=721"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _ := doSunPreview(t, r, c.query)
			if code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", code)
			}
		})
	}
}
