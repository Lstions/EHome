package api

// latest_data_no_fallback_test.go — 钉住「帧表不可用时 sensors 不渲染，但失败得干净」（2026-11-08）。
//
// ============ 为什么要专门钉「失败得干净」============
//
// 硬切换删掉 unified_data 回落之后，帧表 ok=false 是一个**我们主动选择的失败模式**：
// 列表行的 sensors 会不渲染（data_json 里没有 sensors 键）。
//
// 这类失败有两件事必须被钉住：
//  ① 不 500、不 panic —— 否则「主动降级」变成「接口挂掉」
//  ② sensors 确实不出现，且**不会退化成空数组**（空数组与缺键在前端语义不同：
//     缺键 => 走「解析失败」分支；空数组 => 「库里确实还没有数值」）
//
// 这三条（含 ③ 变异）是 Lead 2026-11-08 明确要求的。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestListSensors_NoFallbackWhenFramesUnavailable 是验收 ②。
func TestListSensors_NoFallbackWhenFramesUnavailable(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "nofb", Type: "bms_jbd", NodeID: "NOFB", HardwareID: "0x01"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	// 造一行**不带 sensors** 的 device_data（task-19 之后的线上形状），
	// 再让帧表不可用（SQLite 测试库没有 telemetry_frame 表 => frameDictionaryFor 返回 nil）。
	if err := db.Create(&models.DeviceData{
		DeviceID: dev.ID, NodeID: "NOFB",
		DataJSON:  `{"raw_hex":"ff","channel_id":1,"timestamp":1}`,
		Timestamp: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/data", nil)
	r.ServeHTTP(w, req)

	// ① 不 500、不 panic
	if w.Code != http.StatusOK {
		t.Fatalf("帧表不可用时列表页返回 %d（应 200 + 降级，而不是 5xx）：%s", w.Code, w.Body.String())
	}

	var list struct {
		Data struct {
			Items []struct {
				DataJSON string `json:"data_json"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(list.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Data.Items))
	}

	var dj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(list.Data.Items[0].DataJSON), &dj); err != nil {
		t.Fatalf("data_json 解析失败: %v", err)
	}
	// ② sensors 必须**不存在**（不是空数组）
	if _, has := dj["sensors"]; has {
		t.Fatalf("帧表不可用时仍写入了 sensors 键 —— 说明回落路径还在，硬切换没生效: %s",
			list.Data.Items[0].DataJSON)
	}
	t.Logf("帧表不可用 => 200 且 data_json 无 sensors 键（干净的降级）: %s",
		list.Data.Items[0].DataJSON)
}
