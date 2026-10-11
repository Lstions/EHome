package nodemgr

import (
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"encoding/json"
	"errors"
	"fmt"
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
	// ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
	// 设备因 DMA 不可用而降级为 polled 的通道 id（field 5，repeated）。
	// 设备只在成功时编、未降级时不编 ⇒ 空切片 = 无降级。
	var dmaDegraded []uint64
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
		if field.FieldNum < 1 || field.FieldNum > 5 {
			logger.Warnf("[%s] invalid ConfigResult field %d", deviceID, field.FieldNum)
			return
		}
		// ⚠ field 5（dma_degraded 通道 id）是 **repeated varint**，
		// 不能像 1..4 那样用 seen[] 去重 —— 去重会把第 2 条及之后的降级通道丢掉。
		if field.FieldNum != 5 && seen[field.FieldNum] {
			logger.Warnf("[%s] duplicate ConfigResult field %d", deviceID, field.FieldNum)
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
		case 5:
			// ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
			// 设备因 DMA 不可用而降级为 polled 的通道 id。
			// 设备侧只在**成功**时编这个字段，且未降级时不编（缺失 = 无降级）。
			dmaDegraded = append(dmaDegraded, frame.GetUint64(field))
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
			// ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
			// 每次成功回执都**覆盖**这个字段（不是累加）——
			// 它描述"本次配置的降级情况"，不是历史累积。
			// 设备未降级时不编 field 5 ⇒ dmaDegraded 为空 ⇒ 写空数组，
			// 于是上一次的降级提示会被正确清掉（否则会永久粘住）。
			"config_warnings": buildConfigWarnings(dmaDegraded),
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

// buildConfigWarnings 把设备上报的"降级通道 id"转成用户可见的告警 JSON。
//
// ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
//
//	设备因 DMA 不可用把某条通道降级为中断/轮询时，把这件事变成**提示**。
//
// ⚠ 语义上**不是错误**：配置成功应用了（success=true），只是某条通道没用上
//
//	DMA。所以它进 config_warnings 而不是 config_status/LastError ——
//	后者会让用户以为配置失败了。
//
// ⚠ 为什么必须给"人话"message：用户看到的是前端展示，
//
//	光有 channel_id 用户不知道是什么意思。
//
// 返回 JSON 数组字符串（存进 nodes.config_warnings）。
// 空输入返回 "[]" —— **必须**返回空数组而不是省略：这样每次成功回执都会
// 覆盖旧值，上一次的降级提示不会永久粘住。
func buildConfigWarnings(dmaDegraded []uint64) string {
	type warning struct {
		Code      string `json:"code"`
		ChannelID uint64 `json:"channel_id,omitempty"`
		Message   string `json:"message"`
	}
	out := make([]warning, 0, len(dmaDegraded))
	for _, chID := range dmaDegraded {
		out = append(out, warning{
			Code:      "dma_degraded",
			ChannelID: chID,
			Message: fmt.Sprintf(
				"通道 %d 未能使用 DMA，已自动降级为中断/轮询模式（功能正常）。"+
					"S3/C6 的多个 UART 共用一个 DMA 接口，同一时刻只有 1 条能用 DMA。"+
					"若需要 DMA，请关闭其他 UART 通道的 DMA。", chID),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Marshal 对这几个字段不可能失败；真失败也不能让配置回执处理崩掉。
		logger.Warnf("buildConfigWarnings marshal failed: %v", err)
		return "[]"
	}
	return string(b)
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
