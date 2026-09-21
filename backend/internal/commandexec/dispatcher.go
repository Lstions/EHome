package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DispatchResult is immutable evidence from the transport compiler. The
// dispatcher writes it with the attempt state in the same transaction that
// marks the outbox processed.
type DispatchResult struct {
	BootID      string
	PublishedAt time.Time
	WireDigest  string
}

// Transport compiles and publishes one already-authorized physical attempt.
// A nil dispatcher remains fail-closed.
type Transport interface {
	Dispatch(context.Context, models.CommandExecution, models.CommandAttempt) (DispatchResult, error)
}

// transactionAwareTransport lets production transports read the same durable
// facts that the dispatcher is about to transition. Generic fake transports
// remain on the small Transport interface.
type transactionAwareTransport interface {
	DispatchInTransaction(context.Context, *gorm.DB, models.CommandExecution, models.CommandAttempt) (DispatchResult, error)
}

type Dispatcher struct {
	db        *gorm.DB
	transport Transport
	owner     string
	now       func() time.Time
}

func NewDispatcher(db *gorm.DB, transport Transport, owner string) *Dispatcher {
	return &Dispatcher{db: db, transport: transport, owner: owner, now: func() time.Time { return time.Now().UTC() }}
}

// NewDispatcherOwner returns a diagnostic identity that is unique across
// processes and container replicas. Lease safety still comes from database
// fencing; the owner makes competing or abandoned leases attributable.
func NewDispatcherOwner(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "dispatcher"
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown"
	}
	suffix := fmt.Sprintf(":%d:%s", os.Getpid(), uuid.NewString())
	identity := prefix + ":" + hostname
	if maxIdentity := 96 - len(suffix); len(identity) > maxIdentity {
		identity = identity[:maxIdentity]
	}
	return identity + suffix
}

// ProcessOnce leases one outbox row. It does nothing when no transport is
// configured, which is the production-safe Phase 1 default.
func (d *Dispatcher) ProcessOnce(ctx context.Context) (bool, error) {
	if d.transport == nil {
		return false, nil
	}
	var claimed models.CommandOutbox
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := d.now()
		// Recover publication leases before selecting by outbox order. Otherwise a
		// later command could overtake the expired command on the same Channel.
		if err := tx.Model(&models.CommandOutbox{}).
			Where("state = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", "LEASED", now).
			Updates(map[string]interface{}{"state": "PENDING", "lease_owner": "", "lease_expires_at": nil}).Error; err != nil {
			return err
		}
		var candidate models.CommandOutbox
		find := tx.Table("command_outboxes AS candidate_outbox").
			Select("candidate_outbox.*").
			Joins("JOIN command_executions AS candidate_execution ON candidate_execution.command_id = candidate_outbox.command_id").
			Where("candidate_outbox.state = ?", "PENDING").
			Where(`NOT EXISTS (
				SELECT 1
				FROM command_outboxes AS active_outbox
				JOIN command_executions AS active_execution ON active_execution.command_id = active_outbox.command_id
				WHERE active_outbox.state = ?
				  AND active_execution.node_id = candidate_execution.node_id
				  AND active_execution.channel_id = candidate_execution.channel_id
			)`, "LEASED").
			Order("candidate_outbox.id").Limit(1).Scan(&candidate)
		if find.Error != nil {
			return find.Error
		}
		if find.RowsAffected == 0 {
			return nil
		}
		var execution models.CommandExecution
		if err := tx.Select("node_id", "channel_id", "action_id").First(&execution, "command_id = ?", candidate.CommandID).Error; err != nil {
			return err
		}
		// Channel 锁仅对 ChannelCmdV2 有意义 (物理 UART 互斥); periph_cmd (GPIO/PWM)
		// 是节点级命令, 不走 channel 行 — channel_id=0, 跳过 channel 锁避免
		// "record not found" 失败。
		if !deviceaction.IsPeriphAction(execution.ActionID) {
			// The Channel row is the portable cross-instance mutex for this physical
			// scheduling boundary. Recheck after acquiring it because another replica
			// may have selected a sibling outbox before either transaction held it.
			var channel models.Channel
			if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND node_id = ?", execution.ChannelID, execution.NodeID).
				First(&channel).Error; err != nil {
				return fmt.Errorf("lock command channel: %w", err)
			}
		}
		var activeLeases int64
		if err := tx.Table("command_outboxes AS active_outbox").
			Joins("JOIN command_executions AS active_execution ON active_execution.command_id = active_outbox.command_id").
			Where("active_outbox.state = ?", "LEASED").
			Where("active_execution.node_id = ? AND active_execution.channel_id = ?", execution.NodeID, execution.ChannelID).
			Count(&activeLeases).Error; err != nil {
			return err
		}
		if activeLeases > 0 {
			return nil
		}
		until := now.Add(30 * time.Second)
		update := tx.Model(&models.CommandOutbox{}).
			Where("id = ? AND state = ? AND fencing_token = ?", candidate.ID, "PENDING", candidate.FencingToken).
			Updates(map[string]interface{}{"state": "LEASED", "lease_owner": d.owner, "lease_expires_at": until, "fencing_token": candidate.FencingToken + 1})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return nil
		}
		candidate.State, candidate.LeaseOwner, candidate.LeaseExpiresAt, candidate.FencingToken = "LEASED", d.owner, &until, candidate.FencingToken+1
		claimed = candidate
		return nil
	})
	if err != nil || claimed.ID == 0 {
		return claimed.ID != 0, err
	}
	published, queueDuration, err := d.dispatch(ctx, claimed)
	if err != nil {
		metrics.DeviceActionDispatchTotal.WithLabelValues("error").Inc()
		// Surface the rejection to the operator instead of letting the execution
		// sit in QUEUED until its deadline expires.
		//
		// The 2026-09-20 incident: every dispatch was rejected by
		// deviceaction.ParseHardwareAddress ("hardware_id \"UART1\" must be an
		// address from 1 to 254"), but the error only reached the server log.
		// The UI showed QUEUED for the full deadline (~120s) and then a generic
		// "deadline expired before dispatch", so the real cause was invisible.
		//
		// This runs in its own statement (NOT inside d.dispatch's transaction):
		// the transaction above rolled back precisely to leave the attempt
		// unpublished, and the outbox row is still LEASED. Writing here keeps the
		// lease, fencing token, attempt row and QUEUED status exactly as they were,
		// so RecoverExpired's deadline semantics and the retry path are unchanged;
		// only the human-readable reason becomes visible. A later successful
		// dispatch clears it again (see clearDispatchRejection).
		d.recordDispatchRejection(ctx, claimed.CommandID, err)
	} else if published {
		metrics.DeviceActionDispatchTotal.WithLabelValues("published").Inc()
		if queueDuration >= 0 {
			metrics.DeviceActionQueueDuration.Observe(queueDuration.Seconds())
		}
	} else {
		metrics.DeviceActionDispatchTotal.WithLabelValues("cancelled").Inc()
	}
	return true, err
}

// FinalReasonColumnRunes is the storage budget of every final_reason column
// this domain writes: command_executions.final_reason and
// command_attempts.final_reason are both `gorm:"size:256"`. Every composed
// string is clamped to this budget before it is sent to the database, because
// an over-long value would otherwise be silently cut (or rejected) by the
// storage engine — invisibly and differently per dialect.
//
// It is counted in RUNES: both SQLite and PostgreSQL measure a
// character-varying column in characters, and clampRunes truncates at rune
// boundaries so a multi-byte cause can never end in half a character.
const FinalReasonColumnRunes = 256

// dispatchRejectionMaxRunes bounds the WHOLE reason written by
// recordDispatchRejection, prefix included. It is deliberately smaller than
// FinalReasonColumnRunes (200 < 256): the deadline path later composes
// "<cause>; deadline expired before dispatch" onto the same value, and that
// suffix plus the "…" of a clamped cause must still fit the column. The
// composition in RecoverExpired is clamped again at FinalReasonColumnRunes, so
// this budget is a readability choice, not a correctness boundary.
const dispatchRejectionMaxRunes = 200

// firstPhysicalAttemptNo is the attempt number of the only attempt the
// dispatcher creates today. It is shared by the attempt creation site and by
// recordDispatchRejection so a rejection can never be attributed to a different
// attempt than the one that was just tried. A future retry path that opens a
// second attempt must make both sites use that attempt's own number.
const firstPhysicalAttemptNo uint32 = 1

// recordDispatchRejection publishes "this command could not be handed to the
// device, and why" so the operation history can show it while the command is
// still QUEUED.
//
// Two sinks, deliberately (M-3, 2026-09-21):
//   - the execution row, which is what R4 made visible while the command is
//     still QUEUED (the 2026-09-20 outage showed nothing at all for ~120s);
//   - the attempt row for the attempt that was rejected, which is the level the
//     reason actually belongs to.
//
// Why the write is best-effort instead of an insert: d.dispatch creates the
// attempt row INSIDE the transaction that also marks the outbox PROCESSED, and
// every error path rolls that transaction back — so in today's single-attempt
// flow the rejected attempt has no surviving row here. Creating one would be
// actively wrong: command_attempts carries the unique keys (command_id,
// attempt_no) and envelope_id that the retry's own create would then collide
// with, and it is the publication evidence whose one-to-one correspondence with
// a PROCESSED outbox is relied on by the retention invariant (INV-9 /
// datalifecycle command_outbox_cleanup.go). A placeholder row for a dispatch
// that never reached the wire would corrupt both. So the attempt row is written
// when it exists, the execution row is ALWAYS written as the durable carrier,
// and RecoverExpired (inbox.go) prefers the attempt-scoped copy when one
// exists — which is exactly the arrangement a real second attempt needs: it can
// clear the execution-level copy without destroying the first attempt's cause.
//
// Failure is deliberately non-fatal for the dispatcher: if the reason cannot be
// written, the caller's error and the metrics counter still stand, and the
// deadline path remains the backstop.
func (d *Dispatcher) recordDispatchRejection(ctx context.Context, commandID string, dispatchErr error) {
	if commandID == "" || dispatchErr == nil {
		return
	}
	detail := strings.TrimSpace(dispatchErr.Error())
	if detail == "" {
		return
	}
	reason := clampRunes("dispatch rejected: "+detail, dispatchRejectionMaxRunes)
	db := d.db.WithContext(ctx)
	execErr := db.Model(&models.CommandExecution{}).
		Where("command_id = ? AND status = ?", commandID, StatusQueued).
		Update("final_reason", reason).Error
	// Attempt-scoped copy. RowsAffected == 0 is the expected outcome while the
	// rejected attempt's transaction has rolled back; it is not an error.
	attemptErr := db.Model(&models.CommandAttempt{}).
		Where("command_id = ? AND attempt_no = ?", commandID, firstPhysicalAttemptNo).
		Update("final_reason", reason).Error
	if execErr != nil || attemptErr != nil {
		metrics.DeviceActionDispatchTotal.WithLabelValues("error").Inc()
	}
}

// clampRunes truncates a string to AT MOST limit runes (ellipsis included), so
// a multi-byte reason can never be sliced mid-character and the truncation is
// visible to the reader. The ellipsis is counted against the budget because the
// caller sizes the budget after the storage column, not after the payload.
func clampRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func (d *Dispatcher) dispatch(ctx context.Context, outbox models.CommandOutbox) (bool, time.Duration, error) {
	published := false
	var queueDuration time.Duration
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution models.CommandExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "command_id = ?", outbox.CommandID).Error; err != nil {
			return err
		}
		if execution.Status != StatusQueued {
			cancelled := tx.Model(&models.CommandOutbox{}).
				Where("id = ? AND state = ? AND fencing_token = ?", outbox.ID, "LEASED", outbox.FencingToken).
				Updates(map[string]interface{}{"state": "CANCELLED", "processed_at": d.now(), "lease_expires_at": nil})
			if cancelled.Error != nil {
				return cancelled.Error
			}
			if cancelled.RowsAffected != 1 {
				return fmt.Errorf("outbox lease fencing lost before cancellation")
			}
			return nil
		}
		attempt := models.CommandAttempt{CommandID: execution.CommandID, AttemptNo: firstPhysicalAttemptNo, Status: StatusDispatched, FencingToken: outbox.FencingToken, CreatedAt: d.now()}
		attempt.EnvelopeID = fmt.Sprintf("%s:%d", execution.CommandID, attempt.AttemptNo)
		// A lease/fencing token protects database ownership only. It must never
		// contribute to the wire identity: MQTT may have accepted the packet
		// while this transaction subsequently rolls back. In that crash window
		// the next lease must emit the byte-identical command identity, otherwise
		// the ESP32 correctly reports a command-id/digest collision.
		attempt.WireDigest = stableWireDigest(execution, attempt.AttemptNo)
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		var result DispatchResult
		var err error
		if transport, ok := d.transport.(transactionAwareTransport); ok {
			result, err = transport.DispatchInTransaction(ctx, tx, execution, attempt)
		} else {
			result, err = d.transport.Dispatch(ctx, execution, attempt)
		}
		if err != nil {
			return err
		}
		now := d.now()
		if result.PublishedAt.IsZero() {
			result.PublishedAt = now
		}
		attemptUpdates := map[string]interface{}{"boot_id": result.BootID, "published_at": result.PublishedAt}
		if result.WireDigest != "" {
			attemptUpdates["wire_digest"] = result.WireDigest
		}
		if err := tx.Model(&models.CommandAttempt{}).Where("id = ? AND status = ?", attempt.ID, StatusDispatched).Updates(attemptUpdates).Error; err != nil {
			return err
		}
		// Clear any reason recorded by an earlier rejected dispatch: the command is
		// now on the wire, so a stale rejection would misreport THIS attempt.
		// M-3: the rejected attempt keeps its own copy in command_attempts, so
		// clearing the execution-level value no longer destroys the first
		// attempt's cause — it only stops a stale summary from being read as a
		// property of the attempt that is now published.
		if err := tx.Model(&models.CommandExecution{}).
			Where("command_id = ? AND status = ?", execution.CommandID, StatusQueued).
			Update("final_reason", "").Error; err != nil {
			return err
		}
		transition := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", execution.CommandID, StatusQueued).Update("status", StatusDispatched)
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return fmt.Errorf("execution was cancelled before dispatch commit")
		}
		processed := tx.Model(&models.CommandOutbox{}).
			Where("id = ? AND state = ? AND fencing_token = ?", outbox.ID, "LEASED", outbox.FencingToken).
			Updates(map[string]interface{}{"state": "PROCESSED", "processed_at": now, "lease_expires_at": nil})
		if processed.Error != nil {
			return processed.Error
		}
		if processed.RowsAffected != 1 {
			return fmt.Errorf("outbox lease fencing lost before dispatch commit")
		}
		queueDuration = result.PublishedAt.Sub(execution.CreatedAt)
		published = true
		return nil
	})
	return published, queueDuration, err
}

// stableWireDigest is deliberately independent of outbox lease ownership and
// wall-clock state. A future *new physical attempt* gets a new attempt number;
// transport retransmission of attempt 1 keeps this identity unchanged.
func stableWireDigest(execution models.CommandExecution, attemptNo uint32) string {
	material := fmt.Sprintf("ehome.channel-cmd-v2\x00%s\x00%s\x00%d\x00%s\x00%d",
		execution.CommandID, execution.ActionID, execution.ActionVersion, execution.RequestHash, attemptNo)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}
