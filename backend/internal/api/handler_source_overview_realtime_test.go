package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

func rtDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DataSource{}, &models.EdgeDevice{},
		&models.LogicalDevice{}, &models.SourceGroup{}, &models.SensorFieldMeta{},
		&models.SensorFieldObs{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func rtRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSourceOverviewRoute(r.Group("/api/v1/data-sources"), db)
	return r
}

type rtResp struct {
	Data struct {
		Items []struct {
			Category    string   `json:"category"`
			LabelZh     string   `json:"label_zh"`
			ValueType   string   `json:"value_type"`
			Unit        string   `json:"unit"`
			LatestValue *float64 `json:"latest_value"`
			AgeSec      *int     `json:"age_sec"`
		} `json:"items"`
	} `json:"data"`
}

func rtFetch(t *testing.T, r *gin.Engine) rtResp {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))
	var body rtResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body
}

// TestOverview_FieldMetadata 验证 数据类型/描述/单位 三列有值。
func TestOverview_FieldMetadata(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)
	db.Create(&models.DataSource{DeviceID: 14, Category: "rsoc", EdgeDeviceID: 14,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.SensorFieldMeta{SensorName: "rsoc", LabelZh: "电池剩余电量",
		ValueType: "number", Status: "active"})
	db.Create(&models.SensorFieldObs{DeviceID: 14, SensorName: "rsoc", Unit: "%",
		FirstSeen: time.Now(), LastSeen: time.Now(), Samples: 10})

	body := rtFetch(t, rtRouter(db))
	if len(body.Data.Items) != 1 {
		t.Fatalf("items = %d", len(body.Data.Items))
	}
	it := body.Data.Items[0]
	if it.LabelZh != "电池剩余电量" {
		t.Errorf("label_zh = %q, want 电池剩余电量", it.LabelZh)
	}
	if it.ValueType != "number" {
		t.Errorf("value_type = %q, want number", it.ValueType)
	}
	if it.Unit != "%" {
		t.Errorf("unit = %q, want %%", it.Unit)
	}
}

// TestOverview_RealtimeValue 验证实时值来自最新值缓存。
func TestOverview_RealtimeValue(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)
	db.Create(&models.DataSource{DeviceID: 14, Category: "rsoc", EdgeDeviceID: 14,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	SetLatestValue(models.UnifiedData{DeviceID: 14, SensorName: "rsoc", Value: 87,
		Unit: "%", Timestamp: time.Now().Add(-2 * time.Second)})

	body := rtFetch(t, rtRouter(db))
	it := body.Data.Items[0]
	if it.LatestValue == nil {
		t.Fatal("latest_value 为空 —— 实时值未接入")
	}
	if *it.LatestValue != 87 {
		t.Errorf("latest_value = %v, want 87", *it.LatestValue)
	}
	if it.AgeSec == nil {
		t.Fatal("age_sec 为空 —— 前端无法区分实时与陈旧")
	}
	if *it.AgeSec < 0 || *it.AgeSec > 60 {
		t.Errorf("age_sec = %d，超出合理范围", *it.AgeSec)
	}
}

// TestOverview_ZeroValueIsNotMissing 验证读数 0 不被当成"无数据"。
func TestOverview_ZeroValueIsNotMissing(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)
	db.Create(&models.DataSource{DeviceID: 1, Category: "alarm", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "alarm", Value: 0,
		Timestamp: time.Now()})

	body := rtFetch(t, rtRouter(db))
	it := body.Data.Items[0]
	if it.LatestValue == nil {
		t.Fatal("读数为 0 时 latest_value 为 nil —— 0 是合法读数，不能与无数据混淆")
	}
	if *it.LatestValue != 0 {
		t.Errorf("latest_value = %v, want 0", *it.LatestValue)
	}
}

// TestOverview_ValueTypeInferredWhenNoMeta 验证无 meta 时按观测值推断类型。
func TestOverview_ValueTypeInferredWhenNoMeta(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)
	db.Create(&models.DataSource{DeviceID: 1, Category: "flag", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "flag", Value: 1,
		Timestamp: time.Now()})

	body := rtFetch(t, rtRouter(db))
	it := body.Data.Items[0]
	if it.ValueType != "boolean" {
		t.Errorf("value_type = %q, want boolean（0/1 应推断为开关量）", it.ValueType)
	}
}

// TestOverview_RealtimeValueFromAuthoritativeMemberOnly 是**主备取值正确性**测试。
//
// # 它防什么
//
// 主备组里只有 active 成员在供数。若总览取"任意成员"的值，就会显示一个
// 不供数的 standby 设备的读数 —— 用户会看到"这个来源的数据在变"，
// 但变的其实是备机（它可能已经离线、数据停在几小时前）。
//
// # 为什么必须单独测
//
// 变异测试发现：只有单成员的用例无法暴露"没按 active 过滤"这个缺陷
// （唯一成员恰好就是 active）。必须构造 1 active + 1 standby，
// 且给 standby 一个**不同的**最新值，才能验证取的是 active 那个。
func TestOverview_RealtimeValueFromAuthoritativeMemberOnly(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)

	// 两个成员供同一来源：设备 1 = active（权威），设备 2 = standby
	// ⚠ 刻意让 active 是**后**一个设备（ID 更大）：查询按 edge_device_id ASC 排序，
	// 若实现取"第一个成员"而不是"active 成员"，就会拿到 standby 的值。
	// 这个顺序安排是本测试有效性的关键 —— 反过来排会让缺陷被顺序掩盖。
	db.Create(&models.DataSource{DeviceID: 10, Category: "rsoc", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "a", Status: datasource.StatusStandby,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.DataSource{DeviceID: 10, Category: "rsoc", EdgeDeviceID: 2,
		SourceType: "edge_device", Name: "b", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})

	now := time.Now()
	// active 的读数是 87，standby 的读数是 11（明显不同）
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "rsoc", Value: 11,
		Timestamp: now})
	SetLatestValue(models.UnifiedData{DeviceID: 2, SensorName: "rsoc", Value: 87,
		Timestamp: now})

	body := rtFetch(t, rtRouter(db))
	if len(body.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1（两个成员聚合为一个来源）", len(body.Data.Items))
	}
	it := body.Data.Items[0]
	if it.LatestValue == nil {
		t.Fatal("latest_value 为空")
	}
	if *it.LatestValue != 87 {
		t.Fatalf("latest_value = %v, want 87（必须是 active 成员的值）—— "+
			"取到 %v 说明没有按 active 过滤，会显示不供数的备机读数",
			*it.LatestValue, *it.LatestValue)
	}
}
