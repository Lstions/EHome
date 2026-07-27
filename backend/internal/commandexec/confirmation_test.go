package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// setupConfirmationService creates a service with a medium-risk action and a
// recently-authenticated admin user, ready for confirmation tests.
func setupConfirmationService(t *testing.T) (*Service, *models.EdgeDevice, *models.User) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC()
	node := models.Node{NodeID: "node-conf", Name: "conf", Status: "online", ConfigVersion: "manifest-conf", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-conf", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&node).Update("hardware_info", fmt.Sprintf(`{"channels":[{"id":%d,"enabled":true}]}`, ch.ID)).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "conf-dev", NodeID: node.NodeID, ChannelID: ch.ID, DeviceConfigID: 1, Type: "prs3001", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.Register(deviceaction.Definition{ID: "medium_read", Version: 1, Name: "medium read", DeviceType: "prs3001", Semantics: "read", Risk: "medium", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := actions.Register(deviceaction.Definition{ID: "low_read", Version: 1, Name: "low read", DeviceType: "prs3001", Semantics: "read", Risk: "low", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{2}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, actions)
	service.SetDispatchEnabled(true)

	now := time.Now().UTC()
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{ID: 42, Username: "conf-admin", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return service, &edge, &user
}

func confInput(edge uint, actionID string) ConfirmationInput {
	return ConfirmationInput{EdgeDeviceID: edge, ActorUserID: 42, ActionID: actionID, Params: json.RawMessage(`{}`), Reason: "test reason", SourceIP: "127.0.0.1"}
}

// --- confirmationRequired ---

func TestConfirmationRequired(t *testing.T) {
	tests := []struct {
		risk string
		want bool
	}{
		{"low", false},
		{"", false},
		{"medium", true},
		{"high", true},
		{"critical", true},
		{"MEDIUM", false}, // case-sensitive
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := confirmationRequired(tt.risk); got != tt.want {
			t.Errorf("confirmationRequired(%q) = %v, want %v", tt.risk, got, tt.want)
		}
	}
}

// --- confirmationHash ---

func TestConfirmationHashDeterministic(t *testing.T) {
	h1 := confirmationHash("test-token-abc")
	h2 := confirmationHash("test-token-abc")
	if h1 != h2 {
		t.Fatalf("same token produced different hashes: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash length = %d, want 64 (SHA-256 hex)", len(h1))
	}
	h3 := confirmationHash("different-token")
	if h1 == h3 {
		t.Fatal("different tokens produced same hash")
	}
}

// --- IssueConfirmation input validation ---

func TestIssueConfirmationInputValidation(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		mutate  func(*ConfirmationInput)
		wantErr error
	}{
		{"zero edge", func(in *ConfirmationInput) { in.EdgeDeviceID = 0 }, ErrConfirmationRequired},
		{"zero actor", func(in *ConfirmationInput) { in.ActorUserID = 0 }, ErrConfirmationRequired},
		{"empty action", func(in *ConfirmationInput) { in.ActionID = "" }, ErrConfirmationRequired},
		{"whitespace action", func(in *ConfirmationInput) { in.ActionID = "  " }, ErrConfirmationRequired},
		{"empty reason", func(in *ConfirmationInput) { in.Reason = "" }, ErrConfirmationRequired},
		{"whitespace reason", func(in *ConfirmationInput) { in.Reason = "   " }, ErrConfirmationRequired},
		{"reason too long", func(in *ConfirmationInput) { in.Reason = strings.Repeat("x", 513) }, ErrConfirmationRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := confInput(edge.ID, "medium_read")
			tt.mutate(&in)
			_, err := s.IssueConfirmation(ctx, in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

// --- IssueConfirmation: low risk action does not need confirmation ---

func TestIssueConfirmationNotNeededForLowRisk(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "low_read"))
	if !errors.Is(err, ErrConfirmationNotNeeded) {
		t.Fatalf("err=%v, want ErrConfirmationNotNeeded", err)
	}
}

// --- IssueConfirmation: dispatch disabled ---

func TestIssueConfirmationDispatchDisabled(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	s.SetDispatchEnabled(false)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v, want ErrActionUnavailable", err)
	}
}

// --- IssueConfirmation: edge device not found ---

func TestIssueConfirmationEdgeNotFound(t *testing.T) {
	s, _, _ := setupConfirmationService(t)
	_, err := s.IssueConfirmation(context.Background(), confInput(99999, "medium_read"))
	if err == nil {
		t.Fatal("expected error for nonexistent edge device")
	}
}

// --- IssueConfirmation: edge disabled ---

func TestIssueConfirmationEdgeDisabled(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	s.db.Model(&models.EdgeDevice{}).Where("id = ?", edge.ID).Update("enabled", false)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v, want ErrActionUnavailable", err)
	}
}

// --- IssueConfirmation: node offline ---

func TestIssueConfirmationNodeOffline(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("status", "offline")
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v, want ErrActionUnavailable", err)
	}
}

// --- IssueConfirmation: action not registered ---

func TestIssueConfirmationActionNotFound(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "nonexistent_action"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v, want ErrActionUnavailable", err)
	}
}

// --- IssueConfirmation: user not found ---

func TestIssueConfirmationUserNotFound(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	in := confInput(edge.ID, "medium_read")
	in.ActorUserID = 99999
	_, err := s.IssueConfirmation(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

// --- IssueConfirmation: user disabled ---

func TestIssueConfirmationUserDisabled(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("enabled", false)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("err=%v, want ErrRecentAuthRequired", err)
	}
}

// --- IssueConfirmation: non-admin subject key ---

func TestIssueConfirmationNonAdminSubject(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	otherKey := "operator"
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("subject_key", &otherKey)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("err=%v, want ErrRecentAuthRequired", err)
	}
}

// --- IssueConfirmation: stale login ---

func TestIssueConfirmationStaleLogin(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	stale := time.Now().UTC().Add(-recentAuthenticationWindow - time.Minute)
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("last_login_at", &stale)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("err=%v, want ErrRecentAuthRequired", err)
	}
}

// --- IssueConfirmation: nil login ---

func TestIssueConfirmationNilLogin(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("last_login_at", nil)
	_, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("err=%v, want ErrRecentAuthRequired", err)
	}
}

// --- IssueConfirmation: success + token properties ---

func TestIssueConfirmationSuccess(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	if grant.Token == "" {
		t.Fatal("empty token")
	}
	if grant.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatal("grant already expired")
	}
	if grant.ExpiresAt.After(time.Now().UTC().Add(confirmationLifetime + time.Second)) {
		t.Fatalf("expiry too far in future: %v", grant.ExpiresAt)
	}
	// Verify persisted record
	var conf models.CommandConfirmation
	if err := s.db.First(&conf, "token_hash = ?", confirmationHash(grant.Token)).Error; err != nil {
		t.Fatal(err)
	}
	if conf.ConsumedAt != nil {
		t.Fatal("confirmation should not be consumed yet")
	}
	if conf.ActorUserID != 42 || conf.EdgeDeviceID != edge.ID || conf.ActionID != "medium_read" {
		t.Fatalf("confirmation fields mismatch: %+v", conf)
	}
}

// --- IssueConfirmation: expiry clamped to login window ---

func TestIssueConfirmationExpiryClampedToLoginWindow(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	// Login 8 minutes ago → window expires in 2 minutes, before the 5-minute token lifetime
	loginAt := time.Now().UTC().Add(-8 * time.Minute)
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("last_login_at", &loginAt)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	expectedExpiry := loginAt.Add(recentAuthenticationWindow)
	if grant.ExpiresAt.After(expectedExpiry.Add(time.Second)) {
		t.Fatalf("expiry %v not clamped to login window %v", grant.ExpiresAt, expectedExpiry)
	}
}

// --- consumeConfirmation: empty token ---

func TestConsumeConfirmationEmptyToken(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	def, _ := s.actions.Get("prs3001", "medium_read")
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, "", 42, edge.ID, def, "somehash")
	})
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("err=%v, want ErrConfirmationRequired", err)
	}
}

// --- consumeConfirmation: wrong token ---

func TestConsumeConfirmationWrongToken(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	def, _ := s.actions.Get("prs3001", "medium_read")
	params, _ := deviceaction.CanonicalizeParams(def.InputSchema, json.RawMessage(`{}`))
	requestHash := fmt.Sprintf("%x", sha256sum(params))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, "wrong-token-"+grant.Token, 42, edge.ID, def, requestHash)
	})
	if !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("err=%v, want ErrConfirmationInvalid", err)
	}
}

// --- consumeConfirmation: double consume (replay) ---

func TestConsumeConfirmationReplay(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	def, _ := s.actions.Get("prs3001", "medium_read")
	params, _ := deviceaction.CanonicalizeParams(def.InputSchema, json.RawMessage(`{}`))
	requestHash := fmt.Sprintf("%x", sha256sum(params))

	// First consume succeeds
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, grant.Token, 42, edge.ID, def, requestHash)
	})
	if err != nil {
		t.Fatalf("first consume err=%v", err)
	}
	// Second consume fails
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, grant.Token, 42, edge.ID, def, requestHash)
	})
	if !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("replay consume err=%v, want ErrConfirmationInvalid", err)
	}
}

// --- consumeConfirmation: stale login at consume time ---

func TestConsumeConfirmationStaleLoginAtConsume(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	// Make login stale after issuing
	stale := time.Now().UTC().Add(-recentAuthenticationWindow - time.Minute)
	s.db.Model(&models.User{}).Where("id = ?", 42).Update("last_login_at", &stale)

	def, _ := s.actions.Get("prs3001", "medium_read")
	params, _ := deviceaction.CanonicalizeParams(def.InputSchema, json.RawMessage(`{}`))
	requestHash := fmt.Sprintf("%x", sha256sum(params))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, grant.Token, 42, edge.ID, def, requestHash)
	})
	if !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("err=%v, want ErrRecentAuthRequired", err)
	}
}

// --- consumeConfirmation: wrong actor ---

func TestConsumeConfirmationWrongActor(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	grant, err := s.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatal(err)
	}
	// Create a second user (non-admin subject key to avoid UNIQUE constraint)
	now := time.Now().UTC()
	if err := s.db.Create(&models.User{ID: 43, Username: "other-user", PasswordHash: "hash", Enabled: true, SessionVersion: 1, LastLoginAt: &now}).Error; err != nil {
		t.Fatal(err)
	}

	def, _ := s.actions.Get("prs3001", "medium_read")
	params, _ := deviceaction.CanonicalizeParams(def.InputSchema, json.RawMessage(`{}`))
	requestHash := fmt.Sprintf("%x", sha256sum(params))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.consumeConfirmation(tx, grant.Token, 43, edge.ID, def, requestHash)
	})
	// Wrong actor: either user not found or confirmation mismatch — both are rejections
	if err == nil {
		t.Fatal("expected error for wrong actor consuming confirmation")
	}
	if !errors.Is(err, ErrConfirmationInvalid) && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("err=%v, want ErrConfirmationInvalid or record not found", err)
	}
}

// --- IssueConfirmation: rate limiting ---

func TestIssueConfirmationRateLimit(t *testing.T) {
	s, edge, _ := setupConfirmationService(t)
	ctx := context.Background()
	for i := 0; i < maxConfirmationsPerWindow; i++ {
		in := confInput(edge.ID, "medium_read")
		in.Reason = fmt.Sprintf("rate test %d", i)
		if _, err := s.IssueConfirmation(ctx, in); err != nil {
			t.Fatalf("confirmation %d err=%v", i, err)
		}
	}
	in := confInput(edge.ID, "medium_read")
	in.Reason = "one too many"
	_, err := s.IssueConfirmation(ctx, in)
	if !errors.Is(err, ErrConfirmationRateLimited) {
		t.Fatalf("err=%v, want ErrConfirmationRateLimited", err)
	}
}

// helper: sha256 sum matching confirmation.go's digest logic
func sha256sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
