package commandexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

// InboxFinalizer runs inside the same database transaction that accepts a
// valid inbox event. It is used for device-side final events whose durable
// configuration side effects must not be committed independently.
type InboxFinalizer func(*gorm.DB) error

func (s *Service) RecordInbox(ctx context.Context, event InboxEvent) (*models.CommandExecution, bool, error) {
	return s.recordInbox(ctx, event, nil)
}

func (s *Service) RecordInboxWithFinalizer(ctx context.Context, event InboxEvent, finalizer InboxFinalizer) (*models.CommandExecution, bool, error) {
	return s.recordInbox(ctx, event, finalizer)
}

func (s *Service) recordInbox(ctx context.Context, event InboxEvent, finalizer InboxFinalizer) (*models.CommandExecution, bool, error) {
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
		// Run durable side effects only after both conditional state transitions
		// have succeeded. The transaction still provides atomicity, while this
		// ordering prevents a stale event from applying a config mutation when
		// its command-state UPDATE affects zero rows.
		if finalizer != nil {
			if err := finalizer(tx); err != nil {
				return err
			}
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

// loadAttemptFinalReasons reads the attempt-scoped rejection causes for the
// given commands. Only rows that actually carry a cause are returned, so a
// missing entry and an empty cause are indistinguishable to the caller — which
// is intended: both mean "the attempt level has nothing to say".
//
// It is read-only and, like every other statement in RecoverExpired, runs
// inside the caller's transaction so the composed reason can never mix a cause
// written by a concurrent dispatcher with a row set selected before it.
func loadAttemptFinalReasons(tx *gorm.DB, commandIDs []string) (map[string]string, error) {
	reasons := map[string]string{}
	if len(commandIDs) == 0 {
		return reasons, nil
	}
	var attempts []models.CommandAttempt
	if err := tx.Select("command_id", "attempt_no", "final_reason").
		Where("command_id IN ? AND final_reason <> ''", commandIDs).
		Order("attempt_no").Find(&attempts).Error; err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		// Ascending attempt_no: the FIRST attempt that recorded a cause is the one
		// the operator needs, because it is the one that explains why the command
		// never got out. Later attempts overwrite this map entry only if the first
		// one had no cause at all.
		if _, seen := reasons[attempt.CommandID]; seen {
			continue
		}
		if cause := strings.TrimSpace(attempt.FinalReason); cause != "" {
			reasons[attempt.CommandID] = cause
		}
	}
	return reasons, nil
}

// composeDeadlineReason builds the terminal reason for an expired QUEUED
// command, MOST SPECIFIC CAUSE FIRST, and guarantees the result fits the
// command_executions.final_reason column (size:256).
//
// Priority, and the compatibility contract for each branch:
//  1. attempt-scoped cause (command_attempts.final_reason) — the M-3 addition;
//  2. execution-level cause (command_executions.final_reason) — what G5 read;
//  3. neither — the EXACT legacy text "deadline expired before dispatch",
//     byte for byte, with no trailing "; " and no ellipsis.
//
// The budget is applied to the CAUSE, not to the concatenation: clamping the
// whole string would let a long cause push the "deadline expired before
// dispatch" statement off the end, which is the one sentence the operator uses
// to tell "refused" apart from "never dispatched". The suffix is reserved
// first, the cause is clamped into what is left (rune boundaries, so a
// multi-byte cause cannot be cut mid-character), and the result is clamped once
// more as a guard in case the budget arithmetic ever changes.
func composeDeadlineReason(attemptCause, executionCause string) string {
	const deadlineText = "deadline expired before dispatch"
	cause := strings.TrimSpace(attemptCause)
	if cause == "" {
		cause = strings.TrimSpace(executionCause)
	}
	if cause == "" {
		return deadlineText
	}
	suffix := "; " + deadlineText
	causeBudget := FinalReasonColumnRunes - utf8.RuneCountInString(suffix)
	if causeBudget < 1 {
		return clampRunes(deadlineText, FinalReasonColumnRunes)
	}
	return clampRunes(clampRunes(cause, causeBudget)+suffix, FinalReasonColumnRunes)
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
			// G5 (2026-09-21): the deadline is ONE reason, but it is not always the
			// MOST SPECIFIC one. dispatcher.recordDispatchRejection already wrote
			// "dispatch rejected: <cause>" while the command was still QUEUED
			// (dispatcher.go recordDispatchRejection), precisely so the operator
			// could see WHY nothing was dispatched. Overwriting it here with the generic
			// deadline text re-hid that cause behind the same message the 2026-09-20
			// incident produced, which is what made the outage 120s of silence.
			//
			// M-3 (2026-09-21): the cause is now ALSO recorded on the attempt that was
			// rejected (command_attempts.final_reason), and that attempt-scoped copy is
			// read FIRST. The two copies exist for different lifetimes: the execution
			// row is what the UI shows while the command is QUEUED, and it must be
			// cleared once an attempt actually publishes (otherwise the new attempt
			// would inherit the old attempt's cause); the attempt row is the durable
			// "this specific delivery was refused, and why". Preferring the attempt
			// copy means a future second attempt can clear the execution-level value
			// without erasing the first attempt's cause. When only the execution-level
			// copy exists — rows written before this column, or a rejection that could
			// not be attributed to an attempt — the composed text is byte-for-byte
			// what G5 produced.
			//
			// Semantics that must NOT change: status still becomes FAILED, completed_at
			// is still stamped, the outbox is still CANCELLED, and a command with no
			// recorded rejection still gets exactly "deadline expired before dispatch".
			// Only the case "a specific cause is already known" keeps that cause.
			//
			// Per-row UPDATEs (instead of one IN (...) batch) are the price of that
			// distinction: the reason is per-command data, so the rows can no longer
			// share a single statement. The row set, the WHERE status = QUEUED guard and
			// the transaction are unchanged, so no extra row can be touched.
			attemptReasons, err := loadAttemptFinalReasons(tx, commandIDs)
			if err != nil {
				return err
			}
			for i := range queued {
				reason := composeDeadlineReason(attemptReasons[queued[i].CommandID], queued[i].FinalReason)
				if err := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", queued[i].CommandID, StatusQueued).Updates(map[string]interface{}{"status": StatusFailed, "completed_at": now, "final_reason": reason}).Error; err != nil {
					return err
				}
				queued[i].FinalReason = reason
			}
			if err := tx.Model(&models.CommandOutbox{}).Where("command_id IN ? AND state IN ?", commandIDs, []string{"PENDING", "LEASED"}).Updates(map[string]interface{}{"state": "CANCELLED", "processed_at": now, "lease_expires_at": nil}).Error; err != nil {
				return err
			}
			for i := range queued {
				queued[i].Status = StatusFailed
				queued[i].CompletedAt = &now
				// FinalReason was composed and persisted above; the returned slice must
				// carry THAT value, not the generic deadline text — the composition root
				// publishes these structs to connected clients.
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
		// The without-evidence text is a constant well inside the column budget, but
		// it is clamped through the same helper as the composed text so the column
		// budget has exactly ONE enforcement point for every terminal reason.
		withoutEvidenceReason := clampRunes("deadline expired without final evidence", FinalReasonColumnRunes)
		updates := map[string]interface{}{"status": StatusUnknown, "completed_at": now, "final_reason": withoutEvidenceReason}
		if err := tx.Model(&models.CommandExecution{}).Where("command_id IN ? AND status IN ?", commandIDs, []string{StatusDispatched, StatusDeviceAccepted, StatusVerifying}).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.CommandAttempt{}).Where("command_id IN ? AND status IN ?", commandIDs, []string{StatusDispatched, StatusDeviceAccepted, StatusVerifying}).Updates(map[string]interface{}{"status": StatusUnknown, "completed_at": now}).Error; err != nil {
			return err
		}
		for i := range dispatched {
			dispatched[i].Status = StatusUnknown
			dispatched[i].CompletedAt = &now
			dispatched[i].FinalReason = withoutEvidenceReason
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
