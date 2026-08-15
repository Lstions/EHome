package nodemgr

import (
	"sync/atomic"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// P1-4: Backpressure — overflow goroutine limit for when worker pool is full
var overflowGoroutines int64

const maxOverflowGoroutines = 50

// handleDataReport processes DataReport (type=0x03)
// Fast path: decode frame fields, then dispatch to worker pool
func (m *Manager) handleDataReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode DataReport: %v", deviceID, err)
		return
	}

	var channelID, timestamp, sequence uint64
	var rawData []byte
	var errorCode, requestID uint64
	var edgeDeviceID, commandIndex, commandTemplateID uint64

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			channelID = frame.GetUint64(field)
		case 2:
			timestamp = frame.GetUint64(field)
		case 3:
			sequence = frame.GetUint64(field)
		case 4:
			rawData = frame.GetBytes(field)
		case 5:
			errorCode = frame.GetUint64(field)
		case 6:
			requestID = frame.GetUint64(field)
		case 7:
			edgeDeviceID = frame.GetUint64(field)
		case 8:
			commandIndex = frame.GetUint64(field)
		case 9:
			commandTemplateID = frame.GetUint64(field)
		}
	}

	logger.Debugf("[%s] DataReport: ch=%d ts=%d seq=%d req=%d err=%d raw=%x",
		deviceID, channelID, timestamp, sequence, requestID, errorCode, rawData)

	// G10: Record data received metric
	status := "ok"
	if errorCode != 0 {
		status = "error"
	}
	metrics.DataReceivedTotal.WithLabelValues(deviceID, status).Inc()

	// Dispatch to worker pool (non-blocking)
	// P1-4: collectorID resolved in worker, not in MQTT callback
	job := dataReportJob{
		deviceID:          deviceID,
		channelID:         channelID,
		timestamp:         timestamp,
		sequence:          sequence,
		rawData:           rawData,
		errorCode:         errorCode,
		requestID:         requestID,
		edgeDeviceID:      edgeDeviceID,
		commandIndex:      commandIndex,
		commandTemplateID: commandTemplateID,
	}

	select {
	case m.dataCh <- job:
		// submitted to worker pool
	default:
		// P1-4: Backpressure — limit overflow goroutines via atomic CAS loop
		metrics.WorkerPoolOverflowTotal.Inc()
		for {
			current := atomic.LoadInt64(&overflowGoroutines)
			if current >= maxOverflowGoroutines {
				// Critical overload — block MQTT callback (better than data loss)
				metrics.WorkerPoolBackpressureBlockTotal.Inc()
				logger.Warnf("[%s] CRITICAL: overflow limit reached (%d), blocking MQTT callback", deviceID, current)
				m.processDataReportJob(job)
				break
			}
			if atomic.CompareAndSwapInt64(&overflowGoroutines, current, current+1) {
				go func() {
					defer atomic.AddInt64(&overflowGoroutines, -1)
					m.processDataReportJob(job)
				}()
				break
			}
			// CAS failed — another goroutine modified the counter, retry
		}
	}
}
