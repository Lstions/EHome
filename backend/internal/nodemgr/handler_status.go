package nodemgr

import (
	"ehome/backend/internal/events"
	"ehome/backend/pkg/logger"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
)

// runtimePerformanceReport is an aggregate-only projection of the bounded
// firmware counters sent in StatusReport field 9.  Stack values are FreeRTOS
// words; a zero means the task was not running when the snapshot was made.
type runtimePerformanceReport struct {
	FreeHeapBytes          uint32    `json:"free_heap_bytes"`
	MinFreeHeapBytes       uint32    `json:"min_free_heap_bytes"`
	SchedulerStackFreeWord uint32    `json:"scheduler_stack_free_words"`
	WorkerStackFreeWord    uint32    `json:"worker_stack_free_words"`
	MinCommandQueueSpaces  uint32    `json:"min_command_queue_spaces"`
	ReportDrops            uint32    `json:"report_drops"`
	ReportQueueHighWater   uint32    `json:"report_queue_high_water"`
	QueueCurrentSpaces     [5]uint32 `json:"queue_current_spaces"`
	QueueHighWaterUsed     [5]uint32 `json:"queue_high_water_used"`
	QueueSampleSkipped     [5]uint32 `json:"queue_sample_skipped"`
	QueueSampleRejected    [5]uint32 `json:"queue_sample_rejected"`
}

// controlStatisticsReport is a bounded boot-local aggregate from the V2
// firmware path.  It has no identifiers or payloads and exists solely for
// rollout, replay and queue-health observability.
type controlStatisticsReport struct {
	Accepted  uint32 `json:"accepted"`
	Rejected  uint32 `json:"rejected"`
	Completed uint32 `json:"completed"`
	Replayed  uint32 `json:"replayed"`
}

func decodeRuntimePerformance(data []byte) (runtimePerformanceReport, error) {
	var report runtimePerformanceReport
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return report, err
	}
	seen := [28]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return report, err
		}
		if field.FieldNum < 1 || field.FieldNum > 27 || seen[field.FieldNum] || field.WireType != frame.WireVarint {
			return report, fmt.Errorf("invalid runtime performance field")
		}
		value := frame.GetUint64(field)
		if value > math.MaxUint32 {
			return report, fmt.Errorf("runtime performance overflow")
		}
		seen[field.FieldNum] = true
		switch field.FieldNum {
		case 1:
			report.FreeHeapBytes = uint32(value)
		case 2:
			report.MinFreeHeapBytes = uint32(value)
		case 3:
			report.SchedulerStackFreeWord = uint32(value)
		case 4:
			report.WorkerStackFreeWord = uint32(value)
		case 5:
			report.MinCommandQueueSpaces = uint32(value)
		case 6:
			report.ReportDrops = uint32(value)
		case 7:
			report.ReportQueueHighWater = uint32(value)
		case 8, 9, 10, 11, 12:
			report.QueueCurrentSpaces[field.FieldNum-8] = uint32(value)
		case 13, 14, 15, 16, 17:
			report.QueueHighWaterUsed[field.FieldNum-13] = uint32(value)
		case 18, 19, 20, 21, 22:
			report.QueueSampleSkipped[field.FieldNum-18] = uint32(value)
		case 23, 24, 25, 26, 27:
			report.QueueSampleRejected[field.FieldNum-23] = uint32(value)
		}
	}
	for field := uint8(1); field <= 5; field++ {
		if !seen[field] {
			return report, fmt.Errorf("missing runtime performance field %d", field)
		}
	}
	return report, nil
}

func decodeControlStatistics(data []byte) (controlStatisticsReport, error) {
	var report controlStatisticsReport
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return report, err
	}
	seen := [5]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return report, err
		}
		if field.FieldNum < 1 || field.FieldNum > 4 || seen[field.FieldNum] || field.WireType != frame.WireVarint {
			return report, fmt.Errorf("invalid control statistics field")
		}
		value := frame.GetUint64(field)
		if value > math.MaxUint32 {
			return report, fmt.Errorf("control statistics overflow")
		}
		seen[field.FieldNum] = true
		switch field.FieldNum {
		case 1:
			report.Accepted = uint32(value)
		case 2:
			report.Rejected = uint32(value)
		case 3:
			report.Completed = uint32(value)
		case 4:
			report.Replayed = uint32(value)
		}
	}
	for field := uint8(1); field <= 4; field++ {
		if !seen[field] {
			return report, fmt.Errorf("missing control statistics field %d", field)
		}
	}
	return report, nil
}

// handleStatusReport processes StatusReport (type=0x02)
// v2.1: parses 5 fields (2 new: config_epoch, sync_state)
// and routes through SyncGate for config re-sync on offline→online (fixes G5).
func (m *Manager) handleStatusReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode StatusReport: %v", deviceID, err)
		return
	}

	var uptimeSec uint64
	var status string
	var channelCount uint64

	// v2.1 new fields
	var configEpoch uint64
	var syncStateVarint uint64
	var configHash string // v2.2: config_hash from device
	var syncID string
	var performance *runtimePerformanceReport
	var controlStatistics *controlStatisticsReport
	seen := map[uint8]bool{}

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			logger.Warnf("[%s] malformed StatusReport: %v", deviceID, err)
			return
		}
		if field.FieldNum < 1 || field.FieldNum > 10 || (seen[field.FieldNum] && field.FieldNum != 7) {
			logger.Warnf("[%s] invalid StatusReport field", deviceID)
			return
		}
		seen[field.FieldNum] = true
		expected := uint8(frame.WireVarint)
		if field.FieldNum == 2 || field.FieldNum == 6 || field.FieldNum == 7 || field.FieldNum == 8 || field.FieldNum == 9 || field.FieldNum == 10 {
			expected = frame.WireLengthDelimited
		}
		if field.WireType != expected {
			logger.Warnf("[%s] invalid StatusReport wire type", deviceID)
			return
		}
		switch field.FieldNum {
		case 1:
			uptimeSec = frame.GetUint64(field)
		case 2:
			status = frame.GetString(field)
		case 3:
			channelCount = frame.GetUint64(field)
		case 4: // v2.1: config_epoch
			configEpoch = frame.GetUint64(field)
		case 5: // v2.1: sync_state (varint enum from ESP32: 0=idle,1=syncing,2=error)
			syncStateVarint = frame.GetUint64(field)
		case 6: // v2.2: config_hash
			configHash = frame.GetString(field)
		case 7:
			if err := validateChannelHealth(frame.GetBytes(field)); err != nil {
				logger.Warnf("[%s] malformed channel health: %v", deviceID, err)
				return
			}
		case 8:
			syncID = frame.GetString(field)
		case 9:
			decoded, err := decodeRuntimePerformance(frame.GetBytes(field))
			if err != nil {
				logger.Warnf("[%s] malformed runtime performance: %v", deviceID, err)
				return
			}
			performance = &decoded
		case 10:
			decoded, err := decodeControlStatistics(frame.GetBytes(field))
			if err != nil {
				logger.Warnf("[%s] malformed control statistics: %v", deviceID, err)
				return
			}
			controlStatistics = &decoded
		}
	}
	if !seen[1] || !seen[2] || !seen[5] || status == "" {
		logger.Warnf("[%s] StatusReport missing required fields", deviceID)
		return
	}

	// Map ESP32 sync_state varint to config_sync_state string.
	// idle (0) = device finished applying config → mark in_sync (self-healing).
	// This is the primary recovery path when ConfigResult (0x05) is lost.
	var syncState string
	switch syncStateVarint {
	case 0:
		syncState = "idle"
	case 1:
		syncState = "syncing"
	case 2:
		syncState = "error"
	}

	// Update node status
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for status update", deviceID)
		return
	}

	// Populate node_id → node.ID cache for worker pool lookups
	nodeIDCache.Store(deviceID, nodeIDCacheEntry{nodeID: node.ID, writtenAt: time.Now()})

	oldStatus := node.Status
	now := time.Now()
	node.Status = status
	node.LastSeen = &now
	node.UptimeSeconds = uint32(uptimeSec)
	node.OnlineDuration = uint64(uptimeSec)

	// last_online_time = 本次上线的起始时刻，只在 offline→online 转换时设置。
	// 在线期间不覆盖，这样用户看到的是"什么时候上线的"而非"几秒前"。
	if status == "online" && oldStatus != "online" {
		node.LastOnlineTime = &now
	}

	// StatusReport never overwrites synchronization generations. It may only
	// self-heal a non-syncing node when the current manifest identity matches.
	recoveryAttempted := false
	if syncState == "idle" && node.ConfigSyncState == "syncing" {
		if configHash != "" && configHash == node.ConfigVersion && syncID != "" && syncID == node.LastSyncID {
			node.ConfigSyncState = "in_sync"
			recoveryAttempted = true
		}
	}
	node.ConfigEpoch = configEpoch

	logger.Infof("[%s] StatusReport: status=%s uptime=%d ch=%d epoch=%d sync_state=%d(%s) hash=%s",
		deviceID, status, uptimeSec, channelCount, configEpoch, syncStateVarint, syncState, configHash)

	updates := map[string]interface{}{
		"status": node.Status, "last_seen": node.LastSeen, "uptime_seconds": node.UptimeSeconds,
		"online_duration": node.OnlineDuration, "last_online_time": node.LastOnlineTime,
		"config_epoch": node.ConfigEpoch,
	}
	if performance != nil || controlStatistics != nil {
		if performance != nil {
			updates["free_heap_bytes"] = int(performance.FreeHeapBytes)
		}
		var hardwareInfo map[string]interface{}
		if json.Unmarshal([]byte(node.HardwareInfo), &hardwareInfo) != nil || hardwareInfo == nil {
			hardwareInfo = map[string]interface{}{}
		}
		if performance != nil {
			hardwareInfo["runtime_performance"] = performance
		}
		if controlStatistics != nil {
			hardwareInfo["control_statistics"] = controlStatistics
		}
		if encoded, err := json.Marshal(hardwareInfo); err != nil {
			logger.Warnf("[%s] encode runtime performance failed: %v", deviceID, err)
			return
		} else {
			updates["hardware_info"] = string(encoded)
		}
	}
	if err := m.db.Model(&models.Node{}).Where("id = ?", node.ID).Updates(updates).Error; err != nil {
		logger.Warnf("[%s] persist StatusReport failed: %v", deviceID, err)
		return
	}
	if recoveryAttempted {
		result := m.db.Model(&models.Node{}).
			Where("id = ? AND config_version = ? AND last_sync_id = ? AND config_sync_state = ?", node.ID, configHash, syncID, "syncing").
			Update("config_sync_state", "in_sync")
		if result.Error != nil || result.RowsAffected != 1 {
			logger.Warnf("[%s] status recovery rejected: err=%v rows=%d", deviceID, result.Error, result.RowsAffected)
			return
		}
	}

	// Record event on status change
	if oldStatus != status {
		m.db.Create(&models.NodeEvent{
			NodeID:    node.NodeID,
			EventType: "status_change",
			OldStatus: oldStatus,
			NewStatus: status,
		})
	}

	// === v2.1: SyncGate decision on StatusReport (fixes G5) ===
	rpt := &StatusReportMsg{
		UptimeSec:    uptimeSec,
		Status:       status,
		ChannelCount: channelCount,
		ConfigEpoch:  configEpoch,
		SyncState:    syncState,
		ConfigHash:   configHash,
	}

	decision := m.syncGate.OnStatusReport(deviceID, rpt)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] StatusReport push: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	}

	// offline→online detection
	if oldStatus == "offline" && status == "online" {
		m.triggerDeviceInit(node.NodeID, deviceID)
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id":        deviceID,
		"status":         status,
		"uptime_seconds": uptimeSec,
		"channel_count":  channelCount,
	})
}

func validateChannelHealth(data []byte) error {
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return err
	}
	seenChannelID := false
	edgeHealthCount := 0
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			if !seenChannelID || edgeHealthCount == 0 {
				return fmt.Errorf("channel health requires channel_id and edge health")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch field.FieldNum {
		case 1:
			if seenChannelID || field.WireType != frame.WireVarint {
				return fmt.Errorf("invalid channel_id")
			}
			seenChannelID = true
		case 2:
			if field.WireType != frame.WireLengthDelimited {
				return fmt.Errorf("invalid edge health wire type")
			}
			if err := validateEdgeDeviceHealth(frame.GetBytes(field)); err != nil {
				return fmt.Errorf("edge health %d: %w", edgeHealthCount, err)
			}
			edgeHealthCount++
		default:
			return fmt.Errorf("unexpected channel health field %d", field.FieldNum)
		}
	}
}

func validateEdgeDeviceHealth(data []byte) error {
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return err
	}
	seen := [5]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			for number := uint8(1); number <= 4; number++ {
				if !seen[number] {
					return fmt.Errorf("missing field %d", number)
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if field.FieldNum < 1 || field.FieldNum > 4 || seen[field.FieldNum] ||
			field.WireType != frame.WireVarint {
			return fmt.Errorf("invalid field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
		value := frame.GetUint64(field)
		switch field.FieldNum {
		case 1:
			if value == 0 {
				return fmt.Errorf("edge_device_id must be non-zero")
			}
		case 2:
			if value > 255 {
				return fmt.Errorf("command_index out of range")
			}
		case 3:
			if value == 0 || value > math.MaxUint32 {
				return fmt.Errorf("error_count out of range")
			}
		case 4:
			if value != 1 && value != 2 && value != 3 {
				return fmt.Errorf("invalid comm_status %d", value)
			}
		}
	}
}
