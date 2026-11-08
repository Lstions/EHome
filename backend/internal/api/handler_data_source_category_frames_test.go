package api

// handler_data_source_category_frames_test.go — category 校验的帧表兜底验收（2026-11-08）。
//
// ============ 守什么 ============
//
// 停写后 dataSourceCategoryObserved 的 unified_data 兜底恒为空，于是：
//
//	投影表还没 flush（每 observe_interval 默认 60s 一次）
//	=> 两个来源都空 => 400 category_not_observed
//	=> 用户刚配好字段、刚上报成功，却被拒绝建数据源
//	=> 他以为自己填错了字段名
//
// 这条用例造的就是那个窗口：**投影表空、帧表有**。
//
// 变异验证：把 dataSourceCategoryObserved 的帧表兜底删掉（回退到 unified_data）
// => 本用例 400 => 红。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// newDataSourceCategoryFramesAPI 建一个带帧表的 router（本文件专用）。
//
// 与 newDataSourceCategoryAPI 的差别只有两处：多建帧表、多注入 slot 字典。
// 其余（resolver 注入、路由、鉴权）完全一致 —— 那正是要对比的环境。
func newDataSourceCategoryFramesAPI(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	// ⚠ 这里**不能** AutoMigrate(&models.TelemetryFrame{})：GORM 会给平表加一个
	// 自增 id 列，而 MigrateTelemetryFrameToPartitioned 的月拷贝按
	// (device_id, ts, seq) 搬（帧表本来没有 id 列）=> 报 column "id" does not exist。
	// 与既有 PG 用例一致：只 AutoMigrate 非帧表，帧表交给 datalifecycle 建。
	if err := db.AutoMigrate(&models.SensorFieldObs{}, &models.SensorFieldMeta{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := datasource.New(db, datasource.Options{})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataSourceRoutes(v1.Group("/data-sources"), svc, db)
	return r, db
}

// TestDataSourceCategory_AcceptsFieldSeenOnlyInFrames 是核心断言。
//
// 场景：投影表**空**（刚上报、还没到 flush 点），帧表里已经有该字段。
// 期望：创建成功（201）。停写前这里会因 unified_data 兜底而误拒。
func TestDataSourceCategory_AcceptsFieldSeenOnlyInFrames(t *testing.T) {
	requirePostgresAPI(t) // 帧表分区与 slot JSON 只在 PG 上可信
	withDataSourceCategoryResolver(t, true)
	r, db := newDataSourceCategoryFramesAPI(t)

	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM sensor_field_obs")
	db.Exec("DELETE FROM unified_data")

	const dev = 9971
	// 造一帧，字段含 rsoc；**不写 sensor_field_obs**（模拟 60s flush 窗口内）。
	now := time.Now().UTC()
	slot := telemetry.Slot{ID: 1, Fields: []telemetry.SlotField{
		{Name: "rsoc", Min: 0, Step: 0.1, Bits: 16},
	}}
	raw, err := slot.MarshalFields()
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	if err := db.Create(&models.TelemetrySlot{
		SlotID: 1, Signature: "sig-rsoc", SpecVersion: 1,
		FieldCount: 1, FieldsJSON: raw,
	}).Error; err != nil {
		t.Fatalf("create slot: %v", err)
	}
	if err := db.Create(&models.TelemetryFrame{
		DeviceID: dev, TS: now, Seq: 0,
		FrameKind: models.TelemetryFrameKindKey, SlotID: 1, Payload: []byte{0x00},
	}).Error; err != nil {
		t.Fatalf("create frame: %v", err)
	}

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"category": "rsoc", "edge_device_id": dev,
	})
	if w.Code != http.StatusCreated {
		var resp map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		t.Fatalf("status = %d, want 201（帧表里已有 rsoc；"+
			"返回 400 category_not_observed 说明帧表兜底没生效，用户会以为自己填错字段名）body=%s",
			w.Code, w.Body.String())
	}
}

// TestDataSourceCategory_StillRejectsUnknownField 负向对照：两个来源都没有 => 仍然 400。
//
// 换了数据源**不能**把校验变成「一律放行」—— 那等于没校验。
func TestDataSourceCategory_StillRejectsUnknownField(t *testing.T) {
	requirePostgresAPI(t)
	withDataSourceCategoryResolver(t, true)
	r, db := newDataSourceCategoryFramesAPI(t)

	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM sensor_field_obs")
	db.Exec("DELETE FROM unified_data")

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"category": "never_seen_field", "edge_device_id": 9972,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（两个来源都没有该字段，必须拒绝；"+
			"放行 = 校验失效）body=%s", w.Code, w.Body.String())
	}
}
