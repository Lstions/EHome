package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// DispatchResult is immutable evidence from the transport compiler. The
// dispatcher writes it with the attempt state in the same transaction that
// marks the outbox processed.
type DispatchResult struct {
	BootID      string
	PublishedAt time.Time
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

// ProcessOnce leases one outbox row. It does nothing when no transport is
// configured, which is the production-safe Phase 1 default.
func (d *Dispatcher) ProcessOnce(ctx context.Context) (bool, error) {
	if d.transport == nil {
		return false, nil
	}
	var claimed models.CommandOutbox
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := d.now()
		var candidate models.CommandOutbox
		if err := tx.Where("state = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", "PENDING", now).Order("id").First(&candidate).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		until := now.Add(30 * time.Second)
		update := tx.Model(&models.CommandOutbox{}).Where("id = ? AND state = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", candidate.ID, "PENDING", now).Updates(map[string]interface{}{"state": "LEASED", "lease_owner": d.owner, "lease_expires_at": until, "fencing_token": candidate.FencingToken + 1})
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
	err = d.dispatch(ctx, claimed)
	if err != nil {
		metrics.DeviceActionDispatchTotal.WithLabelValues("error").Inc()
	} else {
		metrics.DeviceActionDispatchTotal.WithLabelValues("published").Inc()
	}
	return true, err
}

func (d *Dispatcher) dispatch(ctx context.Context, outbox models.CommandOutbox) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var execution models.CommandExecution
		if err := tx.First(&execution, "command_id = ?", outbox.CommandID).Error; err != nil {
			return err
		}
		if execution.Status != StatusQueued {
			return tx.Model(&models.CommandOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{"state": "CANCELLED", "processed_at": d.now()}).Error
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
		if err := tx.Model(&models.CommandAttempt{}).Where("id = ? AND status = ?", attempt.ID, StatusDispatched).Updates(map[string]interface{}{"boot_id": result.BootID, "published_at": result.PublishedAt}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", execution.CommandID, StatusQueued).Update("status", StatusDispatched).Error; err != nil {
			return err
		}
		return tx.Model(&models.CommandOutbox{}).Where("id = ? AND state = ? AND fencing_token = ?", outbox.ID, "LEASED", outbox.FencingToken).Updates(map[string]interface{}{"state": "PROCESSED", "processed_at": now, "lease_expires_at": nil}).Error
	})
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
