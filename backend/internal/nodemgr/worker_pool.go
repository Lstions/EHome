package nodemgr

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// dataReportJob represents a parsed DataReport ready for async processing
type dataReportJob struct {
	deviceID     string
	collectorID  uint
	channelID    uint64
	timestamp    uint64
	sequence     uint64
	rawData      []byte
	errorCode    uint64
	requestID    uint64
	edgeDeviceID uint64
	commandIndex uint64
}

const (
	defaultWorkerCount = 4
	defaultJobBuffer   = 128
)

// startWorkerPool launches background goroutines to process DataReport jobs
func (m *Manager) startWorkerPool() {
	m.dataCh = make(chan dataReportJob, defaultJobBuffer)

	for i := 0; i < defaultWorkerCount; i++ {
		m.wg.Add(1)
		go m.dataWorker(i)
	}

	logger.Infof("Started %d data report workers", defaultWorkerCount)
}

// dataWorker processes jobs from the channel
func (m *Manager) dataWorker(id int) {
	defer m.wg.Done()

	for job := range m.dataCh {
		metrics.WorkerPoolQueueSize.Set(float64(len(m.dataCh)))
		m.processDataReportJob(job)
	}

	logger.Infof("Worker %d exiting", id)
}

// processDataReportJob handles the heavy DB/parse work off the MQTT callback
func (m *Manager) processDataReportJob(job dataReportJob) {
	// Record RX in terminal
	if m.termMgr != nil {
		m.termMgr.RecordRX(job.deviceID, uint(job.channelID), job.rawData)
	}

	// request_id routing
	if job.requestID != 0 && m.pendingWrite != nil {
		m.pendingWrite.HandleDataReportAck(uint32(job.requestID), job.rawData)
		var device models.EdgeDevice
		if job.edgeDeviceID > 0 {
			m.db.Where("id = ?", job.edgeDeviceID).First(&device)
		} else {
			m.db.Where("channel_id = ?", job.channelID).First(&device)
		}
		if m.deviceInit != nil && m.deviceInit.HasActiveInit(device.Type) {
			logger.Infof("[%s] DataReport ack for device init, type=%s", job.deviceID, device.Type)
		}
	}

	// Get node
	var node models.Node
	if err := m.db.Where("node_id = ?", job.deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found", job.deviceID)
		return
	}

	// Store raw data
	dataJSON, _ := json.Marshal(map[string]interface{}{
		"raw":            fmt.Sprintf("%x", job.rawData),
		"channel":        job.channelID,
		"sequence":       job.sequence,
		"error_code":     job.errorCode,
		"request_id":     job.requestID,
		"edge_device_id": job.edgeDeviceID,
		"command_index":  job.commandIndex,
	})
	m.db.Create(&models.DeviceData{
		NodeID: node.NodeID,
		DataJSON:    string(dataJSON),
		Timestamp:   time.Now(),
	})

	// WebSocket push: build channel_data event first
	channelDataEvent := map[string]interface{}{
		"device_id":      job.deviceID, // node device_id (string, for terminal correlation)
		"node_id":        job.deviceID, // v2.2 新增 (同一值)
		"channel_id":     job.channelID,
		"raw_hex":        fmt.Sprintf("%x", job.rawData),
		"timestamp":      time.Now().Unix(),
		"error_code":     job.errorCode,
		"request_id":     job.requestID,
		"edge_device_id": job.edgeDeviceID,
		"command_index":  job.commandIndex,
	}

	// Parse and store unified data (includes HA state push)
	if job.errorCode == 0 && job.rawData != nil {
		parsedData := m.parseAndStoreData(job.collectorID, job.deviceID, job.channelID, job.rawData)
		metrics.DataReportsProcessed.Inc()

		// Add parsed sensor data to channel_data event
		if parsedData != nil {
			channelDataEvent["data"] = parsedData
		}
	} else {
		metrics.DataReportErrors.Inc()
	}

	// Try to look up sensor device for richer payload
	var sensorDevice models.EdgeDevice
	if job.edgeDeviceID > 0 {
		// v2.3: new firmware provides edge_device_id directly
		if err := m.db.Where("id = ?", job.edgeDeviceID).First(&sensorDevice).Error; err == nil {
			channelDataEvent["sensor_device_id"] = sensorDevice.ID
			channelDataEvent["sensor_device_name"] = sensorDevice.Name
			channelDataEvent["sensor_type"] = sensorDevice.Type
		}
	} else {
		// Legacy: fall back to channel_id lookup (v2.2 compat)
		if err := m.db.Where("channel_id = ?", job.channelID).First(&sensorDevice).Error; err == nil {
			channelDataEvent["sensor_device_id"] = sensorDevice.ID
			channelDataEvent["sensor_device_name"] = sensorDevice.Name
			channelDataEvent["sensor_type"] = sensorDevice.Type
		}
	}
	m.wsHub.BroadcastEvent(events.ChannelData, channelDataEvent)
}
