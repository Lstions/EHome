package nodemgr

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
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
	if err := db.WithContext(context.Background()).First(&execution, "command_id = ?", commandID).Error; err != nil || execution.NodeID != deviceID {
		logger.Warnf("[%s] ChannelCmdV2 response has unknown or mismatched command %s", deviceID, commandID)
		return
	}
	if err := db.WithContext(context.Background()).Where("command_id = ? AND attempt_no = ?", commandID, response.Attempt).First(&attempt).Error; err != nil {
		logger.Warnf("[%s] ChannelCmdV2 response has unknown attempt command=%s attempt=%d", deviceID, commandID, response.Attempt)
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

func matchesAttemptDigest(wireDigest string, value [16]byte) bool {
	raw, err := hex.DecodeString(wireDigest)
	return err == nil && len(raw) == 32 && subtle.ConstantTimeCompare(raw[:16], value[:]) == 1
}

func formatCommandID(value [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
