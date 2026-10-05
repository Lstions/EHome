package nodemgr

import (
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"errors"
	"time"
)

// handleConfigResult processes ConfigResult (type=0x05)
// v2.1: also updates config_sync_state
func (m *Manager) handleConfigResult(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ConfigResult: %v", deviceID, err)
		return
	}

	var manifestID string
	var success bool

	// v2.1 optional fields
	var configEpoch uint64
	var syncID string
	seen := map[uint8]bool{}

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			logger.Warnf("[%s] malformed ConfigResult: %v", deviceID, err)
			return
		}
		if field.FieldNum < 1 || field.FieldNum > 4 || seen[field.FieldNum] {
			logger.Warnf("[%s] invalid ConfigResult field %d", deviceID, field.FieldNum)
			return
		}
		seen[field.FieldNum] = true
		expectedWire := uint8(frame.WireVarint)
		if field.FieldNum == 1 || field.FieldNum == 4 {
			expectedWire = frame.WireLengthDelimited
		}
		if field.WireType != expectedWire {
			logger.Warnf("[%s] invalid ConfigResult wire type", deviceID)
			return
		}
		switch field.FieldNum {
		case 1:
			manifestID = frame.GetString(field)
		case 2:
			success = frame.GetBool(field)
		case 3: // v2.1: config_epoch
			configEpoch = frame.GetUint64(field)
		case 4: // v2.1: sync_id
			syncID = frame.GetString(field)
		}
	}
	if !seen[1] || !seen[2] || !seen[4] || manifestID == "" || syncID == "" {
		logger.Warnf("[%s] ConfigResult missing identity", deviceID)
		return
	}
	var current models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&current).Error; err != nil {
		return
	}
	// 只丢弃"确实属于更早世代"的回执。
	//
	// 旧实现额外要求 config_sync_state == "syncing"，这让 failed 变成一个**没有出口
	// 的终态**：一次失败把状态写成 failed 之后，同一代（同 manifest_id + 同 sync_id）
	// **迟到的成功回执**会被当成 stale 丢掉 —— 设备后来其实应用成功了，服务端却
	// 拒绝相信，failed 永久粘住（2026-10-05 S3 现场：45+ 分钟 cfg=failed，
	// 期间 0 sent / 0 rejected）。
	//
	// 世代判据（manifest_id + sync_id）本身已经足够精确：sync_id 是每次决策新生成的
	// UUID，跨代必然不同。因此这里放行 failed → applied/in_sync 的复位，
	// 而更早世代的回执仍被 manifest_id/sync_id 挡住。
	if current.ConfigVersion != manifestID || current.LastSyncID != syncID {
		logger.Warnf("[%s] ignoring stale ConfigResult manifest=%s sync_id=%s", deviceID, manifestID, syncID)
		return
	}
	// 状态机守卫：in_sync 已经是终态且世代相同，重复回执无需再写（幂等短路）。
	// failed / syncing / 其它中间态都允许按本回执的结果收敛。
	if current.ConfigSyncState == "in_sync" && success {
		logger.Infof("[%s] ConfigResult already in_sync for manifest=%s sync_id=%s (idempotent)", deviceID, manifestID, syncID)
		return
	}

	logger.Infof("[%s] ConfigResult: manifest=%s success=%v epoch=%d sync_id=%s",
		deviceID, manifestID, success, configEpoch, syncID)

	// Update node config version
	if success {
		now := time.Now()
		updates := map[string]interface{}{
			"config_version":    manifestID,
			"config_status":     "applied",
			"config_sync_state": "in_sync",
			"last_sync_at":      now,
		}
		if syncID != "" {
			updates["last_sync_id"] = syncID
		}
		// CAS 从 "= syncing" 放宽为 "IN (syncing, failed)"：世代判据由
		// manifest_id + sync_id 承担，状态判据只用来**阻止把已确认的 in_sync
		// 降级**。要求严格等于 syncing 会让 failed 永远无法复位（见上文）。
		result := m.db.Model(&models.Node{}).
			Where("node_id = ? AND config_version = ? AND last_sync_id = ? AND config_sync_state IN ?",
				deviceID, manifestID, syncID, []string{"syncing", "failed"}).
			Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			logger.Warnf("[%s] persist ConfigResult rejected: err=%v rows=%d", deviceID, result.Error, result.RowsAffected)
		}
	} else {
		result := m.db.Model(&models.Node{}).
			Where("node_id = ? AND config_version = ? AND last_sync_id = ? AND config_sync_state IN ?",
				deviceID, manifestID, syncID, []string{"syncing", "failed"}).
			Updates(map[string]interface{}{"config_status": "failed", "config_sync_state": "failed"})
		if result.Error != nil || result.RowsAffected != 1 {
			logger.Warnf("[%s] persist ConfigResult failure rejected: err=%v rows=%d", deviceID, result.Error, result.RowsAffected)
		}
	}
}

// handleConfigReport processes ConfigReport (type=0x11)
func (m *Manager) handleConfigReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ConfigReport: %v", deviceID, err)
		return
	}

	var requestID, manifestID string
	var templateCount, channelCount, uptimeSec uint64

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			requestID = frame.GetString(field)
		case 2:
			manifestID = frame.GetString(field)
		case 3:
			templateCount = frame.GetUint64(field)
		case 4:
			channelCount = frame.GetUint64(field)
		case 5:
			uptimeSec = frame.GetUint64(field)
		}
	}

	logger.Infof("[%s] ConfigReport: request=%s manifest=%s templates=%d channels=%d uptime=%d",
		deviceID, requestID, manifestID, templateCount, channelCount, uptimeSec)

	// Verify config sync: compare reported manifest with DB
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err == nil {
		if node.ConfigVersion != manifestID {
			logger.Infof("[%s] Config mismatch! DB=%s device=%s", deviceID, node.ConfigVersion, manifestID)
		}
	}
}
