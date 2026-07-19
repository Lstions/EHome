package commandexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InboxEvent struct {
	EventID, CommandID, EventType, BootID string
	AttemptNo                             uint32
	Payload                               interface{}
	VerifiedResultJSON                    string
}

func (s *Service) RecordInbox(ctx context.Context, event InboxEvent) (*models.CommandExecution, bool, error) {
	if event.EventID == "" || event.CommandID == "" {
		return nil, false, fmt.Errorf("invalid inbox event")
	}
	if event.VerifiedResultJSON != "" && !json.Valid([]byte(event.VerifiedResultJSON)) {
		return nil, false, fmt.Errorf("invalid verified result")
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return nil, false, err
	}
	var execution models.CommandExecution
	applied := false
	var acceptDuration time.Duration
	acceptDurationObserved := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receivedAt := s.now()
		inbox := models.CommandInbox{EventID: event.EventID, CommandID: event.CommandID, EventType: event.EventType, AttemptNo: event.AttemptNo, BootID: event.BootID, PayloadJSON: string(payload), ReceivedAt: receivedAt}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&inbox)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			return nil
		}
		if err := tx.First(&execution, "command_id = ?", event.CommandID).Error; err != nil {
			return err
		}
		to := ""
		switch event.EventType {
		case "accepted":
			to = StatusDeviceAccepted
		case "verifying":
			to = StatusVerifying
		case "final_succeeded":
			to = StatusSucceeded
		case "final_failed":
			to = StatusFailed
		case "unknown":
			to = StatusUnknown
		default:
			return fmt.Errorf("unknown inbox event %q", event.EventType)
		}
		if err := validateTransition(execution.Status, to); err != nil {
			return nil
		} // stale/terminal events are recorded but inert
		now := receivedAt
		if to == StatusDeviceAccepted {
			var attempt models.CommandAttempt
			if err := tx.Select("published_at").Where("command_id = ? AND attempt_no = ? AND boot_id = ?", event.CommandID, event.AttemptNo, event.BootID).First(&attempt).Error; err != nil {
				return err
			}
			if attempt.PublishedAt != nil {
				acceptDuration = now.Sub(*attempt.PublishedAt)
				acceptDurationObserved = true
			}
		}
		updates := map[string]interface{}{"status": to}
		if IsTerminal(to) {
			updates["completed_at"] = now
			updates["final_reason"] = event.EventType
			if to == StatusSucceeded && event.VerifiedResultJSON != "" {
				updates["verified_result_json"] = event.VerifiedResultJSON
			}
		}
		result := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", execution.CommandID, execution.Status).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		attemptUpdates := map[string]interface{}{"status": to}
		if IsTerminal(to) {
			attemptUpdates["completed_at"] = now
		}
		attemptResult := tx.Model(&models.CommandAttempt{}).
			Where("command_id = ? AND attempt_no = ? AND boot_id = ?", event.CommandID, event.AttemptNo, event.BootID).
			Updates(attemptUpdates)
		if attemptResult.Error != nil {
			return attemptResult.Error
		}
		if attemptResult.RowsAffected != 1 {
			return fmt.Errorf("command attempt identity not found")
		}
		execution.Status = to
		if IsTerminal(to) {
			execution.CompletedAt = &now
			execution.FinalReason = event.EventType
			if to == StatusSucceeded && event.VerifiedResultJSON != "" {
				execution.VerifiedResultJSON = event.VerifiedResultJSON
			}
		}
		applied = true
		return nil
	})
	if err == nil && applied {
		metrics.DeviceActionTransitionsTotal.WithLabelValues(execution.Status).Inc()
		if IsTerminal(execution.Status) && !execution.CreatedAt.IsZero() {
			metrics.DeviceActionDuration.Observe(s.now().Sub(execution.CreatedAt).Seconds())
		}
		if execution.Status == StatusDeviceAccepted && acceptDurationObserved && acceptDuration >= 0 {
			metrics.DeviceActionAcceptDuration.Observe(acceptDuration.Seconds())
		}
	}
	return &execution, applied, err
}

// RecoverExpired never creates a new physical attempt. A lost publication
// lease is made available again only while the execution is still QUEUED;
// anything dispatched past its deadline is conservatively UNKNOWN. It returns
// the changed executions so the composition root can publish their terminal
// state to connected clients after the transaction commits.
func (s *Service) RecoverExpired(ctx context.Context) ([]models.CommandExecution, error) {
	now := s.now()
	var expired []models.CommandExecution
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CommandOutbox{}).Where("state = ? AND lease_expires_at < ?", "LEASED", now).Updates(map[string]interface{}{"state": "PENDING", "lease_owner": "", "lease_expires_at": nil}).Error; err != nil {
			return err
		}
		var queued []models.CommandExecution
		if err := tx.Where("status = ? AND deadline_at < ?", StatusQueued, now).Find(&queued).Error; err != nil {
			return err
		}
		if len(queued) > 0 {
			commandIDs := make([]string, 0, len(queued))
			for i := range queued {
				commandIDs = append(commandIDs, queued[i].CommandID)
			}
			if err := tx.Model(&models.CommandExecution{}).Where("command_id IN ? AND status = ?", commandIDs, StatusQueued).Updates(map[string]interface{}{"status": StatusFailed, "completed_at": now, "final_reason": "deadline expired before dispatch"}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.CommandOutbox{}).Where("command_id IN ? AND state IN ?", commandIDs, []string{"PENDING", "LEASED"}).Updates(map[string]interface{}{"state": "CANCELLED", "processed_at": now, "lease_expires_at": nil}).Error; err != nil {
				return err
			}
			for i := range queued {
				queued[i].Status = StatusFailed
				queued[i].CompletedAt = &now
				queued[i].FinalReason = "deadline expired before dispatch"
			}
			expired = append(expired, queued...)
		}

		var dispatched []models.CommandExecution
		if err := tx.Where("status IN ? AND deadline_at < ?", []string{StatusDispatched, StatusDeviceAccepted, StatusVerifying}, now).Find(&dispatched).Error; err != nil {
			return err
		}
		if len(dispatched) == 0 {
			return nil
		}
		commandIDs := make([]string, 0, len(dispatched))
		for i := range dispatched {
			commandIDs = append(commandIDs, dispatched[i].CommandID)
		}
		updates := map[string]interface{}{"status": StatusUnknown, "completed_at": now, "final_reason": "deadline expired without final evidence"}
		if err := tx.Model(&models.CommandExecution{}).Where("command_id IN ? AND status IN ?", commandIDs, []string{StatusDispatched, StatusDeviceAccepted, StatusVerifying}).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.CommandAttempt{}).Where("command_id IN ? AND status IN ?", commandIDs, []string{StatusDispatched, StatusDeviceAccepted, StatusVerifying}).Updates(map[string]interface{}{"status": StatusUnknown, "completed_at": now}).Error; err != nil {
			return err
		}
		for i := range dispatched {
			dispatched[i].Status = StatusUnknown
			dispatched[i].CompletedAt = &now
			dispatched[i].FinalReason = "deadline expired without final evidence"
		}
		expired = append(expired, dispatched...)
		return nil
	})
	if err == nil {
		for _, execution := range expired {
			metrics.DeviceActionTransitionsTotal.WithLabelValues(execution.Status).Inc()
			if !execution.CreatedAt.IsZero() {
				metrics.DeviceActionDuration.Observe(now.Sub(execution.CreatedAt).Seconds())
			}
		}
	}
	return expired, err
}

var _ = time.Time{}
