package nodemgr

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"gorm.io/gorm"
)

func (m *Manager) handleChannelCmdV2Response(deviceID string, payload []byte) {
	if m.commandExec == nil {
		logger.Warnf("[%s] ChannelCmdV2 response ignored: control domain is not configured", deviceID)
		return
	}
	response, err := frame.DecodeChannelCmdV2Response(payload)
	if err != nil {
		logger.Warnf("[%s] malformed ChannelCmdV2 response: %v", deviceID, err)
		return
	}
	commandID := formatCommandID(response.CommandID)
	var execution models.CommandExecution
	var attempt models.CommandAttempt
	db := m.commandExec.Database()
	if err := loadCommandResponseIdentity(db, commandID, response.Attempt, &execution, &attempt); err != nil || execution.NodeID != deviceID {
		logger.Warnf("[%s] ChannelCmdV2 response has unknown or mismatched command %s", deviceID, commandID)
		return
	}
	if attempt.BootID == "" || attempt.BootID != response.BootID || !matchesAttemptDigest(attempt.WireDigest, response.PayloadDigest) {
		logger.Warnf("[%s] ChannelCmdV2 identity mismatch command=%s attempt=%d", deviceID, commandID, response.Attempt)
		return
	}
	eventType := "accepted"
	if !response.Final && !response.Success {
		eventType = "final_failed"
	}
	verifiedResultJSON := ""
	if response.Final {
		if !response.Success {
			eventType = "final_failed"
		} else if result, err := m.commandExec.VerifyFinal(context.Background(), execution, response.RawResponse); err != nil {
			logger.Warnf("[%s] ChannelCmdV2 final verification failed command=%s: %v", deviceID, commandID, err)
			eventType = "final_failed"
		} else if err := m.applySN3001ControlSideEffect(execution); err != nil {
			logger.Warnf("[%s] ChannelCmdV2 control side effect failed command=%s: %v", deviceID, commandID, err)
			eventType = "final_failed"
		} else if encoded, err := json.Marshal(result); err != nil {
			logger.Errorf("[%s] ChannelCmdV2 result encoding failed command=%s: %v", deviceID, commandID, err)
			eventType = "final_failed"
		} else {
			verifiedResultJSON = string(encoded)
			eventType = "final_succeeded"
		}
	}
	eventID := fmt.Sprintf("v2:%s:%d:%s:%d:%s", commandID, response.Attempt, response.BootID, response.EventSequence, eventType)
	updated, applied, err := m.commandExec.RecordInbox(context.Background(), commandexec.InboxEvent{
		EventID: eventID, CommandID: commandID, EventType: eventType, AttemptNo: response.Attempt, BootID: response.BootID,
		Payload:            map[string]interface{}{"error_code": response.ErrorCode, "final": response.Final, "replayed": response.Replayed, "raw_response_len": len(response.RawResponse)},
		VerifiedResultJSON: verifiedResultJSON,
	})
	if err != nil {
		logger.Errorf("[%s] ChannelCmdV2 inbox persist failed command=%s: %v", deviceID, commandID, err)
		return
	}
	if applied && m.wsHub != nil {
		m.wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, updated)
	}
}

// applySN3001ControlSideEffect commits the physical UART settings only after
// the trusted driver verifier has accepted the real device response.  The
// channel event is consumed by SyncGate, which publishes a fresh manifest to
// the collector.  Keeping this here makes a baud-rate change part of the
// audited command lifecycle instead of allowing a UI/API write to silently
// desynchronise the UART configuration.
func (m *Manager) applySN3001ControlSideEffect(execution models.CommandExecution) error {
	if execution.DeviceType != "sn3001_rain" || execution.ActionID != "set_baud_rate" {
		return nil
	}
	var params struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(execution.ParamsJSON), &params); err != nil {
		return fmt.Errorf("decode baud parameters: %w", err)
	}
	target := map[string]uint32{"2400": 2400, "4800": 4800, "9600": 9600}[strings.TrimSpace(params.Value)]
	if target == 0 {
		return fmt.Errorf("unsupported SN-3001 baud rate %q", params.Value)
	}
	var changed bool
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var channel models.Channel
		if err := tx.Where("id = ? AND node_id = ?", execution.ChannelID, execution.NodeID).First(&channel).Error; err != nil {
			return err
		}
		text := strings.TrimSpace(channel.BusConfig)
		if strings.HasPrefix(text, "\\x") || strings.HasPrefix(text, "0x") {
			text = text[2:]
		}
		data, err := hex.DecodeString(text)
		if err != nil || len(data) < 6 {
			return fmt.Errorf("channel %d has malformed UART bus_config", channel.ID)
		}
		current := uint32(data[2])<<24 | uint32(data[3])<<16 | uint32(data[4])<<8 | uint32(data[5])
		if current == target {
			return nil
		}
		data[2] = byte(target >> 24)
		data[3] = byte(target >> 16)
		data[4] = byte(target >> 8)
		data[5] = byte(target)
		updated := strings.ToUpper(hex.EncodeToString(data))
		if err := tx.Model(&models.Channel{}).Where("id = ? AND node_id = ?", channel.ID, channel.NodeID).Update("bus_config", updated).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil || !changed {
		return err
	}
	if m.eventBus == nil {
		return fmt.Errorf("configuration event bus is unavailable")
	}
	return m.eventBus.Publish(ConfigChangeEvent{
		Type: CfgChangeChannel, Action: CfgActionUpdate, NodeID: execution.NodeID,
		EntityID: fmt.Sprint(execution.ChannelID), Actor: "control:set_baud_rate",
	})
}

func loadCommandResponseIdentity(db *gorm.DB, commandID string, attemptNo uint32, execution *models.CommandExecution, attempt *models.CommandAttempt) error {
	for retry := 0; retry < 10; retry++ {
		executionErr := db.WithContext(context.Background()).First(execution, "command_id = ?", commandID).Error
		attemptErr := db.WithContext(context.Background()).Where("command_id = ? AND attempt_no = ?", commandID, attemptNo).First(attempt).Error
		if executionErr == nil && attemptErr == nil {
			return nil
		}
		if (executionErr != nil && !errors.Is(executionErr, gorm.ErrRecordNotFound)) ||
			(attemptErr != nil && !errors.Is(attemptErr, gorm.ErrRecordNotFound)) {
			if executionErr != nil {
				return executionErr
			}
			return attemptErr
		}
		time.Sleep(20 * time.Millisecond)
	}
	return gorm.ErrRecordNotFound
}

func matchesAttemptDigest(wireDigest string, value [16]byte) bool {
	raw, err := hex.DecodeString(wireDigest)
	return err == nil && len(raw) == 32 && subtle.ConstantTimeCompare(raw[:16], value[:]) == 1
}

func formatCommandID(value [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
