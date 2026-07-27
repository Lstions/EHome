package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

func setupService(t *testing.T) (*Service, *models.EdgeDevice) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC()
	node := models.Node{NodeID: "node-1", Name: "test", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-test", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
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
	edge := models.EdgeDevice{Name: "rain", NodeID: node.NodeID, ChannelID: ch.ID, DeviceConfigID: 1, Type: "prs3001", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, actions)
	service.SetDispatchEnabled(true)
	return service, &edge
}

func catalogAction(items []CatalogItem, actionID string) *CatalogItem {
	for i := range items {
		if items[i].Definition.ID == actionID {
			return &items[i]
		}
	}
	return nil
}

func createInput(edge uint, key string) CreateInput {
	return CreateInput{EdgeDeviceID: edge, ActorUserID: 7, ActionID: "read_rainfall", Params: json.RawMessage(`{}`), IdempotencyKey: key, SourceIP: "127.0.0.1"}
}

func TestCreateIsTransactionalAndIdempotent(t *testing.T) {
	s, edge := setupService(t)
	first, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-0001"))
	if err != nil || replayed {
		t.Fatalf("first create err=%v replayed=%v", err, replayed)
	}
	second, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-0001"))
	if err != nil || !replayed || second.CommandID != first.CommandID {
		t.Fatalf("replay err=%v replayed=%v first=%s second=%s", err, replayed, first.CommandID, second.CommandID)
	}
	var outbox models.CommandOutbox
	if err := s.db.Where("command_id = ?", first.CommandID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.State != "PENDING" {
		t.Fatalf("outbox state=%s", outbox.State)
	}
	if first.CommandEngineRevision != 1 {
		t.Fatalf("execution did not freeze command engine revision: %d", first.CommandEngineRevision)
	}
	var audit models.SecurityAuditEvent
	if err := s.db.Where("request_id = ?", first.CommandID).First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.EventName != "device_action.created" {
		t.Fatalf("audit=%s", audit.EventName)
	}
}

func TestCreateReplaysExistingCommandWhenCapabilitiesBecomeStale(t *testing.T) {
	s, edge := setupService(t)
	first, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-stale-replay-0001"))
	if err != nil || replayed {
		t.Fatalf("first create err=%v replayed=%v", err, replayed)
	}

	now := time.Now().UTC()
	stale := now.Add(-MaxCapabilityAge - time.Second)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("resource_reported_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }

	second, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-stale-replay-0001"))
	if err != nil || !replayed || second.CommandID != first.CommandID {
		t.Fatalf("stale replay err=%v replayed=%v first=%s second=%s", err, replayed, first.CommandID, second.CommandID)
	}
	var count int64
	if err := s.db.Model(&models.CommandOutbox{}).Where("command_id = ?", first.CommandID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("outbox count=%d err=%v", count, err)
	}
}

func TestCreateReplaysExistingCommandWhenRolloutIsDisabled(t *testing.T) {
	s, edge := setupService(t)
	first, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-disabled-replay-0001"))
	if err != nil || replayed {
		t.Fatalf("first create err=%v replayed=%v", err, replayed)
	}
	if err := s.actions.SetEnabled(edge.Type, "read_rainfall", false); err != nil {
		t.Fatal(err)
	}

	second, replayed, err := s.Create(context.Background(), createInput(edge.ID, "request-disabled-replay-0001"))
	if err != nil || !replayed || second.CommandID != first.CommandID {
		t.Fatalf("disabled replay err=%v replayed=%v first=%s second=%s", err, replayed, first.CommandID, second.CommandID)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-disabled-new-key-0001")); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("new disabled action error=%v", err)
	}
}

func TestCreateRejectsCollisionAndParams(t *testing.T) {
	s, edge := setupService(t)
	in := createInput(edge.ID, "request-0002")
	in.Params = json.RawMessage(`{"unexpected":1}`)
	if _, _, err := s.Create(context.Background(), in); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("want invalid params, got %v", err)
	}
	now := time.Now().UTC()
	stored := models.CommandExecution{CommandID: "collision-command", EdgeDeviceID: edge.ID, NodeID: edge.NodeID, ActionID: "read_rainfall", ActionVersion: 1, ActorUserID: 7,
		IdempotencyScope: fmt.Sprintf("user:%d:edge:%d:action:%s:v:%d", 7, edge.ID, "read_rainfall", 1), IdempotencyKey: "request-0002", RequestHash: "different", ParamsJSON: "{}", Status: StatusQueued, DeadlineAt: now.Add(time.Minute), CreatedAt: now}
	if err := s.db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-0002")); !errors.Is(err, ErrIdempotencyCollision) {
		t.Fatalf("want collision, got %v", err)
	}
}

func TestControlV2DisabledIsUnavailable(t *testing.T) {
	s, edge := setupService(t)
	s.SetDispatchEnabled(false)
	items, err := s.Catalog(context.Background(), edge.ID)
	item := catalogAction(items, "read_rainfall")
	if err != nil || item == nil || item.Available {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-closed-0001")); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("disabled create error=%v", err)
	}
}

func TestCatalogIdentifiesStaleCapabilityForRefresh(t *testing.T) {
	s, edge := setupService(t)
	now := time.Now().UTC()
	stale := now.Add(-MaxCapabilityAge - time.Second)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("resource_reported_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	items, err := s.Catalog(context.Background(), edge.ID)
	item := catalogAction(items, "read_rainfall")
	if err != nil || item == nil {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	if item.Available || item.ReasonCode != "capability_stale" {
		t.Fatalf("expected stale capability catalog item, got %+v", item)
	}
}

func TestHighRiskConfirmationIsBoundAndSingleUse(t *testing.T) {
	s, edge := setupService(t)
	now := time.Now().UTC()
	subjectKey := models.SystemAdminSubjectKey
	if err := s.db.Create(&models.User{ID: 7, Username: "operator", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewRegistry()
	if err := actions.Register(deviceaction.Definition{ID: "high_read", Version: 1, Name: "high read", DeviceType: edge.Type, Semantics: "read", Risk: "high", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1, 3, 0, 0, 0, 2, 0xc4, 0x0b}, ReadSize: 9, RXTimeoutMS: 1000}}); err != nil {
		t.Fatal(err)
	}
	s.actions = actions
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: strings.Repeat("中", 513)}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("513-character reason error=%v", err)
	}
	grant, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: strings.Repeat("中", 512), SourceIP: "127.0.0.1"})
	if err != nil || grant.Token == "" || !grant.ExpiresAt.After(now) {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	input := CreateInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), IdempotencyKey: "high-confirmation-0001", ConfirmationToken: grant.Token, Reason: "controlled test", SourceIP: "127.0.0.1"}
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("high-risk action crossed the current engine gate: %v", err)
	}
	input.IdempotencyKey = "high-confirmation-0002"
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("high-risk action should remain unavailable: %v", err)
	}
	var confirmation models.CommandConfirmation
	if err := s.db.First(&confirmation, "token_hash = ?", confirmationHash(grant.Token)).Error; err != nil {
		t.Fatalf("confirmation grant was not persisted: %+v err=%v", confirmation, err)
	}
	if confirmation.ConsumedAt != nil {
		t.Fatal("unavailable high-risk action must not consume its confirmation token")
	}
	for index := 0; index < maxConfirmationsPerWindow-1; index++ {
		if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: fmt.Sprintf("controlled test %d", index)}); err != nil {
			t.Fatalf("confirmation %d err=%v", index, err)
		}
	}
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: "rate limited"}); !errors.Is(err, ErrConfirmationRateLimited) {
		t.Fatalf("rate limit error=%v", err)
	}
}

func TestDispatcherCancelsStaleOutboxWithoutPublishing(t *testing.T) {
	s, edge := setupService(t)
	execution, _, err := s.Create(context.Background(), createInput(edge.ID, "stale-outbox-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", execution.CommandID).Update("status", StatusCancelled).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeTransport{}
	processed, err := NewDispatcher(s.db, fake, "test-worker").ProcessOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(fake.attempts) != 0 {
		t.Fatalf("stale outbox was physically dispatched: %+v", fake.attempts)
	}
	var outbox models.CommandOutbox
	if err := s.db.First(&outbox, "command_id = ?", execution.CommandID).Error; err != nil || outbox.State != "CANCELLED" {
		t.Fatalf("outbox=%+v err=%v", outbox, err)
	}
}

func TestMediumRiskRequiresConfirmation(t *testing.T) {
	s, edge := setupService(t)
	now := time.Now().UTC()
	subjectKey := models.SystemAdminSubjectKey
	if err := s.db.Create(&models.User{ID: 7, Username: "medium-operator", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewRegistry()
	if err := actions.Register(deviceaction.Definition{ID: "medium_read", Version: 1, Name: "medium read", DeviceType: edge.Type, Semantics: "read", Risk: "medium", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	s.actions = actions
	input := CreateInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "medium_read", Params: json.RawMessage(`{}`), IdempotencyKey: "medium-confirmation-0001", SourceIP: "127.0.0.1"}
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("medium-risk action should be unavailable before confirmation: %v", err)
	}
	grant, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "medium_read", Params: json.RawMessage(`{}`), Reason: "recoverable test", SourceIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	input.ConfirmationToken, input.Reason = grant.Token, "recoverable test"
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("medium-risk action crossed the current engine gate: %v", err)
	}
}

func TestConfirmationRejectsMissingRuntimeActionChannel(t *testing.T) {
	s, edge := setupService(t)
	now := time.Now().UTC()
	subjectKey := models.SystemAdminSubjectKey
	if err := s.db.Create(&models.User{ID: 7, Username: "runtime-channel-operator", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewRegistry()
	if err := actions.Register(deviceaction.Definition{ID: "medium_read", Version: 1, Name: "medium read", DeviceType: edge.Type, Semantics: "read", Risk: "medium", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	s.actions = actions
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("hardware_info", `{"channels":[]}`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "medium_read", Params: json.RawMessage(`{}`), Reason: "must not mint token", SourceIP: "127.0.0.1"}); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("confirmation for missing runtime channel error=%v", err)
	}
}

func TestConfirmationRequiresRecentAuthentication(t *testing.T) {
	s, edge := setupService(t)
	oldLogin := time.Now().UTC().Add(-recentAuthenticationWindow - time.Second)
	subjectKey := models.SystemAdminSubjectKey
	if err := s.db.Create(&models.User{ID: 7, Username: "operator-old", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &oldLogin}).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewRegistry()
	if err := actions.Register(deviceaction.Definition{ID: "high_read", Version: 1, Name: "high read", DeviceType: edge.Type, Semantics: "read", Risk: "high", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	s.actions = actions
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: "controlled test"}); !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("old login confirmation error=%v", err)
	}
}

func TestCatalogAndCreateRejectUnavailableActionChannel(t *testing.T) {
	s, edge := setupService(t)
	if err := s.db.Model(&models.Channel{}).Where("id = ?", edge.ChannelID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	items, err := s.Catalog(context.Background(), edge.ID)
	item := catalogAction(items, "read_rainfall")
	if err != nil || item == nil || item.Available || item.Reason != "action channel is unavailable" {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-channel-closed")); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("disabled channel create error=%v", err)
	}
}

func TestCatalogAndCreateRejectMissingRuntimeChannel(t *testing.T) {
	s, edge := setupService(t)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("hardware_info", `{"channels":[]}`).Error; err != nil {
		t.Fatal(err)
	}
	items, err := s.Catalog(context.Background(), edge.ID)
	item := catalogAction(items, "read_rainfall")
	if err != nil || item == nil || item.Available || item.Reason != "action channel is unavailable" {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-runtime-missing")); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("missing runtime channel create error=%v", err)
	}
}

func TestVerifyFinalUsesFrozenActionVersionAndCanonicalParams(t *testing.T) {
	s, edge := setupService(t)
	execution, _, err := s.Create(context.Background(), createInput(edge.ID, "verify-final-0001"))
	if err != nil {
		t.Fatal(err)
	}
	// Valid PRS-3001 FC03 response.  The result comes through the action
	// verifier boundary rather than nodemgr's former generic parser path.
	result, err := s.VerifyFinal(context.Background(), *execution, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0})
	if err != nil || len(result) == 0 || result[0].Name != "rainfall" {
		t.Fatalf("verification result=%+v err=%v", result, err)
	}
	stale := *execution
	stale.ActionVersion++
	if _, err := s.VerifyFinal(context.Background(), stale, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0}); err == nil {
		t.Fatal("stale action version was accepted for final verification")
	}
	invalid := *execution
	invalid.ParamsJSON = `{"unexpected":true}`
	if _, err := s.VerifyFinal(context.Background(), invalid, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0}); err == nil {
		t.Fatal("non-canonical persisted params were accepted for final verification")
	}
}

type fakeTransport struct {
	attempts []models.CommandAttempt
	err      error
}

func (f *fakeTransport) Dispatch(_ context.Context, _ models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	f.attempts = append(f.attempts, attempt)
	return DispatchResult{BootID: "boot-test", PublishedAt: time.Now().UTC()}, f.err
}

type fencingLossTransport struct{}

func (fencingLossTransport) Dispatch(context.Context, models.CommandExecution, models.CommandAttempt) (DispatchResult, error) {
	return DispatchResult{}, errors.New("transaction-aware dispatch required")
}

func (fencingLossTransport) DispatchInTransaction(_ context.Context, tx *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if err := tx.Model(&models.CommandOutbox{}).Where("command_id = ?", execution.CommandID).
		Update("fencing_token", attempt.FencingToken+1).Error; err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{BootID: "boot-test", PublishedAt: time.Now().UTC()}, nil
}

type blockingTransport struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (b *blockingTransport) Dispatch(_ context.Context, _ models.CommandExecution, _ models.CommandAttempt) (DispatchResult, error) {
	b.enteredOnce.Do(func() { close(b.entered) })
	<-b.release
	return DispatchResult{BootID: "boot-test", PublishedAt: time.Now().UTC()}, nil
}

func TestDispatcherOwnerIsUniqueAndBounded(t *testing.T) {
	first := NewDispatcherOwner("server")
	second := NewDispatcherOwner("server")
	if first == second || !strings.HasPrefix(first, "server:") || !strings.HasPrefix(second, "server:") {
		t.Fatalf("dispatcher owners are not attributable and unique: %q %q", first, second)
	}
	if len(first) > 96 || len(second) > 96 {
		t.Fatalf("dispatcher owner exceeds storage bound: %d %d", len(first), len(second))
	}
	longFirst := NewDispatcherOwner(strings.Repeat("replica", 30))
	longSecond := NewDispatcherOwner(strings.Repeat("replica", 30))
	if len(longFirst) != 96 || len(longSecond) != 96 || longFirst == longSecond {
		t.Fatalf("bounded owner lost its unique suffix: %q %q", longFirst, longSecond)
	}
}

func TestDispatcherSerializesActiveChannelLeaseWithoutBlockingOtherChannels(t *testing.T) {
	s, edge := setupService(t)
	if !s.db.Migrator().HasIndex(&models.CommandExecution{}, "idx_command_execution_channel") {
		t.Fatal("command channel lease lookup index is missing")
	}
	first, _, err := s.Create(context.Background(), createInput(edge.ID, "channel-lease-first-0001"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Create(context.Background(), createInput(edge.ID, "channel-lease-second-0001"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	leaseExpires := now.Add(time.Minute)
	if err := s.db.Model(&models.CommandOutbox{}).Where("command_id = ?", first.CommandID).Updates(map[string]interface{}{
		"state": "LEASED", "lease_owner": "other-replica", "lease_expires_at": leaseExpires, "fencing_token": 1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	otherChannel := models.Channel{NodeID: edge.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := s.db.Create(&otherChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("hardware_info",
		fmt.Sprintf(`{"channels":[{"id":%d,"enabled":true},{"id":%d,"enabled":true}]}`, edge.ChannelID, otherChannel.ID)).Error; err != nil {
		t.Fatal(err)
	}
	otherEdge := models.EdgeDevice{Name: "other-rain", NodeID: edge.NodeID, ChannelID: otherChannel.ID, DeviceConfigID: 1, Type: edge.Type, Enabled: true, Status: "active"}
	if err := s.db.Create(&otherEdge).Error; err != nil {
		t.Fatal(err)
	}
	other, _, err := s.Create(context.Background(), createInput(otherEdge.ID, "channel-lease-other-0001"))
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeTransport{}
	dispatcher := NewDispatcher(s.db, fake, "test-replica")
	dispatcher.now = func() time.Time { return now }
	processed, err := dispatcher.ProcessOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("other channel processed=%v err=%v", processed, err)
	}
	if len(fake.attempts) != 1 || fake.attempts[0].CommandID != other.CommandID {
		t.Fatalf("blocked channel prevented independent work or was dispatched: %+v", fake.attempts)
	}
	processed, err = dispatcher.ProcessOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("same-channel sibling bypassed active lease: processed=%v err=%v", processed, err)
	}
	var secondOutbox models.CommandOutbox
	if err := s.db.First(&secondOutbox, "command_id = ?", second.CommandID).Error; err != nil || secondOutbox.State != "PENDING" {
		t.Fatalf("same-channel outbox=%+v err=%v", secondOutbox, err)
	}

	// Expired leases are recovered before selection, preserving outbox order.
	dispatcher.now = func() time.Time { return leaseExpires.Add(time.Second) }
	processed, err = dispatcher.ProcessOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("expired lease recovery processed=%v err=%v", processed, err)
	}
	if len(fake.attempts) != 2 || fake.attempts[1].CommandID != first.CommandID {
		t.Fatalf("expired older command was overtaken: %+v", fake.attempts)
	}
}

func TestDispatcherRollsBackWhenOutboxFencingIsLost(t *testing.T) {
	s, edge := setupService(t)
	execution, _, err := s.Create(context.Background(), createInput(edge.ID, "fencing-loss-0001"))
	if err != nil {
		t.Fatal(err)
	}
	processed, err := NewDispatcher(s.db, fencingLossTransport{}, "stale-replica").ProcessOnce(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "fencing lost") {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	got, getErr := s.Get(context.Background(), execution.CommandID)
	if getErr != nil || got.Status != StatusQueued {
		t.Fatalf("execution committed after fencing loss: %+v err=%v", got, getErr)
	}
	var attempts int64
	if err := s.db.Model(&models.CommandAttempt{}).Where("command_id = ?", execution.CommandID).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	var outbox models.CommandOutbox
	if err := s.db.First(&outbox, "command_id = ?", execution.CommandID).Error; err != nil || outbox.State != "LEASED" || outbox.FencingToken != 1 {
		t.Fatalf("outbox=%+v err=%v", outbox, err)
	}
}

func TestDispatcherRecoversLeaseWithoutExpiryBeforeSibling(t *testing.T) {
	s, edge := setupService(t)
	first, _, err := s.Create(context.Background(), createInput(edge.ID, "missing-expiry-first-0001"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Create(context.Background(), createInput(edge.ID, "missing-expiry-second-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.CommandOutbox{}).Where("command_id = ?", first.CommandID).Updates(map[string]interface{}{
		"state": "LEASED", "lease_owner": "broken-replica", "lease_expires_at": nil, "fencing_token": 4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeTransport{}
	processed, err := NewDispatcher(s.db, fake, "recovery-replica").ProcessOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(fake.attempts) != 1 || fake.attempts[0].CommandID != first.CommandID || fake.attempts[0].FencingToken != 5 {
		t.Fatalf("invalid lease recovery overtook the older command: %+v", fake.attempts)
	}
}

func TestPostgresDispatchersDoNotPublishSameChannelConcurrently(t *testing.T) {
	if !testutil.IsPostgres() {
		t.Skip("cross-instance row locking requires PostgreSQL")
	}
	s, edge := setupService(t)
	first, _, err := s.Create(context.Background(), createInput(edge.ID, "postgres-channel-first-0001"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Create(context.Background(), createInput(edge.ID, "postgres-channel-second-0001"))
	if err != nil {
		t.Fatal(err)
	}
	transport := &blockingTransport{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(transport.release) }) }
	t.Cleanup(release)
	type processResult struct {
		processed bool
		err       error
	}
	firstResult := make(chan processResult, 1)
	go func() {
		processed, err := NewDispatcher(s.db, transport, "postgres-replica-a").ProcessOnce(context.Background())
		firstResult <- processResult{processed: processed, err: err}
	}()
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first dispatcher did not reach transport")
	}

	otherTransport := &fakeTransport{}
	processed, err := NewDispatcher(s.db, otherTransport, "postgres-replica-b").ProcessOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("second dispatcher bypassed active channel lease: processed=%v err=%v", processed, err)
	}
	if len(otherTransport.attempts) != 0 {
		t.Fatalf("second dispatcher published concurrently: %+v", otherTransport.attempts)
	}
	release()
	select {
	case result := <-firstResult:
		if result.err != nil || !result.processed {
			t.Fatalf("first dispatcher result=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first dispatcher did not complete")
	}
	var secondOutbox models.CommandOutbox
	if err := s.db.First(&secondOutbox, "command_id = ?", second.CommandID).Error; err != nil || secondOutbox.State != "PENDING" {
		t.Fatalf("second outbox=%+v err=%v", secondOutbox, err)
	}
	var attempts int64
	if err := s.db.Model(&models.CommandAttempt{}).Where("command_id IN ?", []string{first.CommandID, second.CommandID}).Count(&attempts).Error; err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestDispatcherAndInboxAreExactlyOnceAtDomainBoundary(t *testing.T) {
	s, edge := setupService(t)
	exec, _, err := s.Create(context.Background(), createInput(edge.ID, "request-0003"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTransport{}
	d := NewDispatcher(s.db, fake, "test-worker")
	processed, err := d.ProcessOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("dispatch processed=%v err=%v", processed, err)
	}
	if len(fake.attempts) != 1 || fake.attempts[0].AttemptNo != 1 {
		t.Fatalf("attempts=%+v", fake.attempts)
	}
	got, err := s.Get(context.Background(), exec.CommandID)
	if err != nil || got.Status != StatusDispatched {
		t.Fatalf("status=%v err=%v", got, err)
	}
	got, applied, err := s.RecordInbox(context.Background(), InboxEvent{EventID: "evt-1", CommandID: exec.CommandID, AttemptNo: 1, BootID: "boot-test", EventType: "accepted", Payload: map[string]string{"kind": "accepted"}})
	if err != nil || !applied || got.Status != StatusDeviceAccepted {
		t.Fatalf("accepted status=%+v applied=%v err=%v", got, applied, err)
	}
	got, applied, err = s.RecordInbox(context.Background(), InboxEvent{EventID: "evt-2", CommandID: exec.CommandID, AttemptNo: 1, BootID: "boot-test", EventType: "final_succeeded", Payload: map[string]string{}})
	if err != nil || !applied || got.Status != StatusSucceeded {
		t.Fatalf("final status=%+v applied=%v err=%v", got, applied, err)
	}
	_, applied, err = s.RecordInbox(context.Background(), InboxEvent{EventID: "evt-2", CommandID: exec.CommandID, AttemptNo: 1, BootID: "boot-test", EventType: "final_succeeded", Payload: map[string]string{}})
	if err != nil || applied {
		t.Fatalf("duplicate applied=%v err=%v", applied, err)
	}
}

func TestStableWireDigestIgnoresOutboxLeaseFencing(t *testing.T) {
	execution := models.CommandExecution{
		CommandID:     "00112233-4455-6677-8899-aabbccddeeff",
		ActionID:      "read_rainfall",
		ActionVersion: 1,
		RequestHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	first := stableWireDigest(execution, 1)
	if first == "" || first != stableWireDigest(execution, 1) {
		t.Fatalf("a retransmission must keep the same wire digest: %q", first)
	}
	if first == stableWireDigest(execution, 2) {
		t.Fatal("a new physical attempt must get a new wire digest")
	}
	changed := execution
	changed.RequestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if first == stableWireDigest(changed, 1) {
		t.Fatal("a different canonical request must get a different wire digest")
	}
}

func TestRecoverMarksDispatchedDeadlineUnknownWithoutRedelivery(t *testing.T) {
	s, edge := setupService(t)
	exec, _, err := s.Create(context.Background(), createInput(edge.ID, "request-0004"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Updates(map[string]interface{}{"status": StatusDispatched, "deadline_at": past}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&models.CommandAttempt{CommandID: exec.CommandID, AttemptNo: 1, Status: StatusDispatched, EnvelopeID: exec.CommandID + ":1", WireDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BootID: "boot-test", CreatedAt: past}).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].CommandID != exec.CommandID || expired[0].Status != StatusUnknown {
		t.Fatalf("expired=%+v", expired)
	}
	got, err := s.Get(context.Background(), exec.CommandID)
	if err != nil || got.Status != StatusUnknown {
		t.Fatalf("status=%+v err=%v", got, err)
	}
	var attempt models.CommandAttempt
	if err := s.db.Where("command_id = ?", exec.CommandID).First(&attempt).Error; err != nil || attempt.Status != StatusUnknown || attempt.CompletedAt == nil {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
}

func TestRecoverTerminatesQueuedDeadlineWithoutDispatch(t *testing.T) {
	s, edge := setupService(t)
	exec, _, err := s.Create(context.Background(), createInput(edge.ID, "request-queued-expired"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Status != StatusFailed || expired[0].FinalReason != "deadline expired before dispatch" {
		t.Fatalf("expired=%+v", expired)
	}
	got, err := s.Get(context.Background(), exec.CommandID)
	if err != nil || got.Status != StatusFailed {
		t.Fatalf("execution=%+v err=%v", got, err)
	}
	var outbox models.CommandOutbox
	if err := s.db.Where("command_id = ?", exec.CommandID).First(&outbox).Error; err != nil || outbox.State != "CANCELLED" {
		t.Fatalf("outbox=%+v err=%v", outbox, err)
	}
}

func TestResolveUnknownAppendsAuditedIdempotentConclusion(t *testing.T) {
	s, edge := setupService(t)
	execution, _, err := s.Create(context.Background(), createInput(edge.ID, "resolve-unknown-0001"))
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", execution.CommandID).Updates(map[string]interface{}{
		"status": StatusUnknown, "completed_at": completedAt, "final_reason": "device final was not observed",
	}).Error; err != nil {
		t.Fatal(err)
	}

	input := ResolveUnknownInput{
		CommandID: execution.CommandID, ActorUserID: 7,
		Outcome: ResolutionConfirmedFailed, Reason: strings.Repeat("中", 512), SourceIP: "127.0.0.1",
	}
	resolved, replayed, err := s.ResolveUnknown(context.Background(), input)
	if err != nil || replayed {
		t.Fatalf("resolve err=%v replayed=%v", err, replayed)
	}
	if resolved.Status != StatusUnknown || resolved.ManualResolution == nil || resolved.ManualResolution.Outcome != ResolutionConfirmedFailed {
		t.Fatalf("resolved execution=%+v", resolved)
	}
	if resolved.ManualResolution.ResolvedBy != 7 || resolved.ManualResolution.Reason != input.Reason {
		t.Fatalf("resolution=%+v", resolved.ManualResolution)
	}

	loaded, err := s.Get(context.Background(), execution.CommandID)
	if err != nil || loaded.Status != StatusUnknown || loaded.ManualResolution == nil {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	var auditEvent models.SecurityAuditEvent
	if err := s.db.Where("request_id = ? AND event_name = ?", execution.CommandID, "device_action.unknown_resolved").First(&auditEvent).Error; err != nil {
		t.Fatal(err)
	}
	if auditEvent.Result != "confirmed_failed" {
		t.Fatalf("audit result=%q", auditEvent.Result)
	}

	replayedExecution, replayed, err := s.ResolveUnknown(context.Background(), input)
	if err != nil || !replayed || replayedExecution.ManualResolution == nil {
		t.Fatalf("idempotent resolve execution=%+v replayed=%v err=%v", replayedExecution, replayed, err)
	}
	conflict := input
	conflict.Outcome = ResolutionAcknowledgedUnknown
	if _, _, err := s.ResolveUnknown(context.Background(), conflict); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("conflicting resolution error=%v", err)
	}
	var resolutionCount int64
	if err := s.db.Model(&models.CommandManualResolution{}).Where("command_id = ?", execution.CommandID).Count(&resolutionCount).Error; err != nil || resolutionCount != 1 {
		t.Fatalf("resolution count=%d err=%v", resolutionCount, err)
	}
}

func TestResolveUnknownRejectsInvalidOrNonUnknownExecution(t *testing.T) {
	s, edge := setupService(t)
	execution, _, err := s.Create(context.Background(), createInput(edge.ID, "resolve-invalid-0001"))
	if err != nil {
		t.Fatal(err)
	}
	base := ResolveUnknownInput{CommandID: execution.CommandID, ActorUserID: 7, Outcome: ResolutionConfirmedSucceeded, Reason: "independent readback"}
	if _, _, err := s.ResolveUnknown(context.Background(), base); !errors.Is(err, ErrNotResolvable) {
		t.Fatalf("queued resolution error=%v", err)
	}
	for _, invalid := range []ResolveUnknownInput{
		{CommandID: execution.CommandID, ActorUserID: 7, Outcome: "SUCCEEDED", Reason: "invalid outcome"},
		{CommandID: execution.CommandID, ActorUserID: 7, Outcome: ResolutionConfirmedSucceeded, Reason: "   "},
		{CommandID: execution.CommandID, ActorUserID: 0, Outcome: ResolutionConfirmedSucceeded, Reason: "missing actor"},
		{CommandID: execution.CommandID, ActorUserID: 7, Outcome: ResolutionConfirmedSucceeded, Reason: strings.Repeat("中", 513)},
	} {
		if _, _, err := s.ResolveUnknown(context.Background(), invalid); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("invalid resolution %+v error=%v", invalid, err)
		}
	}
}
