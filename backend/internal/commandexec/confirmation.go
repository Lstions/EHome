package commandexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

var (
	ErrConfirmationRequired    = errors.New("confirmation is required")
	ErrConfirmationInvalid     = errors.New("confirmation is invalid, expired, or already used")
	ErrRecentAuthRequired      = errors.New("recent authentication is required")
	ErrConfirmationNotNeeded   = errors.New("confirmation is not required for this action")
	ErrConfirmationRateLimited = errors.New("confirmation rate limit exceeded")
)

const confirmationLifetime = 5 * time.Minute
const recentAuthenticationWindow = 10 * time.Minute
const confirmationRateWindow = time.Minute
const maxConfirmationsPerWindow = 5

type ConfirmationInput struct {
	EdgeDeviceID uint
	ActorUserID  uint
	ActionID     string
	Params       json.RawMessage
	Reason       string
	SourceIP     string
}

type ConfirmationGrant struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Parameterized writes are intentionally confirmed from medium risk upward.
// This is stricter than the minimum high/critical policy and keeps the first
// recoverable setter from becoming a one-click physical change.
func confirmationRequired(risk string) bool {
	return risk == "medium" || risk == "high" || risk == "critical"
}

// IssueConfirmation binds a single use token to the actor, target, action
// version and canonical request hash. It intentionally checks the same
// current availability facts as Create, so confirmation cannot mint a grant
// for an offline/stale node and use it later after the environment changed.
func (s *Service) IssueConfirmation(ctx context.Context, in ConfirmationInput) (*ConfirmationGrant, error) {
	if in.EdgeDeviceID == 0 || in.ActorUserID == 0 || strings.TrimSpace(in.ActionID) == "" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 512 {
		return nil, ErrConfirmationRequired
	}
	var grant ConfirmationGrant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !s.dispatchEnabled {
			return ErrActionUnavailable
		}
		var edge models.EdgeDevice
		if err := tx.Preload("Node").First(&edge, in.EdgeDeviceID).Error; err != nil {
			return err
		}
		if !edge.Enabled || edge.Status == "inactive" || edge.Node.Status != "online" {
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
		definition, ok := s.actions.Get(edge.Type, in.ActionID)
		if !ok || !definition.Enabled {
			return ErrActionUnavailable
		}
		if !confirmationRequired(definition.Risk) {
			return ErrConfirmationNotNeeded
		}
		params, err := deviceaction.CanonicalizeParams(definition.InputSchema, in.Params)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidParams, err)
		}
		digest := sha256.Sum256(params)
		requestHash := hex.EncodeToString(digest[:])
		var user models.User
		if err := tx.First(&user, in.ActorUserID).Error; err != nil {
			return err
		}
		now := s.now()
		if !user.Enabled || user.SubjectKey == nil || *user.SubjectKey != models.SystemAdminSubjectKey || user.LastLoginAt == nil || user.LastLoginAt.Before(now.Add(-recentAuthenticationWindow)) {
			return ErrRecentAuthRequired
		}
		var issued int64
		if err := tx.Model(&models.CommandConfirmation{}).Where("actor_user_id = ? AND edge_device_id = ? AND action_id = ? AND created_at >= ?", in.ActorUserID, edge.ID, definition.ID, now.Add(-confirmationRateWindow)).Count(&issued).Error; err != nil {
			return err
		}
		if issued >= maxConfirmationsPerWindow {
			return ErrConfirmationRateLimited
		}
		rawToken, err := randomConfirmationToken()
		if err != nil {
			return err
		}
		tokenHash := confirmationHash(rawToken)
		expiresAt := now.Add(confirmationLifetime)
		if deadline := user.LastLoginAt.Add(recentAuthenticationWindow); deadline.Before(expiresAt) {
			expiresAt = deadline
		}
		grant = ConfirmationGrant{Token: rawToken, ExpiresAt: expiresAt}
		if err := tx.Create(&models.CommandConfirmation{TokenHash: tokenHash, ActorUserID: in.ActorUserID, EdgeDeviceID: edge.ID, ActionID: definition.ID, ActionVersion: definition.Version, RequestHash: requestHash, ExpiresAt: grant.ExpiresAt, CreatedAt: now}).Error; err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "user", ActorUserID: &in.ActorUserID, EventName: "device_action.confirmation_issued", Result: "issued", SourceIP: in.SourceIP, TargetType: "edge_device", TargetID: fmt.Sprint(edge.ID), Metadata: map[string]interface{}{"action_id": definition.ID, "action_version": definition.Version, "request_hash": requestHash, "reason": in.Reason}})
	})
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

func (s *Service) consumeConfirmation(tx *gorm.DB, rawToken string, actorID, edgeDeviceID uint, definition deviceaction.Definition, requestHash string) error {
	if strings.TrimSpace(rawToken) == "" {
		return ErrConfirmationRequired
	}
	now := s.now()
	var user models.User
	if err := tx.First(&user, actorID).Error; err != nil {
		return err
	}
	if !user.Enabled || user.LastLoginAt == nil || user.LastLoginAt.Before(now.Add(-recentAuthenticationWindow)) {
		return ErrRecentAuthRequired
	}
	result := tx.Model(&models.CommandConfirmation{}).Where("token_hash = ? AND actor_user_id = ? AND edge_device_id = ? AND action_id = ? AND action_version = ? AND request_hash = ? AND consumed_at IS NULL AND expires_at > ?", confirmationHash(rawToken), actorID, edgeDeviceID, definition.ID, definition.Version, requestHash, now).Update("consumed_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConfirmationInvalid
	}
	return nil
}

func randomConfirmationToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func confirmationHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
