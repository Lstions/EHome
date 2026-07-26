package nodemgr

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		} else if encoded, err := json.Marshal(result); err != nil {
			logger.Errorf("[%s] ChannelCmdV2 result encoding failed command=%s: %v", deviceID, commandID, err)
			eventType = "final_failed"
		} else {
			verifiedResultJSON = string(encoded)
			eventType = "final_succeeded"
		}
	}
	eventID := fmt.Sprintf("v2:%s:%d:%s:%d:%s", commandID, response.Attempt, response.BootID, response.EventSequence, eventType)
	var configEvent ConfigChangeEvent
	var configChanged bool
	var finalizer commandexec.InboxFinalizer
	if eventType == "final_succeeded" {
		finalizer = func(tx *gorm.DB) error {
			var err error
			configEvent, configChanged, err = m.applySN3001ControlSideEffectTx(tx, execution)
			return err
		}
	}
	updated, applied, err := m.commandExec.RecordInboxWithFinalizer(context.Background(), commandexec.InboxEvent{
		EventID: eventID, CommandID: commandID, EventType: eventType, AttemptNo: response.Attempt, BootID: response.BootID,
		Payload:            map[string]interface{}{"error_code": response.ErrorCode, "final": response.Final, "replayed": response.Replayed, "raw_response_len": len(response.RawResponse)},
		VerifiedResultJSON: verifiedResultJSON,
	}, finalizer)
	if err != nil && finalizer != nil {
		// The finalizer is part of the transaction, so a failure rolls back the
		// inbox insert and leaves the execution dispatchable as failed evidence.
		// Record the terminal failure separately; this event has a distinct ID
		// from the successful attempt and remains idempotent on retransmission.
		logger.Warnf("[%s] ChannelCmdV2 control side effect rolled back command=%s: %v", deviceID, commandID, err)
		failureEventID := fmt.Sprintf("v2:%s:%d:%s:%d:final_failed", commandID, response.Attempt, response.BootID, response.EventSequence)
		updated, applied, err = m.commandExec.RecordInbox(context.Background(), commandexec.InboxEvent{
			EventID: failureEventID, CommandID: commandID, EventType: "final_failed", AttemptNo: response.Attempt, BootID: response.BootID,
			Payload: map[string]interface{}{"error_code": response.ErrorCode, "final": response.Final, "replayed": response.Replayed, "raw_response_len": len(response.RawResponse), "side_effect_error": err.Error()},
		})
	}
	if err != nil {
		logger.Errorf("[%s] ChannelCmdV2 inbox persist failed command=%s: %v", deviceID, commandID, err)
		return
	}
	if applied && configChanged && m.eventBus != nil {
		if err := m.eventBus.Publish(configEvent); err != nil {
			// The durable state, DB side effect and config-change outbox are
			// already committed. SyncGate will retry the outbox when the
			// in-memory bus is full or temporarily unavailable.
			logger.Warnf("[%s] ChannelCmdV2 config sync event enqueue failed command=%s: %v", deviceID, commandID, err)
		}
	}
	if applied && m.wsHub != nil {
		m.wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, updated)
	}
}

// applySN3001ControlSideEffect commits SN-3001 connection settings only after
// the trusted driver verifier has accepted the real device response. The
// resulting config event is consumed by SyncGate, which publishes a fresh
// manifest to the collector. Keeping this here makes address/baud changes part
// of the audited command lifecycle instead of allowing a UI/API write to
// silently desynchronise the UART configuration.
func (m *Manager) applySN3001ControlSideEffect(execution models.CommandExecution) error {
	if execution.DeviceType != "sn3001_rain" {
		return nil
	}
	if execution.ActionID != "set_device_address" && execution.ActionID != "set_baud_rate" {
		return nil
	}
	var event ConfigChangeEvent
	var changed bool
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var err error
		event, changed, err = m.applySN3001ControlSideEffectTx(tx, execution)
		return err
	})
	if err != nil || !changed {
		return err
	}
	if m.eventBus == nil {
		return fmt.Errorf("configuration event bus is unavailable")
	}
	return m.eventBus.Publish(event)
}

func (m *Manager) applySN3001AddressSideEffect(execution models.CommandExecution) error {
	var event ConfigChangeEvent
	changed := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var err error
		event, changed, err = m.applySN3001ControlSideEffectTx(tx, execution)
		return err
	})
	if err != nil || !changed {
		return err
	}
	if m.eventBus == nil {
		return fmt.Errorf("configuration event bus is unavailable")
	}
	return m.eventBus.Publish(event)
}

// applySN3001ControlSideEffectTx changes only instance-owned configuration
// and must not publish. Callers that already own a transaction use it as the
// finalizer of the command Inbox transition; standalone callers wrap it in a
// transaction and publish after commit.
func (m *Manager) applySN3001ControlSideEffectTx(tx *gorm.DB, execution models.CommandExecution) (ConfigChangeEvent, bool, error) {
	if execution.DeviceType != "sn3001_rain" {
		return ConfigChangeEvent{}, false, nil
	}
	if execution.ActionID == "set_baud_rate" {
		var params struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(execution.ParamsJSON), &params); err != nil {
			return ConfigChangeEvent{}, false, fmt.Errorf("decode baud parameters: %w", err)
		}
		target := map[string]uint32{"2400": 2400, "4800": 4800, "9600": 9600}[strings.TrimSpace(params.Value)]
		if target == 0 {
			return ConfigChangeEvent{}, false, fmt.Errorf("unsupported SN-3001 baud rate %q", params.Value)
		}
		var channel models.Channel
		if err := tx.Where("id = ? AND node_id = ?", execution.ChannelID, execution.NodeID).First(&channel).Error; err != nil {
			return ConfigChangeEvent{}, false, err
		}
		text := strings.TrimSpace(channel.BusConfig)
		if strings.HasPrefix(text, "\\x") || strings.HasPrefix(text, "0x") {
			text = text[2:]
		}
		data, err := hex.DecodeString(text)
		if err != nil || len(data) < 6 {
			return ConfigChangeEvent{}, false, fmt.Errorf("channel %d has malformed UART bus_config", channel.ID)
		}
		current := uint32(data[2])<<24 | uint32(data[3])<<16 | uint32(data[4])<<8 | uint32(data[5])
		if current == target {
			return ConfigChangeEvent{}, false, nil
		}
		data[2] = byte(target >> 24)
		data[3] = byte(target >> 16)
		data[4] = byte(target >> 8)
		data[5] = byte(target)
		updated := strings.ToUpper(hex.EncodeToString(data))
		if err := tx.Model(&models.Channel{}).Where("id = ? AND node_id = ?", channel.ID, channel.NodeID).Update("bus_config", updated).Error; err != nil {
			return ConfigChangeEvent{}, false, err
		}
		event := newControlConfigChangeEvent(execution, CfgChangeChannel, fmt.Sprint(execution.ChannelID), "control:set_baud_rate")
		if err := persistConfigChangeOutbox(tx, event); err != nil {
			return ConfigChangeEvent{}, false, err
		}
		return event, true, nil
	}
	if execution.ActionID != "set_device_address" {
		return ConfigChangeEvent{}, false, nil
	}
	var params struct {
		Value json.Number `json:"value"`
	}
	if err := json.Unmarshal([]byte(execution.ParamsJSON), &params); err != nil {
		return ConfigChangeEvent{}, false, fmt.Errorf("decode address parameters: %w", err)
	}
	target, err := strconv.ParseUint(string(params.Value), 10, 8)
	if err != nil || target < 1 || target > 254 {
		return ConfigChangeEvent{}, false, fmt.Errorf("unsupported SN-3001 device address %q", params.Value)
	}
	var edge models.EdgeDevice
	if err := tx.Where("id = ? AND node_id = ?", execution.EdgeDeviceID, execution.NodeID).First(&edge).Error; err != nil {
		return ConfigChangeEvent{}, false, err
	}
	current := strings.TrimSpace(edge.HardwareID)
	currentValue, parseErr := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(current, "0x"), "0X"), 0, 8)
	if parseErr == nil && currentValue == target {
		return ConfigChangeEvent{}, false, nil
	}
	if err := tx.Model(&models.EdgeDevice{}).Where("id = ? AND node_id = ?", edge.ID, edge.NodeID).Update("hardware_id", strconv.FormatUint(target, 10)).Error; err != nil {
		return ConfigChangeEvent{}, false, err
	}
	event := newControlConfigChangeEvent(execution, CfgChangeEdgeDevice, fmt.Sprint(execution.EdgeDeviceID), "control:set_device_address")
	if err := persistConfigChangeOutbox(tx, event); err != nil {
		return ConfigChangeEvent{}, false, err
	}
	return event, true, nil
}

func newControlConfigChangeEvent(execution models.CommandExecution, changeType ConfigChangeType, entityID string, actor string) ConfigChangeEvent {
	eventID := ""
	if execution.CommandID != "" {
		eventID = fmt.Sprintf("config:v2:%s", execution.CommandID)
	} else {
		eventID = "config:v2:" + uuid.NewString()
	}
	return ConfigChangeEvent{EventID: eventID, Type: changeType, Action: CfgActionUpdate, NodeID: execution.NodeID, EntityID: entityID, Actor: actor}
}

func persistConfigChangeOutbox(tx *gorm.DB, event ConfigChangeEvent) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.ConfigChangeOutbox{
		EventID: event.EventID, Type: string(event.Type), Action: string(event.Action), NodeID: event.NodeID,
		EntityID: event.EntityID, Actor: event.Actor, State: "PENDING", CreatedAt: time.Now().UTC(),
	}).Error
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
