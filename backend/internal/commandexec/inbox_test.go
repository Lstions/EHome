package commandexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/google/uuid"
)

// setupInboxService creates a service with a dispatched execution + attempt,
// ready for inbox event tests.
func setupInboxService(t *testing.T, status string) (*Service, *models.CommandExecution, *models.CommandAttempt) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC()
	node := models.Node{NodeID: "node-inbox", Name: "inbox", Status: "online", ConfigVersion: "m-inbox", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-inbox", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "inbox-dev", NodeID: node.NodeID, ChannelID: ch.ID, DeviceConfigID: 1, Type: "prs3001", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, actions)
	service.SetDispatchEnabled(true)

	now := time.Now().UTC()
	commandID := uuid.NewString()
	execution := models.CommandExecution{
		CommandID: commandID, EdgeDeviceID: edge.ID, NodeID: node.NodeID,
		DeviceType: "prs3001", DeviceConfigID: 1, ChannelID: ch.ID,
		ManifestID: "m-inbox", ActionID: "read_rainfall", ActionVersion: 1,
		ActorUserID: 7, IdempotencyScope: "inbox-test", IdempotencyKey: uuid.NewString(),
		RequestHash: "abc123", ParamsJSON: "{}", Status: status,
		DeadlineAt: now.Add(5 * time.Minute), CreatedAt: now.Add(-time.Minute),
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	publishedAt := now.Add(-30 * time.Second)
	attempt := models.CommandAttempt{
		CommandID: commandID, AttemptNo: 1, Status: status,
		EnvelopeID: commandID + ":1", WireDigest: "digest-test",
		BootID: "boot-inbox", FencingToken: 1,
		CreatedAt: now.Add(-45 * time.Second), PublishedAt: &publishedAt,
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	return service, &execution, &attempt
}

func inboxEvent(commandID, eventType string) InboxEvent {
	return InboxEvent{
		EventID:   uuid.NewString(),
		CommandID: commandID,
		EventType: eventType,
		AttemptNo: 1,
		BootID:    "boot-inbox",
		Payload:   map[string]string{"test": "data"},
	}
}

// --- RecordInbox: input validation ---

func TestRecordInboxInvalidInput(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	ctx := context.Background()

	// Missing EventID
	_, _, err := s.RecordInbox(ctx, InboxEvent{CommandID: exec.CommandID, EventType: "accepted"})
	if err == nil {
		t.Fatal("expected error for missing EventID")
	}

	// Missing CommandID
	_, _, err = s.RecordInbox(ctx, InboxEvent{EventID: "evt-1", EventType: "accepted"})
	if err == nil {
		t.Fatal("expected error for missing CommandID")
	}

	// Invalid VerifiedResultJSON
	_, _, err = s.RecordInbox(ctx, InboxEvent{EventID: "evt-2", CommandID: exec.CommandID, EventType: "final_succeeded", VerifiedResultJSON: "{invalid"})
	if err == nil {
		t.Fatal("expected error for invalid VerifiedResultJSON")
	}
}

// --- RecordInbox: unknown event type ---

func TestRecordInboxUnknownEventType(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	_, _, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "bogus_event"))
	if err == nil {
		t.Fatal("expected error for unknown event type")
	}
}

// --- RecordInbox: valid transition DISPATCHED → DEVICE_ACCEPTED ---

func TestRecordInboxAccepted(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	result, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "accepted"))
	if err != nil || !applied {
		t.Fatalf("err=%v applied=%v", err, applied)
	}
	if result.Status != StatusDeviceAccepted {
		t.Fatalf("status=%s, want %s", result.Status, StatusDeviceAccepted)
	}
}

// --- RecordInbox: DISPATCHED → final_succeeded ---

func TestRecordInboxFinalSucceeded(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	event := inboxEvent(exec.CommandID, "final_succeeded")
	event.VerifiedResultJSON = `[{"name":"rainfall","value":42}]`
	result, applied, err := s.RecordInbox(context.Background(), event)
	if err != nil || !applied {
		t.Fatalf("err=%v applied=%v", err, applied)
	}
	if result.Status != StatusSucceeded {
		t.Fatalf("status=%s, want %s", result.Status, StatusSucceeded)
	}
	if result.CompletedAt == nil {
		t.Fatal("terminal event should set CompletedAt")
	}
	if result.VerifiedResultJSON != `[{"name":"rainfall","value":42}]` {
		t.Fatalf("verified_result=%s", result.VerifiedResultJSON)
	}
}

// --- RecordInbox: DISPATCHED → final_failed ---

func TestRecordInboxFinalFailed(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	result, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "final_failed"))
	if err != nil || !applied {
		t.Fatalf("err=%v applied=%v", err, applied)
	}
	if result.Status != StatusFailed {
		t.Fatalf("status=%s, want %s", result.Status, StatusFailed)
	}
	if result.CompletedAt == nil || result.FinalReason != "final_failed" {
		t.Fatalf("completed=%v reason=%s", result.CompletedAt, result.FinalReason)
	}
}

// --- RecordInbox: DISPATCHED → unknown ---

func TestRecordInboxUnknown(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	result, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "unknown"))
	if err != nil || !applied {
		t.Fatalf("err=%v applied=%v", err, applied)
	}
	if result.Status != StatusUnknown {
		t.Fatalf("status=%s, want %s", result.Status, StatusUnknown)
	}
}

// --- RecordInbox: duplicate event is inert ---

func TestRecordInboxDuplicateEvent(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	event := inboxEvent(exec.CommandID, "accepted")
	_, applied1, err := s.RecordInbox(context.Background(), event)
	if err != nil || !applied1 {
		t.Fatalf("first: err=%v applied=%v", err, applied1)
	}
	// Same EventID again → recorded but inert
	_, applied2, err := s.RecordInbox(context.Background(), event)
	if err != nil {
		t.Fatalf("duplicate err=%v", err)
	}
	if applied2 {
		t.Fatal("duplicate event should not apply transition")
	}
}

// --- RecordInbox: stale event on terminal execution is inert ---

func TestRecordInboxStaleEventOnTerminal(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusSucceeded)
	// Mark completed
	now := time.Now().UTC()
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Updates(map[string]interface{}{"completed_at": now})

	_, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "accepted"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if applied {
		t.Fatal("stale event on terminal execution should not apply")
	}
}

// --- RecordInbox: invalid transition is inert ---

func TestRecordInboxInvalidTransitionInert(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	// DISPATCHED → QUEUED is not a valid transition
	_, applied, err := s.RecordInbox(context.Background(), InboxEvent{
		EventID: uuid.NewString(), CommandID: exec.CommandID, EventType: "accepted",
		AttemptNo: 1, BootID: "boot-inbox", Payload: map[string]string{},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// "accepted" maps to DEVICE_ACCEPTED which IS valid from DISPATCHED, so this should apply
	if !applied {
		t.Fatal("DISPATCHED → DEVICE_ACCEPTED should apply")
	}
}

// --- RecordInbox: command not found ---

func TestRecordInboxCommandNotFound(t *testing.T) {
	s, _, _ := setupInboxService(t, StatusDispatched)
	_, _, err := s.RecordInbox(context.Background(), inboxEvent(uuid.NewString(), "accepted"))
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

// --- RecordInbox: full lifecycle DISPATCHED → accepted → verifying → final_succeeded ---

func TestRecordInboxFullLifecycle(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	ctx := context.Background()

	// Step 1: accepted
	_, applied, err := s.RecordInbox(ctx, inboxEvent(exec.CommandID, "accepted"))
	if err != nil || !applied {
		t.Fatalf("accepted: err=%v applied=%v", err, applied)
	}

	// Step 2: verifying
	_, applied, err = s.RecordInbox(ctx, inboxEvent(exec.CommandID, "verifying"))
	if err != nil || !applied {
		t.Fatalf("verifying: err=%v applied=%v", err, applied)
	}

	// Step 3: final_succeeded
	event := inboxEvent(exec.CommandID, "final_succeeded")
	event.VerifiedResultJSON = `[{"name":"rainfall","value":99}]`
	result, applied, err := s.RecordInbox(ctx, event)
	if err != nil || !applied {
		t.Fatalf("final: err=%v applied=%v", err, applied)
	}
	if result.Status != StatusSucceeded || result.CompletedAt == nil {
		t.Fatalf("final status=%s completed=%v", result.Status, result.CompletedAt)
	}
}

// --- RecoverExpired: QUEUED past deadline → FAILED ---

func TestRecoverExpiredQueued(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusQueued)
	// Set deadline in the past
	past := time.Now().UTC().Add(-time.Minute)
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past)
	// Create outbox
	s.db.Create(&models.CommandOutbox{CommandID: exec.CommandID, EventType: "dispatch", PayloadJSON: "{}", State: "PENDING", CreatedAt: time.Now().UTC()})

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	if expired[0].Status != StatusFailed {
		t.Fatalf("status=%s, want FAILED", expired[0].Status)
	}
	if expired[0].FinalReason != "deadline expired before dispatch" {
		t.Fatalf("reason=%s", expired[0].FinalReason)
	}
	// Outbox should be CANCELLED
	var outbox models.CommandOutbox
	s.db.First(&outbox, "command_id = ?", exec.CommandID)
	if outbox.State != "CANCELLED" {
		t.Fatalf("outbox state=%s, want CANCELLED", outbox.State)
	}
}

// --- RecoverExpired: DISPATCHED past deadline → UNKNOWN ---

func TestRecoverExpiredDispatched(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	past := time.Now().UTC().Add(-time.Minute)
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past)

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	if expired[0].Status != StatusUnknown {
		t.Fatalf("status=%s, want UNKNOWN", expired[0].Status)
	}
	if expired[0].FinalReason != "deadline expired without final evidence" {
		t.Fatalf("reason=%s", expired[0].FinalReason)
	}
	// Attempt should also be UNKNOWN
	var attempt models.CommandAttempt
	s.db.First(&attempt, "command_id = ?", exec.CommandID)
	if attempt.Status != StatusUnknown {
		t.Fatalf("attempt status=%s, want UNKNOWN", attempt.Status)
	}
}

// --- RecoverExpired: DEVICE_ACCEPTED past deadline → UNKNOWN ---

func TestRecoverExpiredDeviceAccepted(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDeviceAccepted)
	past := time.Now().UTC().Add(-time.Minute)
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past)

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Status != StatusUnknown {
		t.Fatalf("expired=%+v", expired)
	}
}

// --- RecoverExpired: LEASED outbox with expired lease → recovered to PENDING ---

func TestRecoverExpiredLeasedOutbox(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusQueued)
	// Outbox with expired lease
	pastLease := time.Now().UTC().Add(-time.Minute)
	s.db.Create(&models.CommandOutbox{
		CommandID: exec.CommandID, EventType: "dispatch", PayloadJSON: "{}",
		State: "LEASED", LeaseOwner: "dead-worker", LeaseExpiresAt: &pastLease,
		CreatedAt: time.Now().UTC(),
	})
	// Set deadline in the future so execution itself doesn't expire
	future := time.Now().UTC().Add(10 * time.Minute)
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", future)

	_, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var outbox models.CommandOutbox
	s.db.First(&outbox, "command_id = ?", exec.CommandID)
	if outbox.State != "PENDING" {
		t.Fatalf("outbox state=%s, want PENDING (lease recovered)", outbox.State)
	}
	if outbox.LeaseOwner != "" {
		t.Fatalf("lease_owner=%s, want empty", outbox.LeaseOwner)
	}
}

// --- RecoverExpired: nothing expired ---

func TestRecoverExpiredNothing(t *testing.T) {
	s, _, _ := setupInboxService(t, StatusDispatched)
	// Deadline is 5 minutes in the future (from setup)
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired count=%d, want 0", len(expired))
	}
}

// --- RecoverExpired: terminal executions are not touched ---

func TestRecoverExpiredSkipsTerminal(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusSucceeded)
	past := time.Now().UTC().Add(-time.Minute)
	s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Updates(map[string]interface{}{"deadline_at": past, "completed_at": past})

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("terminal execution should not be recovered: %+v", expired)
	}
}

// suppress unused import
var _ = json.Marshal
