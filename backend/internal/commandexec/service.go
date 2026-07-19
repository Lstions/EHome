package commandexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

var (
	ErrIdempotencyCollision = errors.New("idempotency key was already used with different request")
	ErrActionUnavailable    = errors.New("action is unavailable for this device")
	ErrInvalidParams        = errors.New("action parameters are invalid")
	ErrNotCancellable       = errors.New("execution is no longer cancellable")
)

type Service struct {
	db              *gorm.DB
	actions         *deviceaction.Registry
	now             func() time.Time
	dispatchEnabled bool
}

func NewService(db *gorm.DB, actions *deviceaction.Registry) *Service {
	return &Service{db: db, actions: actions, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetDispatchEnabled(enabled bool) { s.dispatchEnabled = enabled }

func (s *Service) Database() *gorm.DB { return s.db }

type CreateInput struct {
	EdgeDeviceID      uint
	ActorUserID       uint
	ActionID          string
	Params            json.RawMessage
	IdempotencyKey    string
	SourceIP          string
	ConfirmationToken string
	Reason            string
}

type CatalogItem struct {
	Definition deviceaction.Definition `json:"definition"`
	Available  bool                    `json:"available"`
	Reason     string                  `json:"reason,omitempty"`
}

func (s *Service) Catalog(ctx context.Context, edgeDeviceID uint) ([]CatalogItem, error) {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).Preload("Node").First(&edge, edgeDeviceID).Error; err != nil {
		return nil, err
	}
	var channelErr error
	if _, err := loadActionChannel(s.db.WithContext(ctx), edge); err != nil {
		channelErr = err
	} else if err := requireReportedActionChannel(edge.Node, edge.ChannelID); err != nil {
		channelErr = err
	}
	items := make([]CatalogItem, 0)
	for _, definition := range s.actions.List(edge.Type) {
		item := CatalogItem{Definition: definition, Available: definition.Enabled && s.dispatchEnabled && edge.Enabled && edge.Status != "inactive" && edge.Node.Status == "online"}
		if !s.dispatchEnabled {
			item.Reason = "device control v2 is disabled"
			items = append(items, item)
			continue
		}
		if !definition.Enabled {
			item.Reason = "action is not enabled for rollout"
			items = append(items, item)
			continue
		}
		if !item.Available {
			item.Reason = "edge device or node is unavailable"
		} else if channelErr != nil {
			item.Available = false
			item.Reason = "action channel is unavailable"
		} else if _, _, err := currentCapabilities(edge.Node, s.now); err != nil {
			item.Available = false
			item.Reason = "ChannelCmdV2 capability is unavailable or stale"
		}
		items = append(items, item)
	}
	return items, nil
}

// Create persists execution, audit event and outbox in one transaction. No
// transport is touched here; the dispatcher is the only publication path.
func (s *Service) Create(ctx context.Context, in CreateInput) (*models.CommandExecution, bool, error) {
	if in.EdgeDeviceID == 0 || in.ActorUserID == 0 || strings.TrimSpace(in.ActionID) == "" || !validIdempotencyKey(in.IdempotencyKey) {
		return nil, false, fmt.Errorf("invalid command request")
	}
	var result models.CommandExecution
	replayed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !s.dispatchEnabled {
			return ErrActionUnavailable
		}
		var edge models.EdgeDevice
		if err := tx.Preload("Node").First(&edge, in.EdgeDeviceID).Error; err != nil {
			return err
		}
		if !edge.Enabled || edge.Status == "inactive" || edge.NodeID == "" || edge.Node.Status != "online" {
			return ErrActionUnavailable
		}
		if _, err := loadActionChannel(tx, edge); err != nil {
			return ErrActionUnavailable
		}
		if err := requireReportedActionChannel(edge.Node, edge.ChannelID); err != nil {
			return ErrActionUnavailable
		}
		if _, _, err := currentCapabilities(edge.Node, s.now); err != nil {
			return ErrActionUnavailable
		}
		def, ok := s.actions.Get(edge.Type, in.ActionID)
		if !ok || !def.Enabled {
			return ErrActionUnavailable
		}
		params, err := deviceaction.CanonicalizeParams(def.InputSchema, in.Params)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidParams, err)
		}
		digest := sha256.Sum256(params)
		hash := hex.EncodeToString(digest[:])
		scope := fmt.Sprintf("user:%d:edge:%d:action:%s:v:%d", in.ActorUserID, edge.ID, def.ID, def.Version)
		var existing models.CommandExecution
		err = tx.Where("idempotency_scope = ? AND idempotency_key = ?", scope, in.IdempotencyKey).First(&existing).Error
		if err == nil {
			if existing.RequestHash != hash {
				return ErrIdempotencyCollision
			}
			result, replayed = existing, true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if confirmationRequired(def.Risk) {
			if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 512 {
				return ErrConfirmationRequired
			}
			if err := s.consumeConfirmation(tx, in.ConfirmationToken, in.ActorUserID, edge.ID, def, hash); err != nil {
				return err
			}
		}
		now := s.now()
		commandID, err := newCommandID()
		if err != nil {
			return err
		}
		result = models.CommandExecution{
			CommandID: commandID, EdgeDeviceID: edge.ID, NodeID: edge.NodeID,
			ActionID: def.ID, ActionVersion: def.Version, CommandEngineRevision: edge.Node.CommandEngineRevision, ActorUserID: in.ActorUserID,
			IdempotencyScope: scope, IdempotencyKey: in.IdempotencyKey, RequestHash: hash,
			ParamsJSON: string(params), Status: StatusQueued, DeadlineAt: now.Add(2 * time.Minute), CreatedAt: now,
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		outbox := models.CommandOutbox{CommandID: commandID, EventType: "command.dispatch", PayloadJSON: string(params), State: "PENDING", CreatedAt: now}
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{
			ActorType: "user", ActorUserID: &in.ActorUserID, EventName: "device_action.created", Result: "queued",
			RequestID: commandID, SourceIP: in.SourceIP, TargetType: "edge_device", TargetID: fmt.Sprint(edge.ID),
			Metadata: map[string]interface{}{"action_id": def.ID, "action_version": def.Version, "request_hash": hash},
		})
	})
	if err == nil {
		resultLabel := "queued"
		if replayed {
			resultLabel = "replayed"
		}
		metrics.DeviceActionCreatedTotal.WithLabelValues(resultLabel).Inc()
	}
	return &result, replayed, err
}

func (s *Service) Get(ctx context.Context, commandID string) (*models.CommandExecution, error) {
	var execution models.CommandExecution
	if err := s.db.WithContext(ctx).First(&execution, "command_id = ?", commandID).Error; err != nil {
		return nil, err
	}
	return &execution, nil
}

// VerifyFinal applies the frozen action version and its trusted Driver
// verifier to a successful device Final.  It is intentionally kept inside the
// control domain so nodemgr cannot accidentally fall back to treating a setter
// ACK as generic sensor data.
func (s *Service) VerifyFinal(ctx context.Context, execution models.CommandExecution, raw []byte) ([]drivers.SensorData, error) {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).First(&edge, execution.EdgeDeviceID).Error; err != nil {
		return nil, err
	}
	if edge.NodeID != execution.NodeID {
		return nil, fmt.Errorf("edge device identity changed")
	}
	definition, ok := s.actions.Get(edge.Type, execution.ActionID)
	if !ok || definition.Version != execution.ActionVersion {
		return nil, fmt.Errorf("action definition %s version %d is unavailable", execution.ActionID, execution.ActionVersion)
	}
	params, err := deviceaction.CanonicalizeParams(definition.InputSchema, json.RawMessage(execution.ParamsJSON))
	if err != nil || string(params) != execution.ParamsJSON {
		return nil, fmt.Errorf("persisted action parameters are invalid")
	}
	return definition.Verify(params, raw)
}

func (s *Service) List(ctx context.Context, edgeDeviceID uint, limit int) ([]models.CommandExecution, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var items []models.CommandExecution
	err := s.db.WithContext(ctx).Where("edge_device_id = ?", edgeDeviceID).Order("created_at DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (s *Service) Cancel(ctx context.Context, commandID string, actor uint) (*models.CommandExecution, error) {
	var execution models.CommandExecution
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&execution, "command_id = ?", commandID).Error; err != nil {
			return err
		}
		if execution.ActorUserID != actor || execution.Status != StatusQueued {
			return ErrNotCancellable
		}
		now := s.now()
		updated := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", commandID, StatusQueued).Updates(map[string]interface{}{"status": StatusCancelled, "completed_at": now, "final_reason": "cancelled by actor"})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrNotCancellable
		}
		if err := tx.Model(&models.CommandOutbox{}).Where("command_id = ? AND state = ?", commandID, "PENDING").Update("state", "CANCELLED").Error; err != nil {
			return err
		}
		execution.Status, execution.CompletedAt, execution.FinalReason = StatusCancelled, &now, "cancelled by actor"
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "user", ActorUserID: &actor, EventName: "device_action.cancelled", Result: "cancelled", RequestID: commandID, TargetType: "edge_device", TargetID: fmt.Sprint(execution.EdgeDeviceID), Metadata: map[string]interface{}{"action_id": execution.ActionID}})
	})
	return &execution, err
}

func validIdempotencyKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && strings.TrimSpace(key) == key
}

func newCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// UUIDv4 textual form keeps APIs/database operators familiar without a new dependency.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
