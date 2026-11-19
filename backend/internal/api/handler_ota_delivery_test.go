package api

// handler_ota_delivery_test.go — D2：下发失败必须与任务状态一致，不能留幽灵 pending。
//
// 背景（沙箱实测）：无设备会话时 POST /ota/tasks 返回 500，但任务行已落库为
// pending、30 分钟后被超时扫描改成 failed；客户端拿不到 task_id，重试还会再建一条。
// 选定方案 (a)：下发失败 → 任务置 failed（error_msg 保留原始失败原因）→ 503 +
// error_code=OTA_DELIVERY_FAILED + data.task_id，客户端能定位到这条记录。
// force 审计与任务行是**先落库**的（证据链），失败标记只改任务状态，绝不回滚/删除审计。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"
)

// deliveryEnvelope 是下发失败响应的形状（envelope + data.task_id）。
type deliveryEnvelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	ErrorCode string `json:"error_code"`
	Data      struct {
		TaskID uint   `json:"task_id"`
		OtaID  string `json:"ota_id"`
		Status string `json:"status"`
		Error  string `json:"error_msg"`
	} `json:"data"`
}

// TestOTACreateTask_NoDownlink_NoGhostPending 是 D2 的主用例：
// 无会话 + force 建任务 → 503 + task_id；任务行为 failed（不是 pending）；
// force 审计仍在（证据不随失败回滚）。
func TestOTACreateTask_NoDownlink_NoGhostPending(t *testing.T) {
	// 下发必定失败：模拟「节点没有活跃会话」。
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{err: errStubNoDownlink})
	node := seedGateHTTPNode(t, db, "NODE-NO-SESSION", "2.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})

	const reason = "same-version reflash, device offline"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks",
		createTaskBody(node.NodeID, fw.ID, map[string]interface{}{"force": true, "force_reason": reason})))
	t.Logf("no-session response: status=%d body=%s", w.Code, w.Body.String())

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (delivery failed), got %d: %s", w.Code, w.Body.String())
	}
	var env deliveryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.ErrorCode != "OTA_DELIVERY_FAILED" {
		t.Errorf("error_code = %q, want OTA_DELIVERY_FAILED", env.ErrorCode)
	}
	if env.Data.TaskID == 0 || env.Data.OtaID == "" {
		t.Errorf("response must identify the created task, got %+v", env.Data)
	}
	if env.Data.Status != ota.StatusFailed {
		t.Errorf("data.status = %q, want %q", env.Data.Status, ota.StatusFailed)
	}

	// 状态与响应一致：库里正是那条 failed 任务，且只有一条（没有幽灵第二条）。
	var rows []models.OTATask
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("task rows = %d, want exactly 1 (no duplicate on failure)", len(rows))
	}
	task := rows[0]
	if task.ID != env.Data.TaskID {
		t.Errorf("data.task_id = %d, want the persisted task %d", env.Data.TaskID, task.ID)
	}
	if task.Status != ota.StatusFailed {
		t.Errorf("task status = %q, want %q (ghost pending)", task.Status, ota.StatusFailed)
	}
	if !strings.Contains(task.ErrorMsg, "send failed") || !strings.Contains(task.ErrorMsg, "no downlink session") {
		t.Errorf("task error_msg = %q, want it to preserve the original delivery error", task.ErrorMsg)
	}
	if task.CompletedAt == nil {
		t.Error("failed task must have completed_at (terminal state)")
	}

	// force 审计是证据：任务失败不回滚、不删除它。
	var audits int64
	if err := db.Model(&models.SecurityAuditEvent{}).Where("event_name = ?", "ota.task.force_created").Count(&audits).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 1 {
		t.Errorf("force audit rows = %d, want 1 (evidence must survive delivery failure)", audits)
	}
}

// TestOTACreateTask_WithDownlink_Returns201 是对照组：有会话时创建成功、任务 pending。
func TestOTACreateTask_WithDownlink_Returns201(t *testing.T) {
	pub := &envelopeDownlinkStub{}
	r, db, _ := setupOTATestWithDownlink(t, pub)
	node := seedGateHTTPNode(t, db, "NODE-ONLINE", "1.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks", createTaskBody(node.NodeID, fw.ID, nil)))
	t.Logf("with-session response: status=%d body=%s", w.Code, w.Body.String())

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data models.OTATask `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if env.Data.Status != ota.StatusPending {
		t.Errorf("task status = %q, want %q", env.Data.Status, ota.StatusPending)
	}
	if env.Data.Forced {
		t.Error("plain create must not be forced")
	}
	if len(pub.payloads) != 1 {
		t.Errorf("OtaCmd publish count = %d, want 1", len(pub.payloads))
	}
	var persisted models.OTATask
	if err := db.First(&persisted, env.Data.ID).Error; err != nil {
		t.Fatalf("reload task %d: %v", env.Data.ID, err)
	}
	if persisted.Status != ota.StatusPending {
		t.Errorf("persisted status = %q, want pending", persisted.Status)
	}
}

// TestOTARollback_NoDownlink_NoGhostPending：同一类幽灵也存在于回滚路径
// （AutoRollback 也是「先建任务再下发」）。回复 503 + task_id，任务 failed。
func TestOTARollback_NoDownlink_NoGhostPending(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{err: errStubNoDownlink})
	node := seedGateHTTPNode(t, db, "NODE-RB-NO-SESSION", "2.0.0")
	stable := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "1.0.0", Elf: "elf-rb-stable"})
	now := time.Now()
	if err := db.Create(&models.OTATask{
		OtaID: "ota-prev-success", NodeID: node.NodeID, FirmwareID: stable.ID,
		Status: ota.StatusSuccess, ToVersion: "1.0.0", CompletedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed previous success: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, "/api/v1/ota/rollback/"+node.NodeID))
	t.Logf("rollback no-session response: status=%d body=%s", w.Code, w.Body.String())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	var env deliveryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.ErrorCode != "OTA_DELIVERY_FAILED" || env.Data.TaskID == 0 {
		t.Errorf("response = %+v, want OTA_DELIVERY_FAILED with task_id", env)
	}

	var rollbackTask models.OTATask
	if err := db.Where("node_id = ? AND source = ?", node.NodeID, firmwareid.SourceSystem).First(&rollbackTask).Error; err != nil {
		t.Fatalf("rollback task missing: %v", err)
	}
	if rollbackTask.Status != ota.StatusFailed {
		t.Errorf("rollback task status = %q, want failed (ghost pending)", rollbackTask.Status)
	}
	if rollbackTask.CompletedAt == nil {
		t.Error("failed rollback task must have completed_at")
	}
	if !strings.Contains(rollbackTask.ErrorMsg, "send failed") {
		t.Errorf("rollback task error_msg = %q, want the delivery error preserved", rollbackTask.ErrorMsg)
	}
}
