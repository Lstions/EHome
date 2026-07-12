package nodemgr

import (
	"sync"
	"time"

	"ehome/backend/internal/databus"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// dataReportJob represents a parsed DataReport ready for async processing
type dataReportJob struct {
	deviceID     string
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
	defaultWorkerCount = 8    // P1-5: from 4 → 8
	defaultJobBuffer   = 1024 // P1-5: from 128 → 1024
)

// nodeIDCache caches node_id (string) → node.ID (uint) mapping.
// Updated by handleHello/handleStatusReport, read by worker pool
// instead of repeating db.Where("node_id = ?", job.deviceID).First() per DataReport.
var nodeIDCache sync.Map

// InvalidateNodeIDCache removes a device_id from the node ID cache.
// Must be called when a node is deleted so stale lookups don't return the old primary key.
func InvalidateNodeIDCache(deviceID string) {
	nodeIDCache.Delete(deviceID)
}

// lookupCollectorID returns the collector (node) ID for a device_id.
// Cache-first: if miss, query DB with session isolation and populate cache.
func lookupCollectorID(db *gorm.DB, deviceID string) (uint, bool) {
	if v, ok := nodeIDCache.Load(deviceID); ok {
		return v.(uint), true
	}
	var node models.Node
	if err := db.Session(&gorm.Session{}).Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		return 0, false
	}
	nodeIDCache.Store(deviceID, node.ID)
	return node.ID, true
}

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

// processDataReportJob handles the heavy DB/parse work off the MQTT callback.
// v2.5: Refactored to parse frame and publish to DataEventBus.
// All downstream processing (terminal record, WS push, DB persist, sensor parse)
// is handled by independent consumers on the bus.
func (m *Manager) processDataReportJob(job dataReportJob) {
	if m.dataBus == nil {
		// Fallback for tests where dataBus is not initialized
		return
	}

	evt := databus.DataEvent{
		DeviceID:     job.deviceID,
		ChannelID:    job.channelID,
		Timestamp:    job.timestamp,
		Sequence:     job.sequence,
		RawData:      job.rawData,
		ErrorCode:    job.errorCode,
		RequestID:    job.requestID,
		EdgeDeviceID: job.edgeDeviceID,
		CommandIndex: job.commandIndex,
		ReceivedAt:   time.Now(),
	}

	// Publish to event bus — consumers handle everything
	m.dataBus.Publish(evt)

	metrics.WorkerPoolProcessDuration.Observe(time.Since(evt.ReceivedAt).Seconds())
}
