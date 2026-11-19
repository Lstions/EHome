package ota

// ota_gate_test.go — M1a 闸门 / 系统回滚例外 / Hello 加固 / 设备回滚记账。
//
// 判定的**纯函数**在 internal/firmwareid/compat_test.go（UT-CP-01..10）里逐条覆盖；
// 本文件只证明 Manager 侧的四件事：
//  1. 不兼容**不写库**（AC-8），硬件类 force 仍是 409；
//  2. 版本类 force 建任务成功且 security_audit_events 有 actor/target/reason；
//  3. AutoRollback 以 source=system 建任务（AC-9），Hello 补完成加固（AC-10）；
//  4. 设备回滚识别 = failed(device rolled back) 且不计入 AutoRollback 失败计数（UT-OTA-03）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// newGateFixture 建一套「完全兼容」的节点 + 固件：c6 / 8MB / 无 PSRAM / 1.0.0 → 2.0.0。
func newGateFixture(t *testing.T, nodeID string) (*gorm.DB, *Manager, *models.Node, *models.Firmware) {
	t.Helper()
	db := setupOTATestDB(t)
	node := &models.Node{
		NodeID:          nodeID,
		Name:            "gate-node",
		Model:           "esp32c6",
		Platform:        firmwareid.ChipC6,
		FirmwareVersion: "1.0.0",
		HwFlashMB:       8,
		HwPsram:         firmwareid.PSRAMNo,
		Status:          "online",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	fw := &models.Firmware{
		Version:       "2.0.0",
		Checksum:      "checksum",
		URL:           "http://example/fw.bin",
		Filename:      "fw.bin",
		SizeBytes:     1024 * 1024,
		Chip:          firmwareid.ChipC6,
		FlashMB:       8,
		Psram:         firmwareid.PSRAMNo,
		ModelKey:      "esp32c6-n8",
		ElfSHA256:     strings.Repeat("a", 64),
		Channel:       models.FirmwareChannelStable,
		IdentityState: firmwareid.IdentityVerified,
	}
	if err := db.Create(fw).Error; err != nil {
		t.Fatalf("create firmware: %v", err)
	}
	return db, NewManager(db, nil, nil), node, fw
}

// recordingPublisher 记录下发而不真正建链；AutoRollback 的成功路径需要 Publish 返回 nil。
type recordingPublisher struct {
	mu      sync.Mutex
	nodeIDs []string
}

func (p *recordingPublisher) Publish(nodeID string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodeIDs = append(p.nodeIDs, nodeID)
	return nil
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.nodeIDs)
}

func countRows(t *testing.T, db *gorm.DB, model interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	return n
}

// ==================== 建任务闸门 ====================

// TestCreateTaskWithOptions_RejectsWithoutRow 是 AC-8 的「不兼容 → 409 且不产生任务行」。
// 这里断言的是 Manager 侧两条：typed GateError 可 errors.As；任务行与审计行都是 0。
func TestCreateTaskWithOptions_RejectsWithoutRow(t *testing.T) {
	db, mgr, _, fw := newGateFixture(t, "GATE-1")
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Update("chip", firmwareid.ChipS3).Error; err != nil {
		t.Fatalf("make firmware incompatible: %v", err)
	}

	_, err := mgr.CreateTaskWithOptions(CreateTaskOptions{NodeID: "GATE-1", FirmwareID: fw.ID})
	var gateErr *firmwareid.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *firmwareid.GateError, got %v", err)
	}
	if gateErr.Code != firmwareid.GateModelMismatch {
		t.Errorf("code = %s, want %s", gateErr.Code, firmwareid.GateModelMismatch)
	}
	if gateErr.Branch != firmwareid.BranchModelMismatch {
		t.Errorf("branch = %d, want %d", gateErr.Branch, firmwareid.BranchModelMismatch)
	}
	if gateErr.Forceable {
		t.Error("chip mismatch must not be forceable")
	}
	if got := gateErr.Data().Reasons; len(got) != 1 || got[0] != firmwareid.GateModelMismatch {
		t.Errorf("data.reasons = %v, want [%s]", got, firmwareid.GateModelMismatch)
	}
	if n := countRows(t, db, &models.OTATask{}); n != 0 {
		t.Errorf("gate rejection must not create a task row, got %d", n)
	}
	if n := countRows(t, db, &models.SecurityAuditEvent{}); n != 0 {
		t.Errorf("non-forced rejection must not write audit, got %d", n)
	}
}

// TestCreateTaskWithOptions_ForceDoesNotBypassHardware 守「force 仅 9/10」（方案 §2.4 R5）。
func TestCreateTaskWithOptions_ForceDoesNotBypassHardware(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "GATE-2")
	if err := db.Model(&models.Node{}).Where("id = ?", node.ID).Update("hw_flash_mb", 0).Error; err != nil {
		t.Fatalf("clear node flash profile: %v", err)
	}

	_, err := mgr.CreateTaskWithOptions(CreateTaskOptions{
		NodeID:      node.NodeID,
		FirmwareID:  fw.ID,
		Force:       true,
		ForceReason: "operator insists",
	})
	var gateErr *firmwareid.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *firmwareid.GateError, got %v", err)
	}
	if gateErr.Code != firmwareid.GateHardwareUnknown {
		t.Errorf("code = %s, want %s", gateErr.Code, firmwareid.GateHardwareUnknown)
	}
	if gateErr.Forceable {
		t.Error("hardware gate must never be forceable")
	}
	if n := countRows(t, db, &models.OTATask{}); n != 0 {
		t.Errorf("force must not bypass hardware gate, found %d task row(s)", n)
	}
	if n := countRows(t, db, &models.SecurityAuditEvent{}); n != 0 {
		t.Errorf("rejected force must not write audit, got %d", n)
	}
}

// TestCreateTaskWithOptions_ForceVersionDirectionWritesAudit 是 AC-8 的正例：
// 版本方向（同版本重刷）force 后任务落库，security_audit_events 有 actor/target/reason。
func TestCreateTaskWithOptions_ForceVersionDirectionWritesAudit(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "GATE-3")
	// 目标版本 = 当前版本 → 命中第 9 条（OTA_GATE_NOT_AN_UPGRADE，可 force）。
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Update("version", node.FirmwareVersion).Error; err != nil {
		t.Fatalf("make same-version firmware: %v", err)
	}

	uid := uint(7)
	const reason = "same-version reflash after field failure"
	task, err := mgr.CreateTaskWithOptions(CreateTaskOptions{
		NodeID:      node.NodeID,
		FirmwareID:  fw.ID,
		Force:       true,
		ForceReason: reason,
		Actor: AuditActor{
			Type:      "admin",
			UserID:    &uid,
			Snapshot:  "admin",
			RequestID: "req-1",
			SourceIP:  "10.0.0.1",
		},
	})
	if err != nil {
		t.Fatalf("forced create: %v", err)
	}
	if !task.Forced || task.ForceReason != reason {
		t.Errorf("task forced=%v reason=%q, want true/%q", task.Forced, task.ForceReason, reason)
	}
	if task.Source != firmwareid.SourceUser {
		t.Errorf("source = %q, want %q", task.Source, firmwareid.SourceUser)
	}
	if task.ToVersion != node.FirmwareVersion {
		t.Errorf("to_version = %q, want %q", task.ToVersion, node.FirmwareVersion)
	}

	var event models.SecurityAuditEvent
	if err := db.Where("event_name = ?", forceAuditEventName).First(&event).Error; err != nil {
		t.Fatalf("force audit row missing: %v", err)
	}
	if event.ActorType != "admin" || event.ActorUserID == nil || *event.ActorUserID != uid {
		t.Errorf("audit actor = %s/%v, want admin/%d", event.ActorType, event.ActorUserID, uid)
	}
	if event.ActorSnapshot != "admin" || event.RequestID != "req-1" || event.SourceIP != "10.0.0.1" {
		t.Errorf("audit actor context not persisted: %+v", event)
	}
	if event.TargetType != "firmware" || event.TargetID != fmt.Sprint(fw.ID) {
		t.Errorf("audit target = %s/%s, want firmware/%d", event.TargetType, event.TargetID, fw.ID)
	}
	if event.Result != "success" {
		t.Errorf("audit result = %q, want success", event.Result)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("audit metadata: %v", err)
	}
	if metadata["reason"] != reason {
		t.Errorf("audit reason = %v, want %q", metadata["reason"], reason)
	}
	if metadata["node_id"] != node.NodeID {
		t.Errorf("audit node_id = %v, want %q", metadata["node_id"], node.NodeID)
	}
	if metadata["gate_code"] != firmwareid.GateNotAnUpgrade {
		t.Errorf("audit gate_code = %v, want %s", metadata["gate_code"], firmwareid.GateNotAnUpgrade)
	}
	if n := countRows(t, db, &models.OTATask{}); n != 1 {
		t.Errorf("forced create must persist exactly one task, got %d", n)
	}
}

// TestCreateTaskWithOptions_ForceRequiresReason: force 不带理由 → ErrForceReasonRequired，
// 且不落任务、不落审计（API 层映射 400）。
func TestCreateTaskWithOptions_ForceRequiresReason(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "GATE-4")
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Update("version", node.FirmwareVersion).Error; err != nil {
		t.Fatalf("make same-version firmware: %v", err)
	}

	_, err := mgr.CreateTaskWithOptions(CreateTaskOptions{
		NodeID:      node.NodeID,
		FirmwareID:  fw.ID,
		Force:       true,
		ForceReason: "   ",
	})
	if !errors.Is(err, ErrForceReasonRequired) {
		t.Fatalf("expected ErrForceReasonRequired, got %v", err)
	}
	if n := countRows(t, db, &models.OTATask{}); n != 0 {
		t.Errorf("missing force_reason must not create a task, got %d", n)
	}
	if n := countRows(t, db, &models.SecurityAuditEvent{}); n != 0 {
		t.Errorf("missing force_reason must not write audit, got %d", n)
	}
}

// TestCreateTaskWithOptions_SystemSourceSkipsVersionDirection 是 AC-9 的 Manager 侧：
// 回滚目标（更旧版本 + 高于当前版本的 min_from）只能由 source=system 放行。
func TestCreateTaskWithOptions_SystemSourceSkipsVersionDirection(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "GATE-5")
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Updates(map[string]interface{}{
		"version":          "0.9.0",
		"min_from_version": "1.5.0",
	}).Error; err != nil {
		t.Fatalf("make rollback firmware: %v", err)
	}

	// 人工建这个任务必须被第 9 条拒绝。
	_, err := mgr.CreateTaskWithOptions(CreateTaskOptions{NodeID: node.NodeID, FirmwareID: fw.ID})
	var gateErr *firmwareid.GateError
	if !errors.As(err, &gateErr) || gateErr.Code != firmwareid.GateNotAnUpgrade {
		t.Fatalf("user source must be rejected with %s, got %v", firmwareid.GateNotAnUpgrade, err)
	}

	// 系统回滚通道放行，且任务行 source=system。
	task, err := mgr.CreateTaskWithOptions(CreateTaskOptions{
		NodeID:     node.NodeID,
		FirmwareID: fw.ID,
		Source:     firmwareid.SourceSystem,
	})
	if err != nil {
		t.Fatalf("system source must skip version direction: %v", err)
	}
	if task.Source != firmwareid.SourceSystem {
		t.Errorf("source = %q, want %q", task.Source, firmwareid.SourceSystem)
	}
	if task.Forced {
		t.Error("system rollback is not a force; forced must stay false")
	}
	if task.ToVersion != "0.9.0" {
		t.Errorf("to_version = %q, want 0.9.0", task.ToVersion)
	}
}

// TestAutoRollbackUsesSystemSource 是 AC-9 的端到端（Manager 侧）：
// 节点在 1.0.0、稳定版本记录是 0.9.0（更旧）→ AutoRollback 不被闸门掐死，
// 且新任务行 source=system。
func TestAutoRollbackUsesSystemSource(t *testing.T) {
	db := setupOTATestDB(t)
	now := time.Now()
	node := &models.Node{
		NodeID: "GATE-7", Platform: firmwareid.ChipC6, FirmwareVersion: "1.0.0",
		HwFlashMB: 8, HwPsram: firmwareid.PSRAMNo, Status: "online",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	stable := &models.Firmware{
		Version: "0.9.0", Checksum: "sum", URL: "http://example/old.bin", SizeBytes: 1024,
		Chip: firmwareid.ChipC6, FlashMB: 8, Psram: firmwareid.PSRAMNo, ModelKey: "esp32c6-n8",
		ElfSHA256: strings.Repeat("b", 64), IdentityState: firmwareid.IdentityVerified,
	}
	if err := db.Create(stable).Error; err != nil {
		t.Fatalf("create stable firmware: %v", err)
	}
	// 该节点最后一次成功升级到的版本（AutoRollback 的稳定版本来源）。
	if err := db.Create(&models.OTATask{
		OtaID: "ota-previous", NodeID: node.NodeID, FirmwareID: stable.ID,
		Status: StatusSuccess, ToVersion: "0.9.0", CompletedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed previous success: %v", err)
	}

	pub := &recordingPublisher{}
	mgr := NewManager(db, pub, nil)
	if err := mgr.AutoRollback(node.NodeID); err != nil {
		t.Fatalf("AutoRollback must not be blocked by the gate: %v", err)
	}

	var task models.OTATask
	if err := db.Where("node_id = ? AND status = ?", node.NodeID, StatusPending).First(&task).Error; err != nil {
		t.Fatalf("rollback task missing: %v", err)
	}
	if task.Source != firmwareid.SourceSystem {
		t.Errorf("source = %q, want %q (AC-9)", task.Source, firmwareid.SourceSystem)
	}
	if task.ToVersion != "0.9.0" {
		t.Errorf("to_version = %q, want 0.9.0", task.ToVersion)
	}
	if pub.count() != 1 {
		t.Errorf("rollback OtaCmd publish count = %d, want 1", pub.count())
	}
}

// gateCounterValue 读默认 registry 里某条计数器序列的当前值（不存在按 0）。
//
// 不用 prometheus/testutil：本仓既有测试统一用 client_model dto 快照读指标，
// 避免同一个断言出现两套读法。
func gateCounterValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			got := map[string]string{}
			for _, pair := range metric.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for key, value := range labels {
				if got[key] != value {
					match = false
					break
				}
			}
			if match {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// TestCreateTaskWithOptions_GateRejectionMetric 证明指标埋在 **ota 层**的拒绝处：
// 不经过 HTTP 直接调 Manager 也会自增，因此 HTTP 建任务与 AutoRollback/回滚端点
// 两条路径都被统计到（§9.5 按 reason 观测闸门）。
func TestCreateTaskWithOptions_GateRejectionMetric(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "GATE-METRIC-1")
	// 制造第 4 条（chip 不匹配）。
	if err := db.Model(&models.Firmware{}).Where("id = ?", fw.ID).Update("chip", firmwareid.ChipS3).Error; err != nil {
		t.Fatalf("make firmware incompatible: %v", err)
	}

	labels := map[string]string{"reason": firmwareid.GateModelMismatch}
	before := gateCounterValue(t, "ehome_ota_gate_rejected_total", labels)
	_, err := mgr.CreateTaskWithOptions(CreateTaskOptions{NodeID: node.NodeID, FirmwareID: fw.ID})
	var gateErr *firmwareid.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *firmwareid.GateError, got %v", err)
	}
	after := gateCounterValue(t, "ehome_ota_gate_rejected_total", labels)
	if got := after - before; got != 1 {
		t.Errorf("ehome_ota_gate_rejected_total{reason=%q} increment = %v, want 1", firmwareid.GateModelMismatch, got)
	}

	// 另一个 reason 是独立序列：这才让「按分支建看板」成立。
	otherLabels := map[string]string{"reason": firmwareid.GateHardwareUnknown}
	otherBefore := gateCounterValue(t, "ehome_ota_gate_rejected_total", otherLabels)
	if err := db.Model(&models.Node{}).Where("id = ?", node.ID).Update("hw_flash_mb", 0).Error; err != nil {
		t.Fatalf("clear node flash profile: %v", err)
	}
	if _, err := mgr.CreateTaskWithOptions(CreateTaskOptions{NodeID: node.NodeID, FirmwareID: fw.ID}); err == nil {
		t.Fatal("expected hardware-unknown rejection")
	}
	otherAfter := gateCounterValue(t, "ehome_ota_gate_rejected_total", otherLabels)
	if got := otherAfter - otherBefore; got != 1 {
		t.Errorf("ehome_ota_gate_rejected_total{reason=%q} increment = %v, want 1", firmwareid.GateHardwareUnknown, got)
	}
}

// ==================== Hello 补完成加固（AC-10） ====================

// createHelloTask 用闸门入口建一个兼容的 pending 任务。
func createHelloTask(t *testing.T, mgr *Manager, nodeID string, firmwareID uint) *models.OTATask {
	t.Helper()
	task, err := mgr.CreateTaskWithOptions(CreateTaskOptions{NodeID: nodeID, FirmwareID: firmwareID})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func setTaskStatus(t *testing.T, db *gorm.DB, taskID uint, status string) {
	t.Helper()
	if err := db.Model(&models.OTATask{}).Where("id = ?", taskID).Update("status", status).Error; err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}

// TestHandleHelloOTACompletion_PendingIsNotSuccess 是 UT-HC-01：
// 任务从未开始（pending）时，Hello 恰好报目标版本不能判成功。
func TestHandleHelloOTACompletion_PendingIsNotSuccess(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "HC-1")
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)

	mgr.HandleHelloOTACompletion(node.NodeID, "dev-hc1", fw.Version)

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusPending {
		t.Errorf("pending task must stay pending, got %s", got.Status)
	}
	if got.Progress != 0 || got.ErrorMsg != "" || got.CompletedAt != nil {
		t.Errorf("pending task must not be touched, got %+v", got)
	}
}

// TestHandleHelloOTACompletion_DownloadingIsSuccess 是 UT-HC-02：
// 已离开 pending + 版本相等 → 条件更新为 success，写 §6.2 规定的 error_msg。
func TestHandleHelloOTACompletion_DownloadingIsSuccess(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "HC-2")
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)
	setTaskStatus(t, db, task.ID, StatusDownloading)

	mgr.HandleHelloOTACompletion(node.NodeID, "dev-hc2", fw.Version)

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusSuccess {
		t.Errorf("status = %s, want %s", got.Status, StatusSuccess)
	}
	if got.Progress != 100 {
		t.Errorf("progress = %d, want 100", got.Progress)
	}
	if got.ErrorMsg != helloCompletionErrorMsg {
		t.Errorf("error_msg = %q, want %q", got.ErrorMsg, helloCompletionErrorMsg)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at must be set")
	}
}

// TestHandleHelloOTACompletion_InstallingIsSuccess: §6.2 的判据是「已离开 pending」，
// 不是「必须是 downloading」——设备写完镜像重启后 Hello 补完成时任务正是 installing。
func TestHandleHelloOTACompletion_InstallingIsSuccess(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "HC-2B")
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)
	setTaskStatus(t, db, task.ID, StatusInstalling)

	mgr.HandleHelloOTACompletion(node.NodeID, "dev-hc2b", fw.Version)

	if got := reloadTask(t, db, task.ID); got.Status != StatusSuccess {
		t.Errorf("status = %s, want %s", got.Status, StatusSuccess)
	}
}

// TestHandleHelloOTACompletion_ConditionalUpdateRejectsStaleState 是 UT-HC-03：
// 读与写之间状态被并发改写时，条件更新 RowsAffected=0 → 绝不覆盖。
func TestHandleHelloOTACompletion_ConditionalUpdateRejectsStaleState(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "HC-3")
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)
	setTaskStatus(t, db, task.ID, StatusDownloading)
	if err := db.Model(&models.OTATask{}).Where("id = ?", task.ID).Update("progress", 42).Error; err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	// 交错点：HandleHelloOTACompletion 读完 ota_tasks 之后、条件 UPDATE 之前，
	// 用独立会话把状态推进到 installing（模拟设备侧进度帧先到）。
	flipped := false
	cbName := "test:flip_to_installing_after_hello_read"
	_ = db.Callback().Query().After("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if flipped || tx.Statement.Table != "ota_tasks" {
			return
		}
		flipped = true
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&models.OTATask{}).
			Where("id = ?", task.ID).Update("status", StatusInstalling).Error; err != nil {
			t.Errorf("interleave: flip to installing: %v", err)
		}
	})
	defer func() { _ = db.Callback().Query().Remove(cbName) }()

	mgr.HandleHelloOTACompletion(node.NodeID, "dev-hc3", fw.Version)
	if !flipped {
		t.Fatal("interleave hook did not fire: HandleHelloOTACompletion never read ota_tasks")
	}

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusInstalling {
		t.Errorf("conditional update must not overwrite concurrently-changed row: want %q, got %q",
			StatusInstalling, got.Status)
	}
	if got.Progress != 42 {
		t.Errorf("progress must stay 42, got %d", got.Progress)
	}
	if got.CompletedAt != nil || got.ErrorMsg != "" {
		t.Errorf("stale conditional update must not write terminal fields: %+v", got)
	}
}

// ==================== 设备回滚识别（UT-OTA-03 / §6.6） ====================

// seedPreviousSuccess 造「该节点此前成功升到 version」的历史任务（id 一定小于后续任务）。
func seedPreviousSuccess(t *testing.T, db *gorm.DB, nodeID, version string) *models.Firmware {
	t.Helper()
	now := time.Now()
	fw := &models.Firmware{
		Version: version, Checksum: "sum", URL: "http://example/prev.bin", SizeBytes: 1024,
		Chip: firmwareid.ChipC6, FlashMB: 8, Psram: firmwareid.PSRAMNo, ModelKey: "esp32c6-n8",
		ElfSHA256: strings.Repeat("c", 64), IdentityState: firmwareid.IdentityVerified,
	}
	if err := db.Create(fw).Error; err != nil {
		t.Fatalf("create previous firmware: %v", err)
	}
	if err := db.Create(&models.OTATask{
		OtaID: "ota-prev", NodeID: nodeID, FirmwareID: fw.ID,
		Status: StatusSuccess, ToVersion: version, CompletedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed previous success: %v", err)
	}
	return fw
}

// TestHandleHelloOTACompletion_DeviceRolledBack 是 UT-OTA-03：
// 写入阶段后 Hello 报旧版本（== 上一个成功版本）→ failed(device rolled back)，
// 且不再触发 AutoRollback（回滚的回滚）。
func TestHandleHelloOTACompletion_DeviceRolledBack(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "RB-1")
	seedPreviousSuccess(t, db, node.NodeID, node.FirmwareVersion) // 1.0.0
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)
	setTaskStatus(t, db, task.ID, StatusInstalling)

	// 设备重启后 Hello 报回旧版本（1.0.0）：ToVersion=2.0.0 ≠ 1.0.0。
	mgr.HandleHelloOTACompletion(node.NodeID, "dev-rb1", node.FirmwareVersion)

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("status = %s, want %s", got.Status, StatusFailed)
	}
	if got.ErrorMsg != errorMsgDeviceRolledBack {
		t.Errorf("error_msg = %q, want %q", got.ErrorMsg, errorMsgDeviceRolledBack)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at must be set")
	}
	if n := countRows(t, db, &models.OTATask{}); n != 2 {
		t.Errorf("device rollback must not create a rollback task; want 2 rows (prev+current), got %d", n)
	}
}

// TestHandleHelloOTACompletion_DownloadingOldVersionIsNotRollback:
// 下载阶段的 Hello 报旧版本是**正常现象**（设备当然还跑着旧版本），不能判回滚。
// 否则 sync_manager 每 600s 一次的周期 Hello 会把每次正常升级误杀。
func TestHandleHelloOTACompletion_DownloadingOldVersionIsNotRollback(t *testing.T) {
	db, mgr, node, fw := newGateFixture(t, "RB-2")
	seedPreviousSuccess(t, db, node.NodeID, node.FirmwareVersion)
	task := createHelloTask(t, mgr, node.NodeID, fw.ID)
	setTaskStatus(t, db, task.ID, StatusDownloading)

	mgr.HandleHelloOTACompletion(node.NodeID, "dev-rb2", node.FirmwareVersion)

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusDownloading {
		t.Errorf("downloading task must not be treated as rolled back, got %s", got.Status)
	}
	if got.ErrorMsg != "" {
		t.Errorf("error_msg must stay empty, got %q", got.ErrorMsg)
	}
}

// TestCountRecentFailuresExcludesDeviceRollback: 「回滚的回执」不计入失败计数，
// 否则三次回滚就能凑满 AutoRollback 阈值，形成「回滚的回滚」（RK-12）。
func TestCountRecentFailuresExcludesDeviceRollback(t *testing.T) {
	db := setupOTATestDB(t)
	now := time.Now()
	for i := 0; i < 2; i++ {
		if err := db.Create(&models.OTATask{
			OtaID: fmt.Sprintf("ota-fail-%d", i), NodeID: "RB-3", FirmwareID: 1,
			Status: StatusFailed, ErrorMsg: "real failure", CompletedAt: &now,
		}).Error; err != nil {
			t.Fatalf("seed failure: %v", err)
		}
	}
	if err := db.Create(&models.OTATask{
		OtaID: "ota-rolled-back", NodeID: "RB-3", FirmwareID: 1,
		Status: StatusFailed, ErrorMsg: errorMsgDeviceRolledBack, CompletedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed rollback receipt: %v", err)
	}

	mgr := NewManager(db, nil, nil)
	count, err := mgr.countRecentFailures("RB-3")
	if err != nil {
		t.Fatalf("countRecentFailures: %v", err)
	}
	if count != 2 {
		t.Errorf("fail count = %d, want 2 (device rolled back must be excluded)", count)
	}
}
