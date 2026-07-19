package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
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
	if err != nil || len(items) != 1 || items[0].Available {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	if _, _, err := s.Create(context.Background(), createInput(edge.ID, "request-closed-0001")); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("disabled create error=%v", err)
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
	grant, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), Reason: "controlled test", SourceIP: "127.0.0.1"})
	if err != nil || grant.Token == "" || !grant.ExpiresAt.After(now) {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	input := CreateInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "high_read", Params: json.RawMessage(`{}`), IdempotencyKey: "high-confirmation-0001", ConfirmationToken: grant.Token, Reason: "controlled test", SourceIP: "127.0.0.1"}
	if execution, _, err := s.Create(context.Background(), input); err != nil || execution.Status != StatusQueued {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	input.IdempotencyKey = "high-confirmation-0002"
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("reused token error=%v", err)
	}
	var confirmation models.CommandConfirmation
	if err := s.db.First(&confirmation, "token_hash = ?", confirmationHash(grant.Token)).Error; err != nil || confirmation.ConsumedAt == nil {
		t.Fatalf("confirmation=%+v err=%v", confirmation, err)
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
	if _, _, err := s.Create(context.Background(), input); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("medium risk create without confirmation error=%v", err)
	}
	grant, err := s.IssueConfirmation(context.Background(), ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 7, ActionID: "medium_read", Params: json.RawMessage(`{}`), Reason: "recoverable test", SourceIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	input.ConfirmationToken, input.Reason = grant.Token, "recoverable test"
	if execution, _, err := s.Create(context.Background(), input); err != nil || execution.Status != StatusQueued {
		t.Fatalf("medium risk confirmed create execution=%+v err=%v", execution, err)
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
	if err != nil || len(items) != 1 || items[0].Available || items[0].Reason != "action channel is unavailable" {
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
	if err != nil || len(items) != 1 || items[0].Available || items[0].Reason != "action channel is unavailable" {
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
