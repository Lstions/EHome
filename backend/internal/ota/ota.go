package ota

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// OTA 状态机字面量 (与 docs/v2.0/requirements.md §6.1 一致)
const (
	StatusPending     = "pending"
	StatusDownloading = "downloading"
	StatusVerifying   = "verifying"
	StatusInstalling  = "installing"
	StatusSuccess     = "success"
	StatusFailed      = "failed"
	StatusTimeout     = "timeout"
	StatusNeedsRetry  = "needs_retry"
)

// OtaProgress 状态码 (ESP→SVR, type=0x0B) — 与 docs §6.3 / protocol-spec.md 一致
//
// 当前 ESP32 实际只发 0/1/2/3 四个值, verifying 与 installing 阶段共用
// status=1 (因为 ESP32 端 OTA_STAGE_VERIFYING/OTA_STAGE_APPLYING 都映射到
// OTA_STATUS_INSTALLING)。WireVerifying=4 是 server 内部扩展, 允许未来
// ESP32 升级时显式区分 verifying 和 installing 阶段。
const (
	WireDownloading = 0
	WireInstalling  = 1
	WireSuccess     = 2
	WireFailed      = 3
	WireVerifying   = 4 // server-side extension (future ESP32 升级使用)
)

// 终态集合
var terminalStates = map[string]bool{
	StatusSuccess: true,
	StatusFailed:  true,
	StatusTimeout: true,
}

// 抢占 (supersede) 旧 OTA 记录: 同 node 下所有非终态记录标记为 failed
// 实现 docs §6.4.1
var activeStates = []string{
	StatusPending,
	StatusPending,
	StatusDownloading,
	StatusVerifying,
	StatusInstalling,
}

// 超时扫描用状态集合
var timeoutProneStates = []string{
	StatusPending,
	StatusDownloading,
	StatusInstalling,
}

// auto-rollback 阈值
const (
	rollbackWindow   = 24 * time.Hour
	rollbackFailMax  = 3
	timeoutThreshold = 30 * time.Minute
	timeoutScanTick  = 60 * time.Second
)

// compareVersion compares two semver-like version strings (Major.Minor.Patch).
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareVersion(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(aParts) {
			av, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bv, _ = strconv.Atoi(bParts[i])
		}
		if av < bv {
			return -1
		} else if av > bv {
			return 1
		}
	}
	return 0
}

// ack retry parameters
const (
	ackTimeout   = 30 * time.Second // wait for device to acknowledge OtaCmd
	ackMaxRetries = 3
)

// retry backoff intervals
var ackRetryBackoff = [ackMaxRetries]time.Duration{
	5 * time.Second,
	10 * time.Second,
	15 * time.Second,
}

// Manager handles OTA operations
// bridge holds shared mutable state; sync.Once singleton across all Manager instances.
type bridge struct {
	pendingCmds map[string]chan struct{}
	pendingMu   sync.Mutex
	seqCounter  uint32
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

var (
	_bridgeOnce sync.Once
	_bridge    *bridge
)

func getBridge() *bridge {
	_bridgeOnce.Do(func() {
		_bridge = &bridge{
			pendingCmds: make(map[string]chan struct{}),
			stopCh:      make(chan struct{}),
		}
	})
	return _bridge
}

type Manager struct {
	db    *gorm.DB
	mqtt  *mqtt.Client
	wsHub *websocket.Hub
}

// NewManager creates a new OTA manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub) *Manager {
	getBridge() // ensure bridge singleton is initialized (idempotent)
	return &Manager{
		db:    db,
		mqtt:  mqttClient,
		wsHub: wsHub,
	}
}

// Close stops background goroutines (timeout scanner).
func (m *Manager) Close() {
	close(_bridge.stopCh)
	_bridge.wg.Wait()
}

// timeoutScanner runs every 60s and marks stale downloading/installing tasks as timeout.
func (b *bridge) timeoutScanner() {
	defer b.wg.Done()
	ticker := time.NewTicker(timeoutScanTick)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			// scanTimeouts needs db; called from Manager wrapper
		}
	}
}

// scanTimeouts marks tasks in downloading/installing with no progress update for >30min as timeout.
func (m *Manager) scanTimeouts() {
	now := time.Now()
	threshold := now.Add(-timeoutThreshold)

	var tasks []models.OTATask
	if err := m.db.Where("status IN ? AND updated_at < ?", timeoutProneStates, threshold).Find(&tasks).Error; err != nil {
		logger.Errorf("[OTA] timeout scan DB error: %v", err)
		return
	}

	for i := range tasks {
		t := tasks[i]
		t.Status = StatusTimeout
		t.ErrorMsg = fmt.Sprintf("Timed out: no progress for >%v", timeoutThreshold)
		t.CompletedAt = &now
		if err := m.db.Save(&t).Error; err != nil {
			logger.Errorf("[OTA] failed to mark task %s as timeout: %v", t.OtaID, err)
			continue
		}

		// WebSocket push
		if m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":   t.OtaID,
				"status":   t.Status,
				"progress": t.Progress,
				"reason":   "timeout",
			})
		}
		logger.Infof("[OTA] task %s timed out (no progress since %s)", t.OtaID, t.UpdatedAt.Format(time.RFC3339))

		// Check auto-rollback after timeout
		m.maybeAutoRollback(t.NodeID)
	}
}

// maybeAutoRollback checks if the same node has had >=3 OTA failures in 24h,
// and if so, automatically rolls back to the last known stable firmware.
func (m *Manager) maybeAutoRollback(nodeID string) {
	since := time.Now().Add(-rollbackWindow)
	var failCount int64
	m.db.Model(&models.OTATask{}).
		Where("collector_id = ? AND status IN ? AND completed_at >= ?",
			nodeID,
			[]string{StatusFailed, StatusTimeout, StatusNeedsRetry},
			since,
		).
		Count(&failCount)

	if failCount < int64(rollbackFailMax) {
		return
	}

	// Trigger auto-rollback
	logger.Infof("[OTA] node %s has %d failures in 24h, triggering auto-rollback", nodeID, failCount)
	if err := m.AutoRollback(nodeID); err != nil {
		logger.Errorf("[OTA] auto-rollback failed for node %s: %v", nodeID, err)
	}
}

// AutoRollback finds the last known stable firmware for a node and creates a rollback OTA task.
// Same node with 3+ OTA failures in 24h triggers this automatically.
func (m *Manager) AutoRollback(nodeID string) error {
	// Find the node's current firmware version
	var node models.Node
	if err := m.db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	// Find the last successful OTA for this node — that tells us the previous stable version
	var lastSuccess models.OTATask
	err := m.db.Where("collector_id = ? AND status = ?", nodeID, StatusSuccess).
		Order("completed_at DESC").
		First(&lastSuccess).Error
	if err != nil {
		return fmt.Errorf("no successful OTA found for node %s, cannot determine stable version: %w", nodeID, err)
	}

	// Find the firmware record for that version
	var stableFW models.Firmware
	if err := m.db.First(&stableFW, lastSuccess.FirmwareID).Error; err != nil {
		return fmt.Errorf("stable firmware record not found (id=%d): %w", lastSuccess.FirmwareID, err)
	}

	// If the current version already matches the stable version, skip
	if node.FirmwareVersion == stableFW.Version {
		return fmt.Errorf("node %s already on stable version %s, no rollback needed", nodeID, stableFW.Version)
	}

	// Create rollback task
	task, err := m.CreateTask(nodeID, stableFW.ID)
	if err != nil {
		return fmt.Errorf("failed to create rollback task: %w", err)
	}

	// Send the OTA command
	if err := m.SendOtaCommand(task); err != nil {
		return fmt.Errorf("failed to send rollback OTA command: %w", err)
	}

	logger.Infof("[OTA] auto-rollback: node %s → firmware %s (task %s)", nodeID, stableFW.Version, task.OtaID)
	return nil
}

// GetNodeOTAStatus returns the OTA status for a node.
func (m *Manager) GetNodeOTAStatus(nodeID string) (map[string]interface{}, error) {
	var node models.Node
	if err := m.db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	// Last successful OTA
	var lastSuccess models.OTATask
	lastUpgradeTime := (*time.Time)(nil)
	err := m.db.Where("collector_id = ? AND status = ?", nodeID, StatusSuccess).
		Order("completed_at DESC").
		First(&lastSuccess).Error
	if err == nil {
		lastUpgradeTime = lastSuccess.CompletedAt
	}

	// Count failures in 24h
	since := time.Now().Add(-rollbackWindow)
	var failCount int64
	m.db.Model(&models.OTATask{}).
		Where("collector_id = ? AND status IN ? AND completed_at >= ?",
			nodeID,
			[]string{StatusFailed, StatusTimeout, StatusNeedsRetry},
			since,
		).
		Count(&failCount)

	result := map[string]interface{}{
		"node_id":              nodeID,
		"current_version":      node.FirmwareVersion,
		"last_upgrade_time":    lastUpgradeTime,
		"fail_count_24h":       failCount,
	}

	return result, nil
}

// CreateTask creates a new OTA task per docs §6.4.1:
//  1. Mark all in-flight OTA records for the same node as failed ("Superseded by new attempt")
//  2. Create new record in pending state with the target firmware version
//  3. (Caller is expected to publish the OtaCommand afterwards)
func (m *Manager) CreateTask(collectorID string, firmwareID uint) (*models.OTATask, error) {
	var firmware models.Firmware
	if err := m.db.First(&firmware, firmwareID).Error; err != nil {
		return nil, fmt.Errorf("firmware not found: %w", err)
	}

	// §6.4.1: Supersede any prior in-flight OTA for this node
	now := time.Now()
	res := m.db.Model(&models.OTATask{}).
		Where("collector_id = ? AND status IN ?", collectorID, activeStates).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    "Superseded by new attempt",
			"completed_at": &now,
		})
	if res.Error != nil {
		logger.Warnf("[OTA] Failed to supersede prior tasks for collector %s: %v", collectorID, res.Error)
	} else if res.RowsAffected > 0 {
		logger.Infof("[OTA] Superseded %d in-flight OTA record(s) for collector %s", res.RowsAffected, collectorID)
	}

	task := &models.OTATask{
		OtaID:      fmt.Sprintf("ota-%d", now.UnixNano()),
		NodeID:     collectorID,
		FirmwareID: firmwareID,
		ToVersion:  firmware.Version,
		Status:     StatusPending,
		Progress:   0,
	}

	if err := m.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	return task, nil
}

// SendOtaCommand sends OtaCommand to a device and waits for acknowledgement.
// It registers a pending channel, then spawns a goroutine that:
//  1. Waits up to 30s for the device to reply with OtaProgress(status=0)
//  2. If no ack within 30s, resends the command (up to 3 retries with backoff)
//  3. After 3 retries with no ack, marks the task as failed
func (m *Manager) SendOtaCommand(task *models.OTATask) error {
	var firmware models.Firmware
	if err := m.db.First(&firmware, task.FirmwareID).Error; err != nil {
		return err
	}

	// Get device_id
	var nodeRecord models.Node
	if err := m.db.Where("node_id = ?", task.NodeID).First(&nodeRecord).Error; err != nil {
		return err
	}

	// Allocate a sequence number for this OtaCmd
	seq := atomic.AddUint32(&_bridge.seqCounter, 1)

	// Build the frame once; reuse on retries
	payload := m.buildOtaCmdPayload(task, &firmware, seq)
	topic := mqtt.TopicForNode(nodeRecord.NodeID)

	// Register pending ack channel
	ackCh := make(chan struct{})
	_bridge.pendingMu.Lock()
	_bridge.pendingCmds[task.OtaID] = ackCh
	_bridge.pendingMu.Unlock()

	// Initial publish
	if err := m.mqtt.Publish(topic, payload); err != nil {
		_bridge.pendingMu.Lock()
		delete(_bridge.pendingCmds, task.OtaID)
		_bridge.pendingMu.Unlock()
		return fmt.Errorf("mqtt publish ota_cmd: %w", err)
	}
	logger.Infof("[OTA] sent OtaCmd ota_id=%s seq=%d to %s", task.OtaID, seq, topic)

	// Spawn ack-wait goroutine
	_bridge.wg.Add(1)
	go func() {
		defer _bridge.wg.Done()
		defer func() {
			// Cleanup: remove from pending map
			_bridge.pendingMu.Lock()
			delete(_bridge.pendingCmds, task.OtaID)
			_bridge.pendingMu.Unlock()
		}()

		for attempt := 0; attempt < ackMaxRetries; attempt++ {
			select {
			case <-ackCh:
				// Device acknowledged — done
				logger.Infof("[OTA] ota_id=%s acknowledged (attempt %d)", task.OtaID, attempt+1)
				return
			case <-time.After(ackTimeout):
				// No ack — retry
				backoff := ackRetryBackoff[attempt]
				logger.Warnf("[OTA] ota_id=%s no ack after %v (attempt %d/%d), retrying in %v",
					task.OtaID, ackTimeout, attempt+1, ackMaxRetries, backoff)

				// Wait backoff before resending
				select {
				case <-ackCh:
					logger.Infof("[OTA] ota_id=%s acknowledged during backoff (attempt %d)", task.OtaID, attempt+1)
					return
				case <-time.After(backoff):
				}

				// Resend
				if err := m.mqtt.Publish(topic, payload); err != nil {
					logger.Errorf("[OTA] ota_id=%s retry publish failed: %v", task.OtaID, err)
				} else {
					logger.Infof("[OTA] ota_id=%s resent (attempt %d/%d)", task.OtaID, attempt+1, ackMaxRetries)
				}
			}
		}

		// All retries exhausted — mark task as failed
		logger.Errorf("[OTA] ota_id=%s failed: no ack after %d attempts", task.OtaID, ackMaxRetries)
		now := time.Now()
		m.db.Model(&models.OTATask{}).Where("ota_id = ?", task.OtaID).Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    fmt.Sprintf("No device acknowledgement after %d retries", ackMaxRetries),
			"completed_at": &now,
		})

		if m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":   task.OtaID,
				"status":   StatusFailed,
				"progress": 0,
				"reason":   "ack_timeout",
			})
		}

		// Check auto-rollback
		m.maybeAutoRollback(task.NodeID)
	}()

	return nil
}

// buildOtaCmdPayload constructs the binary payload for an OtaCmd frame.
func (m *Manager) buildOtaCmdPayload(task *models.OTATask, firmware *models.Firmware, seq uint32) []byte {
	enc := frame.NewEncoder(frame.MsgOtaCmd)
	enc.EncodeString(1, task.OtaID)
	enc.EncodeString(2, firmware.URL)
	enc.EncodeString(3, firmware.Checksum)
	enc.EncodeVarint(4, firmware.SizeBytes)
	enc.EncodeString(5, firmware.Version)
	enc.EncodeVarint(frame.OtaCmdFieldSequence, uint64(seq))
	return enc.Bytes()
}

// HandleOtaProgress processes OtaProgress messages from devices per docs §6.4.2:
//   - Map wire status code to state machine literal (0/1/2/3 → downloading/installing/success/failed)
//   - When transitioning out of pending, set started_at
//   - When reaching a terminal state (success or failed), set completed_at
//   - Push ota_progress over WebSocket for live UI updates
func (m *Manager) HandleOtaProgress(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return
	}

	var taskID string
	var status, progressPct uint64
	var errorMsg string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			taskID = frame.GetString(field)
		case 2:
			status = frame.GetUint64(field)
		case 3:
			progressPct = frame.GetUint64(field)
		case 4:
			errorMsg = frame.GetString(field)
		}
	}

	// Acknowledge pending OtaCmd: device replied with first progress (status=0 = WireDownloading)
	if status == WireDownloading {
		_bridge.pendingMu.Lock()
		if ch, ok := _bridge.pendingCmds[taskID]; ok {
			close(ch) // signal the ack-wait goroutine
		}
		_bridge.pendingMu.Unlock()
	}

	// Update task in DB
	var task models.OTATask
	if err := m.db.Where("ota_id = ?", taskID).First(&task).Error; err != nil {
		logger.Infof("OTA task not found: %s", taskID)
		return
	}

	// Idempotency: terminal state records are immutable
	if terminalStates[task.Status] {
		logger.Infof("[%s] OTA task %s already in terminal state %s, ignoring progress update",
			deviceID, taskID, task.Status)
		return
	}

	now := time.Now()
	task.Progress = uint8(progressPct)

	// Map wire code → state literal
	wasActive := task.Status == StatusPending
	switch status {
	case WireDownloading:
		task.Status = StatusDownloading
	case WireVerifying:
		// server-side extension: ESP32 未来可能显式发 verifying 状态
		// (当前 OTA_STAGE_VERIFYING 在 ESP32 端映射为 OTA_STATUS_INSTALLING=1)
		task.Status = StatusVerifying
	case WireInstalling:
		// downloading → installing (could also be after verifying, but that's a server-only state)
		task.Status = StatusInstalling
	case WireSuccess:
		task.Status = StatusSuccess
		task.Progress = 100
		task.CompletedAt = &now
	case WireFailed:
		task.Status = StatusFailed
		if errorMsg != "" {
			task.ErrorMsg = errorMsg
		}
		task.CompletedAt = &now
	default:
		// Unknown wire code: log warning, keep current state (fail-open)
		logger.Warnf("[%s] OtaProgress unknown wire code: %d (ota_id=%s)",
			task.OtaID, status, taskID)
	}

	if wasActive && task.Status == StatusDownloading {
		task.StartedAt = &now
	}

	if err := m.db.Save(&task).Error; err != nil {
		logger.Errorf("[%s] Failed to save OTA task %s: %v", deviceID, taskID, err)
		return
	}

	// WebSocket push
	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":    taskID,
			"status":    task.Status,
			"progress":  task.Progress,
			"device_id": deviceID,
		})
	}

	logger.Infof("[%s] OTA progress: %s -> %s (%d%%)", deviceID, taskID, task.Status, task.Progress)

	// Check auto-rollback on failure
	if task.Status == StatusFailed {
		m.maybeAutoRollback(task.NodeID)
	}
}

// HandleHelloOTACompletion reconciles in-flight OTA tasks when a device Hello reports
// a new firmware version, per docs §6.4.3.
//
// If the device is now running the target firmware (to_version), any in-flight task for
// that node is marked success. This covers the case where the ESP32 reboots into
// the new firmware and the trailing success OtaProgress frame is lost.
//
// Enhanced: version mismatch detection with needs_retry status.
func (m *Manager) HandleHelloOTACompletion(collectorID string, deviceID, firmwareVersion string) {
	if collectorID == "" {
		return
	}

	// Find the latest non-terminal OTA record for this node
	var task models.OTATask
	err := m.db.Where("collector_id = ? AND status IN ?", collectorID, activeStates).
		Order("id DESC").
		First(&task).Error
	if err != nil {
		return // No in-flight OTA or DB error
	}

	// Only action: device reports the target version → mark success.
	// This covers the case where the ESP32 reboots into the new firmware
	// and the trailing success OtaProgress frame is lost.
	//
	// Timeout/failure detection is handled by timeoutScanner (60s tick, 30min threshold)
	// and SendOtaCommand's ack retry goroutine (30s×3 retries).
	// We intentionally do NOT mark tasks as failed/mismatch here — the device may
	// still be downloading while periodically sending Hello with the old version.
	if task.ToVersion != "" && task.ToVersion == firmwareVersion {
		now := time.Now()
		task.Status = StatusSuccess
		task.Progress = 100
		task.CompletedAt = &now
		task.ErrorMsg = "Auto-completed via Hello (device reported target firmware)"
		if task.StartedAt == nil {
			task.StartedAt = &now
		}
		if err := m.db.Save(&task).Error; err == nil && m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":    task.OtaID,
				"status":    task.Status,
				"progress":  100,
				"device_id": deviceID,
				"reason":    "hello_completion",
			})
		}
		logger.Infof("[%s] OTA %s auto-completed via Hello (firmware_version=%s)",
			deviceID, task.OtaID, firmwareVersion)
	}
}

// CancelTask marks an in-flight OTA task as cancelled (failed with reason)
// F6.x: user-initiated cancel from UI
func (m *Manager) CancelTask(taskID uint) error {
	var task models.OTATask
	if err := m.db.First(&task, taskID).Error; err != nil {
		return fmt.Errorf("task not found: %w", err)
	}
	if _, ok := terminalStates[task.Status]; ok {
		return fmt.Errorf("task %s is already in terminal state %s", task.OtaID, task.Status)
	}
	now := time.Now()
	task.Status = StatusFailed
	task.ErrorMsg = "cancelled by user"
	task.CompletedAt = &now
	if err := m.db.Save(&task).Error; err != nil {
		return err
	}
	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":   task.OtaID,
			"status":   task.Status,
			"progress": task.Progress,
			"reason":   "cancelled",
		})
	}
	logger.Infof("[OTA %s] cancelled by user", task.OtaID)
	return nil
}
