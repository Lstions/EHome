package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

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
		if err := tx.Select("node_id", "channel_id").First(&execution, "command_id = ?", candidate.CommandID).Error; err != nil {
			return err
		}
		// The Channel row is the portable cross-instance mutex for this physical
		// scheduling boundary. Recheck after acquiring it because another replica
		// may have selected a sibling outbox before either transaction held it.
		var channel models.Channel
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND node_id = ?", execution.ChannelID, execution.NodeID).
			First(&channel).Error; err != nil {
			return fmt.Errorf("lock command channel: %w", err)
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
		attempt := models.CommandAttempt{CommandID: execution.CommandID, AttemptNo: 1, Status: StatusDispatched, FencingToken: outbox.FencingToken, CreatedAt: d.now()}
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
