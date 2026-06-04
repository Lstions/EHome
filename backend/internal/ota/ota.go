package ota

import (
	"fmt"
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
}

// 抢占 (supersede) 旧 OTA 记录: 同一 collector 下所有非终态记录标记为 failed
// 实现 docs §6.4.1
var activeStates = []string{
	StatusPending,
	StatusDownloading,
	StatusVerifying,
	StatusInstalling,
}

// Manager handles OTA operations
type Manager struct {
	db    *gorm.DB
	mqtt  *mqtt.Client
	wsHub *websocket.Hub
}

// NewManager creates a new OTA manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub) *Manager {
	return &Manager{
		db:    db,
		mqtt:  mqttClient,
		wsHub: wsHub,
	}
}

// CreateTask creates a new OTA task per docs §6.4.1:
//  1. Mark all in-flight OTA records for the same collector as failed ("Superseded by new attempt")
//  2. Create new record in pending state with the target firmware version
//  3. (Caller is expected to publish the OtaCommand afterwards)
func (m *Manager) CreateTask(collectorID uint, firmwareID uint) (*models.OTATask, error) {
	var firmware models.Firmware
	if err := m.db.First(&firmware, firmwareID).Error; err != nil {
		return nil, fmt.Errorf("firmware not found: %w", err)
	}

	// §6.4.1: Supersede any prior in-flight OTA for this collector
	now := time.Now()
	res := m.db.Model(&models.OTATask{}).
		Where("collector_id = ? AND status IN ?", collectorID, activeStates).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    "Superseded by new attempt",
			"completed_at": &now,
		})
	if res.Error != nil {
		logger.Warnf("[OTA] Failed to supersede prior tasks for collector %d: %v", collectorID, res.Error)
	} else if res.RowsAffected > 0 {
		logger.Infof("[OTA] Superseded %d in-flight OTA record(s) for collector %d", res.RowsAffected, collectorID)
	}

	task := &models.OTATask{
		OtaID:       fmt.Sprintf("ota-%d", now.UnixNano()),
		CollectorID: collectorID,
		FirmwareID:  firmwareID,
		ToVersion:   firmware.Version,
		Status:      StatusPending,
		Progress:    0,
	}

	if err := m.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	return task, nil
}

// SendOtaCommand sends OtaCommand to a device
func (m *Manager) SendOtaCommand(task *models.OTATask) error {
	var firmware models.Firmware
	if err := m.db.First(&firmware, task.FirmwareID).Error; err != nil {
		return err
	}

	// Get device_id
	var collector models.Collector
	if err := m.db.First(&collector, task.CollectorID).Error; err != nil {
		return err
	}

	enc := frame.NewEncoder(frame.MsgOtaCmd)
	enc.EncodeString(1, task.OtaID)
	enc.EncodeString(2, firmware.URL)
	enc.EncodeString(3, firmware.Checksum)
	enc.EncodeVarint(4, firmware.SizeBytes)
	enc.EncodeString(5, firmware.Version)

	topic := mqtt.TopicForDevice(collector.NodeID)
	return m.mqtt.Publish(topic, enc.Bytes())
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
}

// HandleHelloOTACompletion reconciles in-flight OTA tasks when a device Hello reports
// a new firmware version, per docs §6.4.3.
//
// If the device is now running the target firmware (to_version), any in-flight task for
// that collector is marked success. This covers the case where the ESP32 reboots into
// the new firmware and the trailing success OtaProgress frame is lost.
func (m *Manager) HandleHelloOTACompletion(collectorID uint, deviceID, firmwareVersion string) {
	if collectorID == 0 {
		return
	}

	// Find the latest non-terminal OTA record for this collector
	var task models.OTATask
	err := m.db.Where("collector_id = ? AND status IN ?", collectorID, activeStates).
		Order("id DESC").
		First(&task).Error
	if err != nil {
		return // No in-flight OTA or DB error — nothing to do
	}

	now := time.Now()
	// Case A: device already reports the target version → mark success
	if task.ToVersion != "" && task.ToVersion == firmwareVersion {
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
		return
	}

	// Case B: stuck in flight for >10 minutes with mismatched version → mark failed
	if task.StartedAt != nil && now.Sub(*task.StartedAt) > 10*time.Minute {
		task.Status = StatusFailed
		task.ErrorMsg = fmt.Sprintf("Timeout: no progress for >10m, device reports %s (expected %s)",
			firmwareVersion, task.ToVersion)
		task.CompletedAt = &now
		if err := m.db.Save(&task).Error; err == nil && m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":    task.OtaID,
				"status":    task.Status,
				"progress":  task.Progress,
				"device_id": deviceID,
				"reason":    "hello_timeout",
			})
		}
		logger.Infof("[%s] OTA %s timed out (started_at=%s, current fw=%s)",
			deviceID, task.OtaID, task.StartedAt.Format(time.RFC3339), firmwareVersion)
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
