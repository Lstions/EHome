package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

// 字段目录端点（GET /data-sources/overview）的契约测试。
//
// # 这个端点的语义（2026-10-23 重做）
//
// **只读的「已接入字段目录」** —— 回答"设备解析成功之后，系统里有哪些字段、
// 什么类型、什么单位、最新值多少、多久前更新的"。
//
// 它**不是**主备来源配置台账。早期版本把 data_sources（主备配置表）当字段
// 目录渲染，输出了 group_id/kind/members 等配置概念。本文件用反向断言锁住：
// 那些字段不得再出现在响应里。

func ovDB(t *testing.T) *gorm.DB {
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

func ovRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSourceOverviewRoute(r.Group("/api/v1/data-sources"), db)
	return r
}

type ovItem struct {
	EdgeDeviceID   uint     `json:"edge_device_id"`
	EdgeDeviceName string   `json:"edge_device_name"`
	Category       string   `json:"category"`
	LabelZh        string   `json:"label_zh"`
	ValueType      string   `json:"value_type"`
	Unit           string   `json:"unit"`
	LatestValue    *float64 `json:"latest_value"`
	UpdatedAt      *string  `json:"updated_at"`
	AgeSec         *int     `json:"age_sec"`
	Status         string   `json:"status"`
}

type ovResp struct {
	Data struct {
		Items       []ovItem `json:"items"`
		Total       int      `json:"total"`
		GeneratedAt string   `json:"generated_at"`
	} `json:"data"`
}

func ovFetch(t *testing.T, r *gin.Engine) ovResp {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body ovResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	return body
}

// TestOverview_EmptyIsValid 空表返回合法空结构（不是 null）。
func TestOverview_EmptyIsValid(t *testing.T) {
	defer resetLatestValueCacheForTest()
	body := ovFetch(t, ovRouter(ovDB(t)))
	if body.Data.Items == nil {
		t.Error("items 是 null，应为空数组（前端会崩）")
	}
	if body.Data.Total != 0 {
		t.Errorf("total = %d, want 0", body.Data.Total)
	}
	if body.Data.GeneratedAt == "" {
		t.Error("generated_at 缺失")
	}
}

// TestOverview_FieldFacts 验证字段事实四要素（描述/类型/单位/实时值/更新时间）。
func TestOverview_FieldFacts(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	// 字段目录的顶层身份就是边缘设备本身：造设备只为拿到展示用的名字
	// （ID 与下面 DataSource.EdgeDeviceID 对齐，14）。
	db.Create(&models.EdgeDevice{ID: 14, Name: "bms-1", Type: "bms", NodeID: "n1", ChannelID: 1})
	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 14,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.SensorFieldMeta{SensorName: "rsoc", LabelZh: "剩余电量百分比",
		ValueType: "number", Status: "active"})
	db.Create(&models.SensorFieldObs{DeviceID: 14, SensorName: "rsoc", Unit: "%",
		FirstSeen: time.Now(), LastSeen: time.Now(), Samples: 5})
	SetLatestValue(models.UnifiedData{DeviceID: 14, SensorName: "rsoc", Value: 87,
		Unit: "%", Timestamp: time.Now().Add(-5 * time.Second)})

	body := ovFetch(t, ovRouter(db))
	if body.Data.Total != 1 {
		t.Fatalf("total = %d, want 1", body.Data.Total)
	}
	it := body.Data.Items[0]
	if it.EdgeDeviceID != 14 {
		t.Errorf("edge_device_id = %d, want 14", it.EdgeDeviceID)
	}
	if it.EdgeDeviceName != "bms-1" {
		t.Errorf("edge_device_name = %q, want bms-1", it.EdgeDeviceName)
	}
	if it.LabelZh != "剩余电量百分比" {
		t.Errorf("label_zh = %q", it.LabelZh)
	}
	if it.ValueType != "number" {
		t.Errorf("value_type = %q", it.ValueType)
	}
	if it.Unit != "%" {
		t.Errorf("unit = %q", it.Unit)
	}
	if it.LatestValue == nil || *it.LatestValue != 87 {
		t.Errorf("latest_value = %v, want 87", it.LatestValue)
	}
	if it.UpdatedAt == nil {
		t.Error("updated_at 缺失 —— 前端无法显示更新时间")
	}
	if it.AgeSec == nil {
		t.Error("age_sec 缺失")
	}
}

// TestOverview_NoConfigConcepts 是**反向断言**。
//
// 响应里不得出现主备配置概念（group_id/kind/members）。它们是「来源组」页
// 的职责；混进只读字段目录会造成：
//   - 用户以为能在这里配置（实际只读）；
//   - 「当前来源」与「逻辑设备」重复显示同一台设备；
//   - 「成员」在单设备时恒为 1，零信息量。
func TestOverview_NoConfigConcepts(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	db.Create(&models.DataSource{Category: "rsoc", EdgeDeviceID: 14,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	// 即便存在来源组，响应也不应带组信息
	db.Create(&models.SourceGroup{ID: 5, Name: "组", Kind: "primary_backup",
		Category: "rsoc", Enabled: true})

	w := httptest.NewRecorder()
	ovRouter(db).ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/data-sources/overview", nil))
	raw := w.Body.String()

	for _, forbidden := range []string{`"group_id"`, `"group_name"`, `"members"`, `"member_count"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("响应包含主备配置字段 %s —— 它不属于只读字段目录：%s", forbidden, raw)
		}
	}
}

// TestOverview_Freshness 验证状态按数据新鲜度派生（不是配置状态）。
func TestOverview_Freshness(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	db.Create(&models.DataSource{Category: "a", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})

	cases := []struct {
		name   string
		ageSec int
		want   string
	}{
		{"5s 内 = ok", 5, "ok"},
		{"29s = ok", 29, "ok"},
		{"31s = delay", 31, "delay"},
		{"299s = delay", 299, "delay"},
		{"301s = stale", 301, "stale"},
		{"1h = stale", 3600, "stale"},
	}
	for _, tc := range cases {
		ts := time.Now().Add(-time.Duration(tc.ageSec) * time.Second)
		SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "a", Value: 1, Timestamp: ts})
		body := ovFetch(t, ovRouter(db))
		if body.Data.Items[0].Status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, body.Data.Items[0].Status, tc.want)
		}
	}
}

// TestOverview_NoDataStatus 无读数时状态为 nodata、值为 nil。
func TestOverview_NoDataStatus(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	db.Create(&models.DataSource{Category: "a", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})

	it := ovFetch(t, ovRouter(db)).Data.Items[0]
	if it.Status != "nodata" {
		t.Errorf("status = %q, want nodata", it.Status)
	}
	if it.LatestValue != nil {
		t.Errorf("latest_value = %v, want nil", it.LatestValue)
	}
	if it.UpdatedAt != nil {
		t.Errorf("updated_at = %v, want nil", it.UpdatedAt)
	}
}

// TestOverview_ZeroValueKept 读数 0 不被当成无数据。
func TestOverview_ZeroValueKept(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	db.Create(&models.DataSource{Category: "alarm", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "alarm", Value: 0,
		Timestamp: time.Now()})

	it := ovFetch(t, ovRouter(db)).Data.Items[0]
	if it.LatestValue == nil {
		t.Fatal("读数为 0 时 latest_value 为 nil —— 0 是合法读数")
	}
	if *it.LatestValue != 0 {
		t.Errorf("latest_value = %v, want 0", *it.LatestValue)
	}
	if it.Status != "ok" {
		t.Errorf("status = %q, want ok（刚收到数据）", it.Status)
	}
}

// TestOverview_DisabledRowDoesNotShadowActive 台账行状态排序不影响字段行的取值来源。
//
// 原用例（TestOverview_MultiMemberPicksActive）构造「同一字段两个成员, 取 active 那个」。
// 组键收敛为 (edge_device_id, category) 且唯一索引后, **同键两行不可构造** ——
// 多设备供同一字段是来源组的职责, 不是 data_sources 台账的。该用例因此失去对象。
//
// 保留下来的性质: 若同一 (设备, 类别) 存在非 active 的状态行（唯一索引放开或历史
// 脏数据），实时值必须仍来自 active 那条 —— 而不是"排序后第一个"。这里用两行
// 不同 (edge_device_id, category) 的独立字段行验证「字段目录不串行」: 各自的
// 实时值必须来自各自设备, 不能张冠李戴。
func TestOverview_SeparateFieldRowsKeepTheirOwnValues(t *testing.T) {
	defer resetLatestValueCacheForTest()
	db := ovDB(t)
	db.Create(&models.DataSource{Category: "v", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "a", Status: datasource.StatusStandby,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.DataSource{Category: "v", EdgeDeviceID: 2,
		SourceType: "edge_device", Name: "b", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	now := time.Now()
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "v", Value: 11, Timestamp: now})
	SetLatestValue(models.UnifiedData{DeviceID: 2, SensorName: "v", Value: 87, Timestamp: now})

	body := ovFetch(t, ovRouter(db))
	if body.Data.Total != 2 {
		t.Fatalf("total = %d, want 2（两个设备各一行字段目录）", body.Data.Total)
	}
	byDevice := map[uint]*float64{}
	for _, it := range body.Data.Items {
		byDevice[it.EdgeDeviceID] = it.LatestValue
	}
	if v, ok := byDevice[1]; !ok || v == nil || *v != 11 {
		t.Errorf("设备 1 的实时值 = %v, want 11（不得取到别的设备的读数）", v)
	}
	if v, ok := byDevice[2]; !ok || v == nil || *v != 87 {
		t.Errorf("设备 2 的实时值 = %v, want 87", v)
	}
}
