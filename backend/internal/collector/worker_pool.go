package collector

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// dataReportJob represents a parsed DataReport ready for async processing
type dataReportJob struct {
	deviceID   string
	channelID  uint64
	timestamp  uint64
	sequence   uint64
	rawData    []byte
	errorCode  uint64
	requestID  uint64
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
	m.termMgr.RecordRX(job.deviceID, uint(job.channelID), job.rawData)

	// request_id routing
	if job.requestID != 0 {
		m.pendingWrite.HandleDataReportAck(uint32(job.requestID), job.rawData)
		var device models.Device
		if err := m.db.Where("channel_id = ?", job.channelID).First(&device).Error; err == nil {
			if m.deviceInit.HasActiveInit(device.Type) {
				logger.Infof("[%s] DataReport ack for device init, type=%s", job.deviceID, device.Type)
			}
		}
	}

	// Get collector
	var collector models.Collector
	if err := m.db.Where("device_id = ?", job.deviceID).First(&collector).Error; err != nil {
		logger.Infof("[%s] Collector not found", job.deviceID)
		return
	}

	// Store raw data
	dataJSON, _ := json.Marshal(map[string]interface{}{
		"raw":        fmt.Sprintf("%x", job.rawData),
		"channel":    job.channelID,
		"sequence":   job.sequence,
		"error_code": job.errorCode,
		"request_id": job.requestID,
	})
	m.db.Create(&models.DeviceData{
		CollectorID: collector.ID,
		DataJSON:    string(dataJSON),
		Timestamp:   time.Now(),
	})

	// Parse and store unified data (includes HA state push)
	if job.errorCode == 0 && job.rawData != nil {
		m.parseAndStoreData(collector.ID, job.deviceID, job.channelID, job.rawData)
		metrics.DataReportsProcessed.Inc()
	} else {
		metrics.DataReportErrors.Inc()
	}

	// WebSocket push
	m.wsHub.BroadcastEvent("channel_data", map[string]interface{}{
		"device_id":  job.deviceID,
		"channel_id": job.channelID,
		"raw_hex":    fmt.Sprintf("%x", job.rawData),
		"timestamp":  time.Now().Unix(),
		"error_code": job.errorCode,
		"request_id": job.requestID,
	})
}
