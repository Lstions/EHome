package ota

import (
	"fmt"
	"ehome/backend/pkg/logger"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"

	"gorm.io/gorm"
)

// Manager handles OTA operations
type Manager struct {
	db     *gorm.DB
	mqtt   *mqtt.Client
	wsHub  *websocket.Hub
}

// NewManager creates a new OTA manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub) *Manager {
	return &Manager{
		db:    db,
		mqtt:  mqttClient,
		wsHub: wsHub,
	}
}

// CreateTask creates a new OTA task
func (m *Manager) CreateTask(collectorID uint, firmwareID uint) (*models.OTATask, error) {
	var firmware models.Firmware
	if err := m.db.First(&firmware, firmwareID).Error; err != nil {
		return nil, fmt.Errorf("firmware not found: %w", err)
	}

	task := &models.OTATask{
		OTaID:       fmt.Sprintf("ota-%d", time.Now().Unix()),
		CollectorID: collectorID,
		FirmwareID:  firmwareID,
		Status:      "pending",
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
	enc.EncodeString(1, task.OTaID)
	enc.EncodeString(2, firmware.URL)
	enc.EncodeString(3, firmware.Checksum)
	enc.EncodeVarint(4, firmware.SizeBytes)
	enc.EncodeString(5, firmware.Version)

	topic := mqtt.TopicForDevice(collector.DeviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// HandleOtaProgress processes OtaProgress messages from devices
func (m *Manager) HandleOtaProgress(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return
	}

	var taskID string
	var status, progressPct uint64

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
		}
	}

	// Update task in DB
	var task models.OTATask
	if err := m.db.Where("ota_id = ?", taskID).First(&task).Error; err != nil {
		logger.Infof("OTA task not found: %s", taskID)
		return
	}

	task.Progress = uint8(progressPct)
	switch status {
	case 0:
		task.Status = "downloading"
	case 1:
		task.Status = "completed"
	case 2:
		task.Status = "failed"
	}
	m.db.Save(&task)

	// WebSocket push
	if m.wsHub != nil {
		m.wsHub.BroadcastEvent("ota_progress", map[string]interface{}{
			"ota_id":      taskID,
			"status":      task.Status,
			"progress":    progressPct,
			"device_id":   deviceID,
		})
	}

	logger.Infof("[%s] OTA progress: %s = %d%%", deviceID, taskID, progressPct)
}
