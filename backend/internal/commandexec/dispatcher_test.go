package commandexec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/google/uuid"
)

// dispatchFakeTransport records dispatch calls and returns a configurable result.
type dispatchFakeTransport struct {
	calls  []models.CommandExecution
	result DispatchResult
	err    error
}

func (f *dispatchFakeTransport) Dispatch(_ context.Context, exec models.CommandExecution, _ models.CommandAttempt) (DispatchResult, error) {
	f.calls = append(f.calls, exec)
	if f.err != nil {
		return DispatchResult{}, f.err
	}
	return f.result, nil
}

// setupDispatcher creates a dispatcher with a QUEUED execution + PENDING outbox.
func setupDispatcher(t *testing.T, transport Transport) (*Dispatcher, *models.CommandExecution, *models.CommandOutbox) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC()
	node := models.Node{NodeID: "node-disp", Name: "disp", Status: "online", BootID: "boot-disp", ResourceReportedAt: &reported}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	commandID := uuid.NewString()
	execution := models.CommandExecution{
		CommandID: commandID, EdgeDeviceID: 1, NodeID: node.NodeID,
		DeviceType: "prs3001", DeviceConfigID: 1, ChannelID: ch.ID,
		ManifestID: "m-disp", ActionID: "read_rainfall", ActionVersion: 1,
		ActorUserID: 7, IdempotencyScope: "disp-test", IdempotencyKey: uuid.NewString(),
		RequestHash: "hash-disp", ParamsJSON: "{}", Status: StatusQueued,
		DeadlineAt: now.Add(5 * time.Minute), CreatedAt: now.Add(-time.Minute),
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	outbox := models.CommandOutbox{
		CommandID: commandID, EventType: "dispatch", PayloadJSON: "{}",
		State: "PENDING", FencingToken: 0, CreatedAt: now,
	}
	if err := db.Create(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(db, transport, "test-owner")
	return d, &execution, &outbox
}

// --- NewDispatcherOwner ---

func TestNewDispatcherOwner(t *testing.T) {
	owner := NewDispatcherOwner("myprefix")
	if !strings.HasPrefix(owner, "myprefix:") {
		t.Fatalf("owner=%q, want prefix 'myprefix:'", owner)
	}
	if len(owner) > 96 {
		t.Fatalf("owner length=%d, want <=96", len(owner))
	}
	// Empty prefix defaults to "dispatcher"
	owner2 := NewDispatcherOwner("")
	if !strings.HasPrefix(owner2, "dispatcher:") {
		t.Fatalf("owner2=%q, want prefix 'dispatcher:'", owner2)
	}
	// Uniqueness
	owner3 := NewDispatcherOwner("myprefix")
	if owner == owner3 {
		t.Fatal("two owners should be unique")
	}
}

// --- stableWireDigest ---

func TestStableWireDigestDeterministic(t *testing.T) {
	exec := models.CommandExecution{CommandID: "cmd-1", ActionID: "read", ActionVersion: 1, RequestHash: "abc"}
	d1 := stableWireDigest(exec, 1)
	d2 := stableWireDigest(exec, 1)
	if d1 != d2 {
		t.Fatalf("same input produced different digests: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("digest length=%d, want 64", len(d1))
	}
	// Different attempt number → different digest
	d3 := stableWireDigest(exec, 2)
	if d1 == d3 {
		t.Fatal("different attempt should produce different digest")
	}
	// Different command → different digest
	exec2 := exec
	exec2.CommandID = "cmd-2"
	d4 := stableWireDigest(exec2, 1)
	if d1 == d4 {
		t.Fatal("different command should produce different digest")
	}
}

// --- ProcessOnce: nil transport ---

func TestProcessOnceNilTransport(t *testing.T) {
	d, _, _ := setupDispatcher(t, nil)
	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("nil transport should not process")
	}
}

// --- ProcessOnce: no pending outbox ---

func TestProcessOnceNoPending(t *testing.T) {
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}}
	d, _, outbox := setupDispatcher(t, ft)
	// Mark outbox as already processed
	d.db.Model(&models.CommandOutbox{}).Where("id = ?", outbox.ID).Update("state", "PROCESSED")
	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("no pending outbox should not process")
	}
	if len(ft.calls) != 0 {
		t.Fatal("transport should not be called")
	}
}

// --- ProcessOnce: happy path ---

func TestProcessOnceHappyPath(t *testing.T) {
	publishedAt := time.Now().UTC()
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-disp", PublishedAt: publishedAt}}
	d, exec, _ := setupDispatcher(t, ft)

	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("should have processed")
	}
	if len(ft.calls) != 1 {
		t.Fatalf("transport calls=%d, want 1", len(ft.calls))
	}
	if ft.calls[0].CommandID != exec.CommandID {
		t.Fatalf("dispatched wrong command: %s", ft.calls[0].CommandID)
	}

	// Execution should be DISPATCHED
	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)
	if updated.Status != StatusDispatched {
		t.Fatalf("execution status=%s, want DISPATCHED", updated.Status)
	}

	// Outbox should be PROCESSED
	var outbox models.CommandOutbox
	d.db.First(&outbox, "command_id = ?", exec.CommandID)
	if outbox.State != "PROCESSED" {
		t.Fatalf("outbox state=%s, want PROCESSED", outbox.State)
	}
	if outbox.ProcessedAt == nil {
		t.Fatal("outbox should have ProcessedAt set")
	}

	// Attempt should exist
	var attempt models.CommandAttempt
	if err := d.db.First(&attempt, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != StatusDispatched || attempt.BootID != "boot-disp" {
		t.Fatalf("attempt status=%s boot=%s", attempt.Status, attempt.BootID)
	}
	if attempt.WireDigest == "" {
		t.Fatal("attempt should have wire digest")
	}
}

// --- ProcessOnce: execution already cancelled ---

func TestProcessOnceCancelledExecution(t *testing.T) {
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}}
	d, exec, _ := setupDispatcher(t, ft)
	// Cancel the execution before dispatch
	d.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("status", StatusCancelled)

	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("should have claimed the outbox")
	}
	// Transport should NOT be called (execution not QUEUED)
	if len(ft.calls) != 0 {
		t.Fatal("transport should not be called for cancelled execution")
	}
	// Outbox should be CANCELLED
	var outbox models.CommandOutbox
	d.db.First(&outbox, "command_id = ?", exec.CommandID)
	if outbox.State != "CANCELLED" {
		t.Fatalf("outbox state=%s, want CANCELLED", outbox.State)
	}
}

// --- ProcessOnce: transport error ---

func TestProcessOnceTransportError(t *testing.T) {
	ft := &dispatchFakeTransport{err: fmt.Errorf("mqtt publish failed")}
	d, exec, _ := setupDispatcher(t, ft)

	_, err := d.ProcessOnce(context.Background())
	if err == nil {
		t.Fatal("expected transport error to propagate")
	}
	// Execution should remain QUEUED (transaction rolled back)
	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)
	if updated.Status != StatusQueued {
		t.Fatalf("execution status=%s, want QUEUED after transport error", updated.Status)
	}
}

// --- ProcessOnce: expired lease recovered before selection ---

func TestProcessOnceExpiredLeaseRecovered(t *testing.T) {
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}}
	d, exec, outbox := setupDispatcher(t, ft)
	// Simulate a stale lease on the outbox
	past := time.Now().UTC().Add(-time.Minute)
	d.db.Model(&models.CommandOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{
		"state": "LEASED", "lease_owner": "dead-worker", "lease_expires_at": past,
	})

	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expired lease should be recovered and processed")
	}
	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)
	if updated.Status != StatusDispatched {
		t.Fatalf("execution status=%s, want DISPATCHED", updated.Status)
	}
}

// --- ProcessOnce: channel contention (active lease on same channel) ---

func TestProcessOnceChannelContention(t *testing.T) {
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}}
	d, exec, _ := setupDispatcher(t, ft)

	// Create a second execution + outbox on the same channel with an active lease
	now := time.Now().UTC()
	cmd2 := uuid.NewString()
	d.db.Create(&models.CommandExecution{
		CommandID: cmd2, EdgeDeviceID: 2, NodeID: exec.NodeID,
		DeviceType: "prs3001", DeviceConfigID: 1, ChannelID: exec.ChannelID,
		ManifestID: "m", ActionID: "read", ActionVersion: 1,
		ActorUserID: 7, IdempotencyScope: "s2", IdempotencyKey: uuid.NewString(),
		RequestHash: "h2", ParamsJSON: "{}", Status: StatusDispatched,
		DeadlineAt: now.Add(5 * time.Minute), CreatedAt: now,
	})
	futureLease := now.Add(30 * time.Second)
	d.db.Create(&models.CommandOutbox{
		CommandID: cmd2, EventType: "dispatch", PayloadJSON: "{}",
		State: "LEASED", LeaseOwner: "other-worker", LeaseExpiresAt: &futureLease,
		CreatedAt: now,
	})

	// Our outbox should be skipped because the channel has an active lease
	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("should not process when channel has active lease")
	}
	if len(ft.calls) != 0 {
		t.Fatal("transport should not be called during channel contention")
	}
}
