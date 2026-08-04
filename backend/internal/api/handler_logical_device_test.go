package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

func TestLogicalDeviceAPI_ListMergePreviewUpdate(t *testing.T) {
	r, db := setupTestRouter(t)
	// 两个逻辑设备, 各带软删实例 + 数据。
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ld1 := models.LogicalDevice{IdentityKey: "bms:0x76", Name: "BMS-A", DeviceType: "bms", RetentionDays: 365}
	ld2 := models.LogicalDevice{IdentityKey: "bms:0x77", Name: "BMS-B", DeviceType: "bms", RetentionDays: 365}
	if err := db.Create(&ld1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ld2).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&models.Node{NodeID: "N-LD", Name: "ld-node", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-LD", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev1 := models.EdgeDevice{Name: "inst1", Type: "bms", NodeID: "N-LD", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ld1.ID}
	dev2 := models.EdgeDevice{Name: "inst2", Type: "bms", NodeID: "N-LD", ChannelID: 1, HardwareID: "0x77", LogicalDeviceID: &ld2.ID}
	db.Create(&dev1)
	db.Create(&dev2)
	db.Delete(&dev1)
	db.Delete(&dev2)
	db.Create(&models.UnifiedData{DeviceID: dev1.ID, LogicalDeviceID: &ld1.ID, SensorName: "voltage", Unit: "V", Value: 48, Timestamp: base})
	db.Create(&models.UnifiedData{DeviceID: dev2.ID, LogicalDeviceID: &ld2.ID, SensorName: "voltage", Unit: "V", Value: 47, Timestamp: base.Add(time.Hour)})

	// GET /logical-devices — 列表含实例数/最后数据时间。
	req := httptest.NewRequest("GET", "/api/v1/logical-devices", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				ID            uint    `json:"id"`
				Name          string  `json:"name"`
				InstanceCount int64   `json:"instance_count"`
				LastDataAt    *string `json:"last_data_at"`
				RowEstimate   *int64  `json:"row_estimate"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if listResp.Data.Total != 2 {
		t.Fatalf("total = %d, want 2", listResp.Data.Total)
	}
	for _, item := range listResp.Data.Items {
		if item.InstanceCount != 1 {
			t.Errorf("%s instance_count = %d, want 1 (soft-deleted counted)", item.Name, item.InstanceCount)
		}
		if item.LastDataAt == nil {
			t.Errorf("%s last_data_at missing", item.Name)
		}
	}

	// POST /logical-devices/merge/preview — 时间范围 + 重叠。
	body, _ := json.Marshal(map[string]interface{}{"source_ids": []uint{ld1.ID, ld2.ID}})
	req = httptest.NewRequest("POST", "/api/v1/logical-devices/merge/preview", bytes.NewReader(body))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var previewResp struct {
		Data struct {
			Sources []struct {
				ID                uint       `json:"id"`
				FirstDataAt       *time.Time `json:"first_data_at"`
				LastDataAt        *time.Time `json:"last_data_at"`
				OverlapWithOthers bool       `json:"overlap_with_others"`
			} `json:"sources"`
			TargetRetentionDays int `json:"target_retention_days"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &previewResp)
	if len(previewResp.Data.Sources) != 2 || previewResp.Data.TargetRetentionDays <= 0 {
		t.Fatalf("preview payload unexpected: %s", w.Body.String())
	}

	// POST /logical-devices/merge — 成功, 返回 job_ids。
	body, _ = json.Marshal(map[string]interface{}{"target_name": "合并BMS", "source_ids": []uint{ld1.ID, ld2.ID}})
	req = httptest.NewRequest("POST", "/api/v1/logical-devices/merge", bytes.NewReader(body))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	var mergeResp struct {
		Data struct {
			TargetID uint   `json:"target_id"`
			JobIDs   []uint `json:"job_ids"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &mergeResp)
	if mergeResp.Data.TargetID == 0 || len(mergeResp.Data.JobIDs) != 2 {
		t.Fatalf("merge payload unexpected: %s", w.Body.String())
	}

	// GET /logical-devices/merge-jobs/:id — 进度。
	jobID := mergeResp.Data.JobIDs[0]
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/logical-devices/merge-jobs/%d", jobID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("merge job: %d %s", w.Code, w.Body.String())
	}
	var jobResp struct {
		Data struct {
			Status          string `json:"status"`
			SourceLogicalID uint   `json:"source_logical_id"`
			TargetLogicalID uint   `json:"target_logical_id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &jobResp)
	if jobResp.Data.Status != "pending" || jobResp.Data.TargetLogicalID != mergeResp.Data.TargetID {
		t.Fatalf("job payload unexpected: %s", w.Body.String())
	}

	// GET merge-jobs 404。
	req = httptest.NewRequest("GET", "/api/v1/logical-devices/merge-jobs/99999", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing job: %d, want 404", w.Code)
	}

	// PUT /logical-devices/:id — 改 name/retention_days。
	body, _ = json.Marshal(map[string]interface{}{"name": "合并后BMS", "retention_days": 730})
	req = httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/logical-devices/%d", mergeResp.Data.TargetID), bytes.NewReader(body))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var updated models.LogicalDevice
	db.First(&updated, mergeResp.Data.TargetID)
	if updated.Name != "合并后BMS" || updated.RetentionDays != 730 {
		t.Errorf("update not applied: name=%q retention=%d", updated.Name, updated.RetentionDays)
	}
	// identity_key 只读: PUT 不接受该字段。

	// PUT 非法 retention_days → 400。
	body, _ = json.Marshal(map[string]interface{}{"retention_days": 0})
	req = httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/logical-devices/%d", mergeResp.Data.TargetID), bytes.NewReader(body))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("zero retention: %d, want 400", w.Code)
	}
}

// TestLogicalDeviceAPI_Merge409Conflicts — 409 响应体带结构化 conflicts
// (D-1), 前端逐条呈现。
func TestLogicalDeviceAPI_Merge409Conflicts(t *testing.T) {
	r, db := setupTestRouter(t)
	ld1 := models.LogicalDevice{IdentityKey: "bms:0x76", Name: "BMS-A", DeviceType: "bms"}
	ld2 := models.LogicalDevice{IdentityKey: "bms:0x77", Name: "BMS-B", DeviceType: "bms"}
	db.Create(&ld1)
	db.Create(&ld2)
	db.Create(&models.Node{NodeID: "N-LD", Name: "ld-node", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-LD", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	// ld2 有存活实例 → alive_instance 冲突。
	living := models.EdgeDevice{Name: "live", Type: "bms", NodeID: "N-LD", ChannelID: 1, HardwareID: "0x77", LogicalDeviceID: &ld2.ID}
	db.Create(&living)

	body, _ := json.Marshal(map[string]interface{}{"target_name": "X", "source_ids": []uint{ld1.ID, ld2.ID}})
	req := httptest.NewRequest("POST", "/api/v1/logical-devices/merge", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("merge: %d, want 409: %s", w.Code, w.Body.String())
	}
	var conflictResp struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		Conflicts []struct {
			LogicalDeviceID uint   `json:"logical_device_id"`
			LogicalName     string `json:"logical_name"`
			Reason          string `json:"reason"`
			InstanceID      uint   `json:"instance_id"`
			InstanceName    string `json:"instance_name"`
			NodeName        string `json:"node_name"`
		} `json:"conflicts"`
	}
	json.Unmarshal(w.Body.Bytes(), &conflictResp)
	if conflictResp.Code != http.StatusConflict {
		t.Errorf("envelope code = %d, want 409", conflictResp.Code)
	}
	if len(conflictResp.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1: %s", len(conflictResp.Conflicts), w.Body.String())
	}
	c := conflictResp.Conflicts[0]
	if c.LogicalDeviceID != ld2.ID || c.Reason != "alive_instance" {
		t.Errorf("conflict = %+v", c)
	}
	if c.InstanceID != living.ID || c.InstanceName != "live" || c.NodeName != "ld-node" {
		t.Errorf("conflict instance detail = %+v", c)
	}

	// 整体回滚: 无 merge: 目标, ld1 未占位。
	var targets int64
	db.Model(&models.LogicalDevice{}).Where("identity_key LIKE ?", "merge:%").Count(&targets)
	if targets != 0 {
		t.Errorf("targets after 409 = %d, want 0", targets)
	}
	var reloaded models.LogicalDevice
	db.First(&reloaded, ld1.ID)
	if reloaded.MergedInto != nil {
		t.Error("ld1 occupied despite 409 rollback")
	}
}

// TestLogicalDeviceAPI_MergeValidation — 基础校验 400。
func TestLogicalDeviceAPI_MergeValidation(t *testing.T) {
	r, _ := setupTestRouter(t)
	for _, body := range []string{
		`{"target_name":"","source_ids":[1,2]}`,
		`{"target_name":"X","source_ids":[1]}`,
		`{"target_name":"X","source_ids":[]}`,
		`not-json`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/logical-devices/merge", bytes.NewReader([]byte(body)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: %d, want 400", body, w.Code)
		}
	}
}
