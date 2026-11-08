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
		&models.SourceGroup{}, &models.SensorFieldMeta{},
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
			EdgeDeviceID uint     `json:"edge_device_id"`
			Category     string   `json:"category"`
			LabelZh      string   `json:"label_zh"`
			ValueType    string   `json:"value_type"`
			Unit         string   `json:"unit"`
			LatestValue  *float64 `json:"latest_value"`
			AgeSec       *int     `json:"age_sec"`
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
	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 14,
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
	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 14,
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
	db.Create(&models.DataSource{Category: "alarm", EdgeDeviceID: 1,
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
	db.Create(&models.DataSource{Category: "flag", EdgeDeviceID: 1,
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

// TestOverview_RealtimeValuesAreNotCrossedBetweenDevices 是**取值不串行**测试。
//
// # 它防什么
//
// 字段目录按 (edge_device_id, category) 出**一行一个字段**。若实现把"哪个设备
// 的读数"搞错（例如拿第一行的 edge_device_id 去取所有行的缓存），用户会在设备 A
// 的字段行上看到设备 B 的读数 —— 数字看起来合理，但归属是错的，比空白更坏。
//
// # 为什么必须单独测
//
// 单设备用例无法暴露"取错设备"：只有一个设备时取谁都对。必须让两台设备上报
// **同一个字段名**、给**明显不同**的值, 才能验证归属。
//
// # 与已删用例的关系
//
// 2026-10-23（契约 §6）: 组键收敛为 (edge_device_id, category) 并带唯一索引,
// 原用例「同一键两个成员, 取 active 成员的值」所依赖的**同键两行**已不可构造
// （多设备供同一字段归来源组管）。取代它的是本用例: 两个**独立键**各自取值,
// 谁都不得污染谁。
func TestOverview_RealtimeValuesAreNotCrossedBetweenDevices(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := rtDB(t)

	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "a", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 2,
		SourceType: "edge_device", Name: "b", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})

	now := time.Now()
	// 设备 1 = 11，设备 2 = 87（明显不同, 取错立刻可辨）。
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "rsoc", Value: 11, Timestamp: now})
	SetLatestValue(models.UnifiedData{DeviceID: 2, SensorName: "rsoc", Value: 87, Timestamp: now})

	body := rtFetch(t, rtRouter(db))
	if len(body.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2（两个设备各一行）", len(body.Data.Items))
	}
	want := map[uint]float64{1: 11, 2: 87}
	for _, it := range body.Data.Items {
		if it.LatestValue == nil {
			t.Fatalf("设备 %d 的 latest_value 为空", it.EdgeDeviceID)
		}
		if exp := want[it.EdgeDeviceID]; *it.LatestValue != exp {
			t.Fatalf("设备 %d latest_value = %v, want %v —— 取到了别的设备的读数",
				it.EdgeDeviceID, *it.LatestValue, exp)
		}
	}
}
