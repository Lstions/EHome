package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== data_sources.category 保存期校验（方案 §9.2#8 / §9.4） ====================

// fakeSelectorResolver 是最小 selectorResolver 实现（只暴露 Enabled 供校验分支用）。
type fakeSelectorResolver struct{ enabled bool }

func (f fakeSelectorResolver) Resolve(uint, string) (sensorfield.Resolved, error) {
	return sensorfield.Resolved{}, nil
}
func (f fakeSelectorResolver) Enabled() bool { return f.enabled }

// withDataSourceCategoryResolver 注入假 resolver，并注册清理（避免跨用例串味）。
func withDataSourceCategoryResolver(t *testing.T, enabled bool) {
	t.Helper()
	SetFieldSelectorResolver(fakeSelectorResolver{enabled: enabled})
	t.Cleanup(func() { SetFieldSelectorResolver(nil) })
}

// newDataSourceCategoryAPI 构造含 POST /data-sources 的最小引擎（复用既有夹具）。
func newDataSourceCategoryAPI(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.SensorFieldObs{}, &models.SensorFieldMeta{}); err != nil {
		t.Fatalf("migrate sensor field tables: %v", err)
	}
	svc := datasource.New(db, datasource.Options{})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataSourceRoutes(v1.Group("/data-sources"), svc, db)
	return r, db
}

// TestDataSourceCategory_RejectsSpecialSelector 一期只支持字面量：selector 形态 400。
func TestDataSourceCategory_RejectsSpecialSelector(t *testing.T) {
	withDataSourceCategoryResolver(t, true)
	r, db := newDataSourceCategoryAPI(t)
	seed := seedDataSource(t, datasource.New(db, datasource.Options{}), datasource.CreateInput{
		DeviceID: 1, Category: "rsoc", EdgeDeviceID: 1,
	})
	_ = seed
	for _, category := range []string{"@soc", "temperature_*", "virtual:dc_power"} {
		w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
			"device_id": 2, "category": category, "edge_device_id": 2,
		})
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if w.Code != http.StatusBadRequest {
			t.Fatalf("category %q: status = %d want 400 (body=%s)", category, w.Code, w.Body.String())
		}
		if resp["error_code"] != "invalid_selector_syntax" {
			t.Fatalf("category %q: error_code = %v want invalid_selector_syntax", category, resp["error_code"])
		}
	}
}

// TestDataSourceCategory_RejectsUnknownField 未上报的物理名 400 category_not_observed。
func TestDataSourceCategory_RejectsUnknownField(t *testing.T) {
	withDataSourceCategoryResolver(t, true)
	r, _ := newDataSourceCategoryAPI(t)
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 2, "category": "rsoc", "edge_device_id": 2,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400 (body=%s)", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error_code"] != "category_not_observed" {
		t.Fatalf("error_code = %v want category_not_observed (body=%s)", resp["error_code"], w.Body.String())
	}
}

// TestDataSourceCategory_AcceptsObservedLiteral 已上报字面量 ⇒ 创建成功。
func TestDataSourceCategory_AcceptsObservedLiteral(t *testing.T) {
	withDataSourceCategoryResolver(t, true)
	r, db := newDataSourceCategoryAPI(t)
	// 投影表里登记该设备已上报 rsoc。
	if err := db.Create(&models.SensorFieldObs{
		DeviceID: 2, SensorName: "rsoc", Unit: "%",
	}).Error; err != nil {
		t.Fatalf("seed obs: %v", err)
	}
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 2, "category": "rsoc", "edge_device_id": 2,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d want 201 (body=%s)", w.Code, w.Body.String())
	}
}

// TestDataSourceCategory_SkippedWhenUnwired 未注入 resolver ⇒ 保持旧行为（不校验）。
func TestDataSourceCategory_SkippedWhenUnwired(t *testing.T) {
	SetFieldSelectorResolver(nil)
	r, _ := newDataSourceCategoryAPI(t)
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 3, "category": "@soc", "edge_device_id": 3,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("unwired resolver must keep legacy behavior: status = %d want 201 (body=%s)", w.Code, w.Body.String())
	}
}
