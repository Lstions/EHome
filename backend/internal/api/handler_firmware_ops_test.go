package api

// handler_firmware_ops_test.go — M1a/M1b 的 HTTP 面验收：
//   A. 建任务闸门：409 + data（HARDWARE_UNKNOWN / NOT_AN_UPGRADE）、force 放行 + 审计、
//      force 缺理由 400、硬件类 force 仍 409、手动回滚的闸门映射；
//   B. 列表：默认排除撤回、include_withdrawn、node_id 逐行 compatible/reasons、筛选与非法参数；
//   C. 撤回/删除：在途任务 409 FIRMWARE_IN_USE、成功落 withdrawn_at / 软删 + 审计。
//
// 用例里的 t.Logf 会打印真实响应体 —— 验收报告里的 409/200/列表样例即取自这里，
// 也方便以后 diff 契约时一眼看到形状变化。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"

	"gorm.io/gorm"
)

// ==================== 夹具 ====================

// gateFirmwareSeed 描述一行的身份/渠道；零值字段由 seedGateHTTPFirmware 补默认值。
type gateFirmwareSeed struct {
	Version       string
	Chip          string
	FlashMB       uint16
	Psram         string
	IdentityState string
	SizeBytes     uint64
	Channel       string
	Withdrawn     bool
	Elf           string
	// CreatedAt 非零时原样写入（GORM 只在零值时自动填），让排序用例可控。
	CreatedAt time.Time
}

func seedGateHTTPFirmware(t *testing.T, db *gorm.DB, seed gateFirmwareSeed) *models.Firmware {
	t.Helper()
	if seed.Version == "" {
		seed.Version = "2.0.0"
	}
	if seed.Chip == "" {
		seed.Chip = firmwareid.ChipC6
	}
	if seed.FlashMB == 0 {
		seed.FlashMB = 8
	}
	if seed.Psram == "" {
		seed.Psram = firmwareid.PSRAMNo
	}
	if seed.IdentityState == "" {
		seed.IdentityState = firmwareid.IdentityVerified
	}
	if seed.SizeBytes == 0 {
		seed.SizeBytes = 1024
	}
	if seed.Channel == "" {
		seed.Channel = models.FirmwareChannelStable
	}
	fw := &models.Firmware{
		Version:       seed.Version,
		Checksum:      "checksum-" + seed.Version,
		URL:           "http://example/fw.bin",
		Filename:      seed.Version + ".bin",
		SizeBytes:     seed.SizeBytes,
		Chip:          seed.Chip,
		FlashMB:       seed.FlashMB,
		Psram:         seed.Psram,
		ModelKey:      firmwareid.ModelKey(seed.Chip, seed.FlashMB),
		ElfSHA256:     seed.Elf,
		Channel:       seed.Channel,
		IdentityState: seed.IdentityState,
	}
	if seed.Withdrawn {
		now := time.Now().UTC()
		fw.WithdrawnAt = &now
	}
	if !seed.CreatedAt.IsZero() {
		fw.CreatedAt = seed.CreatedAt
	}
	if err := db.Create(fw).Error; err != nil {
		t.Fatalf("seed firmware %s: %v", seed.Version, err)
	}
	return fw
}

// seedGateHTTPNode 建一台与 c6/8MB/no-PSRAM 固件完全兼容的节点。
func seedGateHTTPNode(t *testing.T, db *gorm.DB, nodeID, runningVersion string) *models.Node {
	t.Helper()
	node := &models.Node{
		NodeID:          nodeID,
		Name:            nodeID,
		Model:           "esp32c6",
		Platform:        firmwareid.ChipC6,
		FirmwareVersion: runningVersion,
		HwFlashMB:       8,
		HwPsram:         firmwareid.PSRAMNo,
		Status:          "online",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("seed node %s: %v", nodeID, err)
	}
	return node
}

func gateHTTPRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", authHeader(t))
	return req
}

func createTaskBody(nodeID string, firmwareID uint, extra map[string]interface{}) string {
	payload := map[string]interface{}{"node_id": nodeID, "firmware_id": firmwareID}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// gateEnvelope 是 409/400 的 envelope 形状。
type gateEnvelope struct {
	Code      int                   `json:"code"`
	Message   string                `json:"message"`
	ErrorCode string                `json:"error_code"`
	Data      firmwareid.CompatData `json:"data"`
}

func decodeGateEnvelope(t *testing.T, body []byte) gateEnvelope {
	t.Helper()
	var env gateEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	return env
}

func countGateTasks(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.OTATask{}).Count(&count).Error; err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return count
}

// ==================== A. 建任务闸门 ====================

// TestOTACreateTask_HardwareUnknownReturns409WithFix 是 AC-8 的 409 样例：
// 硬件档案不完整 → error_code + data.reasons/node_hw/firmware/forceable/fix，且不写任务行。
func TestOTACreateTask_HardwareUnknownReturns409WithFix(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})
	node := seedGateHTTPNode(t, db, "NODE-HW-UNKNOWN", "1.0.0")
	// 制造「迁移后容量未识别」的真实形态（M0 迁移只能补 chip/psram）。
	if err := db.Model(&models.Node{}).Where("id = ?", node.ID).Update("hw_flash_mb", 0).Error; err != nil {
		t.Fatalf("clear node flash profile: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks", createTaskBody(node.NodeID, fw.ID, nil)))
	t.Logf("409 HARDWARE_UNKNOWN body: %s", w.Body.String())

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	env := decodeGateEnvelope(t, w.Body.Bytes())
	if env.Code != http.StatusConflict || env.ErrorCode != firmwareid.GateHardwareUnknown {
		t.Fatalf("envelope = %+v, want 409/%s", env, firmwareid.GateHardwareUnknown)
	}
	if len(env.Data.Reasons) != 1 || env.Data.Reasons[0] != firmwareid.GateHardwareUnknown {
		t.Errorf("data.reasons = %v, want [%s]", env.Data.Reasons, firmwareid.GateHardwareUnknown)
	}
	if env.Data.Forceable {
		t.Error("hardware gate must not be forceable")
	}
	if env.Data.NodeHW.FlashMB != 0 {
		t.Errorf("data.node_hw.flash_mb = %d, want 0", env.Data.NodeHW.FlashMB)
	}
	if !strings.Contains(env.Data.Fix, "ehomectl node hw set --id NODE-HW-UNKNOWN") {
		t.Errorf("data.fix = %q, want the node hw set command", env.Data.Fix)
	}
	if got := countGateTasks(t, db); got != 0 {
		t.Errorf("gate rejection must not create a task row, got %d", got)
	}
}

// TestOTACreateTask_NotAnUpgradeThenForce 是 409(NOT_AN_UPGRADE) + 400(缺理由) + 201(force) 三连：
// 版本方向唯一允许 force 的地方，且 force 必须写 security_audit_events。
func TestOTACreateTask_NotAnUpgradeThenForce(t *testing.T) {
	pub := &envelopeDownlinkStub{}
	r, db, _ := setupOTATestWithDownlink(t, pub)
	// 目标版本 == 节点当前版本 → 命中第 9 条（可 force）。
	node := seedGateHTTPNode(t, db, "NODE-SAME-VERSION", "2.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})

	// 1) 不带 force → 409，forceable=true
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks", createTaskBody(node.NodeID, fw.ID, nil)))
	t.Logf("409 NOT_AN_UPGRADE body: %s", w.Body.String())
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	env := decodeGateEnvelope(t, w.Body.Bytes())
	if env.ErrorCode != firmwareid.GateNotAnUpgrade {
		t.Errorf("error_code = %s, want %s", env.ErrorCode, firmwareid.GateNotAnUpgrade)
	}
	if !env.Data.Forceable {
		t.Error("NOT_AN_UPGRADE must be forceable")
	}
	if len(env.Data.Reasons) != 1 || env.Data.Reasons[0] != firmwareid.GateNotAnUpgrade {
		t.Errorf("data.reasons = %v, want [%s]", env.Data.Reasons, firmwareid.GateNotAnUpgrade)
	}

	// 2) force 但没理由 → 400 force_reason_required
	w = httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks",
		createTaskBody(node.NodeID, fw.ID, map[string]interface{}{"force": true})))
	t.Logf("400 force_reason_required body: %s", w.Body.String())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if env := decodeGateEnvelope(t, w.Body.Bytes()); env.ErrorCode != "force_reason_required" {
		t.Errorf("error_code = %s, want force_reason_required", env.ErrorCode)
	}

	// 3) force + 理由 → 201，任务行 forced/source 正确，审计有 actor/target/reason。
	const reason = "same-version reflash after field failure"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks",
		createTaskBody(node.NodeID, fw.ID, map[string]interface{}{"force": true, "force_reason": reason})))
	t.Logf("201 forced task body: %s", w.Body.String())
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var taskEnv struct {
		Data models.OTATask `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &taskEnv); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if !taskEnv.Data.Forced || taskEnv.Data.ForceReason != reason {
		t.Errorf("task forced=%v reason=%q", taskEnv.Data.Forced, taskEnv.Data.ForceReason)
	}
	if taskEnv.Data.Source != firmwareid.SourceUser {
		t.Errorf("task source = %q, want %q", taskEnv.Data.Source, firmwareid.SourceUser)
	}
	if taskEnv.Data.ToVersion != "2.0.0" {
		t.Errorf("task to_version = %q, want 2.0.0", taskEnv.Data.ToVersion)
	}
	if len(pub.payloads) != 1 {
		t.Errorf("OtaCmd publish count = %d, want 1", len(pub.payloads))
	}

	var event models.SecurityAuditEvent
	if err := db.Where("event_name = ?", "ota.task.force_created").First(&event).Error; err != nil {
		t.Fatalf("force audit row missing: %v", err)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("audit metadata: %v", err)
	}
	actorID := uint(0)
	if event.ActorUserID != nil {
		actorID = *event.ActorUserID
	}
	t.Logf("audit row: event=%s actor=%s/%d snapshot=%s target=%s/%s result=%s metadata=%s",
		event.EventName, event.ActorType, actorID, event.ActorSnapshot, event.TargetType, event.TargetID, event.Result, event.Metadata)
	if event.ActorType != "admin" || event.ActorUserID == nil || *event.ActorUserID != 1 {
		t.Errorf("audit actor = %s/%v, want admin/1", event.ActorType, event.ActorUserID)
	}
	if event.TargetType != "firmware" || event.TargetID != fmt.Sprint(fw.ID) {
		t.Errorf("audit target = %s/%s, want firmware/%d", event.TargetType, event.TargetID, fw.ID)
	}
	if metadata["reason"] != reason {
		t.Errorf("audit metadata.reason = %v, want %q", metadata["reason"], reason)
	}
	if metadata["gate_code"] != firmwareid.GateNotAnUpgrade {
		t.Errorf("audit metadata.gate_code = %v, want %s", metadata["gate_code"], firmwareid.GateNotAnUpgrade)
	}
}

// TestOTACreateTask_ForceDoesNotBypassHardware：硬件类闸门 force 仍 409（§2.4 R5）。
func TestOTACreateTask_ForceDoesNotBypassHardware(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})
	node := seedGateHTTPNode(t, db, "NODE-FORCE-HW", "1.0.0")
	// 芯片档位不匹配（固件 s3 刷 c6 节点）—— 属硬件类，不可 force。
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Update("chip", firmwareid.ChipS3).Error; err != nil {
		t.Fatalf("make firmware incompatible: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks",
		createTaskBody(node.NodeID, fw.ID, map[string]interface{}{"force": true, "force_reason": "please"})))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	env := decodeGateEnvelope(t, w.Body.Bytes())
	if env.ErrorCode != firmwareid.GateModelMismatch || env.Data.Forceable {
		t.Errorf("envelope = %+v, want MODEL_MISMATCH not forceable", env)
	}
	if got := countGateTasks(t, db); got != 0 {
		t.Errorf("force must not bypass hardware gate, found %d task row(s)", got)
	}
	var audits int64
	db.Model(&models.SecurityAuditEvent{}).Count(&audits)
	if audits != 0 {
		t.Errorf("rejected force must not write audit, got %d", audits)
	}
}

// TestOTARollback_GateErrorMaps409：手动回滚与建任务共用闸门映射（稳定版本是 legacy 时 409）。
func TestOTARollback_GateErrorMaps409(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	node := seedGateHTTPNode(t, db, "NODE-ROLLBACK", "2.0.0")
	stable := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "1.0.0", IdentityState: "legacy", Elf: "legacy-elf",
	})
	now := time.Now()
	if err := db.Create(&models.OTATask{
		OtaID: "ota-prev-success", NodeID: node.NodeID, FirmwareID: stable.ID,
		Status: ota.StatusSuccess, ToVersion: "1.0.0", CompletedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed previous success: %v", err)
	}

	labels := map[string]string{"reason": firmwareid.GateFirmwareUnverified}
	before := metricCounterValue(t, "ehome_ota_gate_rejected_total", labels)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, "/api/v1/ota/rollback/"+node.NodeID))
	t.Logf("rollback 409 body: %s", w.Body.String())
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if env := decodeGateEnvelope(t, w.Body.Bytes()); env.ErrorCode != firmwareid.GateFirmwareUnverified {
		t.Errorf("error_code = %s, want %s", env.ErrorCode, firmwareid.GateFirmwareUnverified)
	}
	// 指标埋在 ota 层而非 HTTP 层：回滚端点（source=system）的拒绝也要 +1。
	if got := metricCounterValue(t, "ehome_ota_gate_rejected_total", labels) - before; got != 1 {
		t.Errorf("ehome_ota_gate_rejected_total{reason=%q} increment = %v, want 1 (rollback path)", firmwareid.GateFirmwareUnverified, got)
	}
}

func gateRequestNoBody(t *testing.T, method, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", authHeader(t))
	return req
}

// ==================== B. 列表 ====================

// listFirmwareRow 是列表行的解码形状（compatible/reasons 仅带 node_id 时出现）。
type listFirmwareRow struct {
	ID            uint       `json:"id"`
	Version       string     `json:"version"`
	IdentityState string     `json:"identity_state"`
	WithdrawnAt   *time.Time `json:"withdrawn_at"`
	Compatible    *bool      `json:"compatible"`
	Reasons       []string   `json:"reasons"`
}

func listFirmwaresViaHTTP(t *testing.T, r http.Handler, query string) ([]listFirmwareRow, int) {
	t.Helper()
	path := "/api/v1/firmwares"
	if query != "" {
		path += "?" + query
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodGet, path))
	var env struct {
		Data []listFirmwareRow `json:"data"`
	}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode list: %v (%s)", err, w.Body.String())
		}
		t.Logf("list %q -> %s", path, w.Body.String())
	}
	return env.Data, w.Code
}

// TestFirmwareList_DefaultExcludesWithdrawnAndAnnotatesCompat 是 B 的主验收：
// 默认排除撤回、include_withdrawn 行数变化、带 node_id 时 verified 行 compatible=true、
// legacy 行 compatible=false 且 reasons 含 OTA_GATE_FIRMWARE_UNVERIFIED（AC-17）。
func TestFirmwareList_DefaultExcludesWithdrawnAndAnnotatesCompat(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	node := seedGateHTTPNode(t, db, "NODE-LIST", "0.9.0")
	verified := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0", Elf: "elf-verified"})
	legacy := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "1.5.0", IdentityState: "legacy", Elf: "elf-legacy"})
	withdrawn := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "1.0.0", Withdrawn: true, Elf: "elf-withdrawn"})

	// 默认：撤回行不可见（软删同样由 GORM 默认排除）。
	rows, code := listFirmwaresViaHTTP(t, r, "")
	if code != http.StatusOK {
		t.Fatalf("default list status = %d", code)
	}
	if len(rows) != 2 {
		t.Fatalf("default list rows = %d, want 2 (withdrawn excluded): %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.ID == withdrawn.ID {
			t.Errorf("withdrawn firmware %d must not appear by default", row.ID)
		}
		if row.Compatible != nil {
			t.Errorf("row %d: compatible must be absent without node_id", row.ID)
		}
	}

	// include_withdrawn=true：撤回行回来。
	rows, code = listFirmwaresViaHTTP(t, r, "include_withdrawn=true")
	if code != http.StatusOK || len(rows) != 3 {
		t.Fatalf("include_withdrawn rows = %d (code %d), want 3", len(rows), code)
	}

	// 带 node_id：逐行兼容判定（与建任务同源）。
	rows, code = listFirmwaresViaHTTP(t, r, "node_id="+node.NodeID)
	if code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("node_id list rows = %d (code %d), want 2", len(rows), code)
	}
	byID := map[uint]listFirmwareRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	verifiedRow, ok := byID[verified.ID]
	if !ok {
		t.Fatalf("verified row %d missing", verified.ID)
	}
	if verifiedRow.Compatible == nil || !*verifiedRow.Compatible {
		t.Errorf("verified row compatible = %v, want true", verifiedRow.Compatible)
	}
	if len(verifiedRow.Reasons) != 0 {
		t.Errorf("verified row reasons = %v, want none", verifiedRow.Reasons)
	}
	legacyRow, ok := byID[legacy.ID]
	if !ok {
		t.Fatalf("legacy row %d missing", legacy.ID)
	}
	if legacyRow.Compatible == nil || *legacyRow.Compatible {
		t.Errorf("legacy row compatible = %v, want false", legacyRow.Compatible)
	}
	if len(legacyRow.Reasons) != 1 || legacyRow.Reasons[0] != firmwareid.GateFirmwareUnverified {
		t.Errorf("legacy row reasons = %v, want [%s]", legacyRow.Reasons, firmwareid.GateFirmwareUnverified)
	}
}

// TestFirmwareList_FiltersAndValidation：chip/flash_mb/channel/q 过滤 + 非法参数 400 + 未知节点 404。
func TestFirmwareList_FiltersAndValidation(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0", Elf: "elf-c6-8"})
	seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.1.0", Chip: firmwareid.ChipS3, FlashMB: 16, Elf: "elf-s3-16"})
	seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "1.9.0", Channel: models.FirmwareChannelBeta, Elf: "elf-beta"})

	rows, code := listFirmwaresViaHTTP(t, r, "chip=esp32s3")
	if code != http.StatusOK || len(rows) != 1 || rows[0].Version != "2.1.0" {
		t.Errorf("chip filter: code=%d rows=%+v", code, rows)
	}
	rows, code = listFirmwaresViaHTTP(t, r, "flash_mb=16")
	if code != http.StatusOK || len(rows) != 1 || rows[0].Version != "2.1.0" {
		t.Errorf("flash_mb filter: code=%d rows=%+v", code, rows)
	}
	rows, code = listFirmwaresViaHTTP(t, r, "channel=beta")
	if code != http.StatusOK || len(rows) != 1 || rows[0].Version != "1.9.0" {
		t.Errorf("channel filter: code=%d rows=%+v", code, rows)
	}
	rows, code = listFirmwaresViaHTTP(t, r, "q="+url.QueryEscape("2.0.0"))
	if code != http.StatusOK || len(rows) != 1 || rows[0].Version != "2.0.0" {
		t.Errorf("q filter: code=%d rows=%+v", code, rows)
	}

	for _, query := range []string{"flash_mb=7", "channel=withdrawn", "chip=esp32", "include_withdrawn=maybe"} {
		if _, code := listFirmwaresViaHTTP(t, r, query); code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, code)
		}
	}
	if _, code := listFirmwaresViaHTTP(t, r, "node_id=NODE-NOT-EXIST"); code != http.StatusNotFound {
		t.Errorf("unknown node status = %d, want 404", code)
	}
}

// TestFirmwareList_OrderStableNewestFirst 是「分页顺序稳定」的验收用例。
// PG 的堆序不保证顺序（实测 2,1,3），前端分页会因此翻页抖动；
// 约定 created_at DESC, id DESC（新固件在前；同一时间戳用 id 确定性 tie-break）。
func TestFirmwareList_OrderStableNewestFirst(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	now := time.Now().UTC().Truncate(time.Millisecond)

	// 插入顺序故意打乱，并造一对 created_at 相同的行验证 id DESC tie-break。
	middle := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "v-middle", Elf: "elf-middle", CreatedAt: now.Add(-2 * time.Hour),
	})
	oldest := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "v-oldest", Elf: "elf-oldest", CreatedAt: now.Add(-3 * time.Hour),
	})
	tieA := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "v-tie-a", Elf: "elf-tie-a", CreatedAt: now.Add(-1 * time.Hour),
	})
	newest := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "v-newest", Elf: "elf-newest", CreatedAt: now.Add(-30 * time.Minute),
	})
	tieB := seedGateHTTPFirmware(t, db, gateFirmwareSeed{
		Version: "v-tie-b", Elf: "elf-tie-b", CreatedAt: now.Add(-1 * time.Hour),
	})

	first, code := listFirmwaresViaHTTP(t, r, "")
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	second, _ := listFirmwaresViaHTTP(t, r, "")

	ids := func(rows []listFirmwareRow) []uint {
		out := make([]uint, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}
	firstIDs := ids(first)
	secondIDs := ids(second)
	t.Logf("list order (two requests): first=%v second=%v", firstIDs, secondIDs)

	// 两次请求必须返回完全相同的顺序（分页稳定的前提）。
	if fmt.Sprint(firstIDs) != fmt.Sprint(secondIDs) {
		t.Errorf("list order is unstable across requests: first=%v second=%v", firstIDs, secondIDs)
	}
	// newest(-30m) → created_at=-1h 的 tie: id DESC（tieB 后插入 id 更大，排前）→ middle → oldest
	want := []uint{newest.ID, tieB.ID, tieA.ID, middle.ID, oldest.ID}
	if fmt.Sprint(firstIDs) != fmt.Sprint(want) {
		t.Errorf("list order = %v, want %v (created_at DESC, id DESC)", firstIDs, want)
	}
}

// ==================== C. 撤回 / 删除保护 ====================

func seedInFlightTask(t *testing.T, db *gorm.DB, nodeID string, firmwareID uint, status string) *models.OTATask {
	t.Helper()
	task := &models.OTATask{
		OtaID:  fmt.Sprintf("ota-inflight-%d-%s", firmwareID, status),
		NodeID: nodeID, FirmwareID: firmwareID, Status: status, ToVersion: "2.0.0",
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("seed in-flight task: %v", err)
	}
	return task
}

func markTaskTerminal(t *testing.T, db *gorm.DB, task *models.OTATask) {
	t.Helper()
	now := time.Now()
	if err := db.Model(&models.OTATask{}).Where("id = ?", task.ID).
		Updates(map[string]interface{}{"status": ota.StatusSuccess, "completed_at": &now}).Error; err != nil {
		t.Fatalf("finish task: %v", err)
	}
}

// TestFirmwareWithdraw_InFlight409ThenSuccessAudit：C 的撤回验收。
func TestFirmwareWithdraw_InFlight409ThenSuccessAudit(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	node := seedGateHTTPNode(t, db, "NODE-WD", "1.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0", Elf: "elf-wd"})
	task := seedInFlightTask(t, db, node.NodeID, fw.ID, ota.StatusDownloading)

	// 1) 在途任务 → 409 FIRMWARE_IN_USE，withdrawn_at 不变。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, fmt.Sprintf("/api/v1/firmwares/%d/withdraw", fw.ID)))
	t.Logf("withdraw 409 body: %s", w.Body.String())
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if env := decodeGateEnvelope(t, w.Body.Bytes()); env.ErrorCode != "FIRMWARE_IN_USE" {
		t.Errorf("error_code = %s, want FIRMWARE_IN_USE", env.ErrorCode)
	}
	var reloaded models.Firmware
	if err := db.First(&reloaded, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware: %v", err)
	}
	if reloaded.WithdrawnAt != nil {
		t.Error("rejected withdraw must not set withdrawn_at")
	}

	// 2) 任务结束后可撤回，写 withdrawn_at + 审计。
	markTaskTerminal(t, db, task)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, fmt.Sprintf("/api/v1/firmwares/%d/withdraw", fw.ID)))
	t.Logf("withdraw 200 body: %s", w.Body.String())
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := db.First(&reloaded, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware: %v", err)
	}
	if reloaded.WithdrawnAt == nil {
		t.Fatal("withdrawn_at must be set")
	}
	var auditEvent models.SecurityAuditEvent
	if err := db.Where("event_name = ?", "firmware.withdrawn").First(&auditEvent).Error; err != nil {
		t.Fatalf("withdraw audit missing: %v", err)
	}
	withdrawActorID := uint(0)
	if auditEvent.ActorUserID != nil {
		withdrawActorID = *auditEvent.ActorUserID
	}
	t.Logf("withdraw audit: actor=%s/%d snapshot=%s target=%s/%s", auditEvent.ActorType, withdrawActorID, auditEvent.ActorSnapshot, auditEvent.TargetType, auditEvent.TargetID)

	// 3) 默认列表不再出现；include_withdrawn 可见。
	rows, _ := listFirmwaresViaHTTP(t, r, "")
	if len(rows) != 0 {
		t.Errorf("default list must hide withdrawn firmware, got %+v", rows)
	}
	rows, _ = listFirmwaresViaHTTP(t, r, "include_withdrawn=true")
	if len(rows) != 1 {
		t.Errorf("include_withdrawn must show the row, got %+v", rows)
	}

	// 4) 幂等：重复撤回 200，不重复审计。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, fmt.Sprintf("/api/v1/firmwares/%d/withdraw", fw.ID)))
	if w.Code != http.StatusOK {
		t.Errorf("second withdraw status = %d, want 200 (idempotent)", w.Code)
	}
	var auditCount int64
	db.Model(&models.SecurityAuditEvent{}).Where("event_name = ?", "firmware.withdrawn").Count(&auditCount)
	if auditCount != 1 {
		t.Errorf("withdraw audit rows = %d, want 1", auditCount)
	}
}

// TestFirmwareDelete_InFlight409ThenSoftDeleteAudit：C 的删除验收。
func TestFirmwareDelete_InFlight409ThenSoftDeleteAudit(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	node := seedGateHTTPNode(t, db, "NODE-DEL", "1.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0", Elf: "elf-del"})
	task := seedInFlightTask(t, db, node.NodeID, fw.ID, ota.StatusInstalling)

	// 1) 在途任务 → 409，行仍在。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodDelete, fmt.Sprintf("/api/v1/firmwares/%d", fw.ID)))
	t.Logf("delete 409 body: %s", w.Body.String())
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if env := decodeGateEnvelope(t, w.Body.Bytes()); env.ErrorCode != "FIRMWARE_IN_USE" {
		t.Errorf("error_code = %s, want FIRMWARE_IN_USE", env.ErrorCode)
	}
	if err := db.First(&models.Firmware{}, fw.ID).Error; err != nil {
		t.Fatalf("firmware must still exist after rejected delete: %v", err)
	}

	// 2) 任务结束后删除成功：软删（Unscoped 可见 deleted_at）+ 审计。
	markTaskTerminal(t, db, task)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodDelete, fmt.Sprintf("/api/v1/firmwares/%d", fw.ID)))
	t.Logf("delete 200 body: %s", w.Body.String())
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := db.First(&models.Firmware{}, fw.ID).Error; err == nil {
		t.Error("soft-deleted firmware must be hidden from the default scope")
	}
	var unscoped models.Firmware
	if err := db.Unscoped().First(&unscoped, fw.ID).Error; err != nil {
		t.Fatalf("soft-deleted row must remain unscoped: %v", err)
	}
	if !unscoped.DeletedAt.Valid {
		t.Error("deleted_at must be set (soft delete, not hard delete)")
	}
	var auditEvent models.SecurityAuditEvent
	if err := db.Where("event_name = ?", "firmware.deleted").First(&auditEvent).Error; err != nil {
		t.Fatalf("delete audit missing: %v", err)
	}
	deleteActorID := uint(0)
	if auditEvent.ActorUserID != nil {
		deleteActorID = *auditEvent.ActorUserID
	}
	t.Logf("delete audit: actor=%s/%d snapshot=%s target=%s/%s", auditEvent.ActorType, deleteActorID, auditEvent.ActorSnapshot, auditEvent.TargetType, auditEvent.TargetID)

	// 3) 默认列表为空（软删不出现在列表里）。
	rows, _ := listFirmwaresViaHTTP(t, r, "")
	if len(rows) != 0 {
		t.Errorf("default list must hide deleted firmware, got %+v", rows)
	}
}

// TestFirmwareLifecycleActions_NotFound：撤回/删除不存在的固件 → 404。
func TestFirmwareLifecycleActions_NotFound(t *testing.T) {
	r, _, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	if w := httptest.NewRecorder(); true {
		r.ServeHTTP(w, gateRequestNoBody(t, http.MethodPost, "/api/v1/firmwares/99999/withdraw"))
		if w.Code != http.StatusNotFound {
			t.Errorf("withdraw unknown: status = %d, want 404", w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateRequestNoBody(t, http.MethodDelete, "/api/v1/firmwares/99999"))
	if w.Code != http.StatusNotFound {
		t.Errorf("delete unknown: status = %d, want 404", w.Code)
	}
}
