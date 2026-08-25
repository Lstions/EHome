package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// setupPeriphNode creates an online node plus its GPIO/PWM config rows and
// returns the periph-enabled action registry.
// Also creates the system_admin user row (ID=7) required by IssueConfirmation.
func setupPeriphNode(t *testing.T, db *gorm.DB) models.Node {
	t.Helper()
	node := models.Node{NodeID: "node-periph", Name: "periph", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	gpio := models.GPIOConfig{NodeID: node.NodeID, Pin: 5, Direction: 1, Enabled: true}
	if err := db.Create(&gpio).Error; err != nil {
		t.Fatal(err)
	}
	pwm := models.PWMConfig{NodeID: node.NodeID, HardwareID: "pwm0", Channel: 2, Pin: 18, Frequency: 1000, Enabled: true}
	if err := db.Create(&pwm).Error; err != nil {
		t.Fatal(err)
	}
	// IssueConfirmation requires a real User row with SystemAdminSubjectKey + recent LastLoginAt.
	now := time.Now().UTC()
	subjectKey := models.SystemAdminSubjectKey
	if err := db.Create(&models.User{ID: 7, Username: "operator", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	return node
}

func periphActions(t *testing.T) *deviceaction.Registry {
	t.Helper()
	return deviceaction.NewBuiltInRegistry(nil)
}

// issuePeriphConfirmation mints a single-use token for a medium-risk periph
// action against the node-id target semantics.
func issuePeriphConfirmation(t *testing.T, s *Service, nodeID uint, actionID string, params json.RawMessage) string {
	t.Helper()
	grant, err := s.IssueConfirmation(context.Background(), ConfirmationInput{
		EdgeDeviceID: nodeID, ActorUserID: 7, ActionID: actionID, Params: params,
		Reason: "test periph action", SourceIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("IssueConfirmation(%s) failed: %v", actionID, err)
	}
	return grant.Token
}

// decodePeriphFrame decodes the published PeriphCmd frame into a field map.
func decodePeriphFrame(t *testing.T, payload []byte) map[uint8]uint64 {
	t.Helper()
	decoder, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if decoder.MsgType() != frame.MsgPeriphCmd {
		t.Fatalf("msg type = 0x%02X, want 0x%02X", decoder.MsgType(), frame.MsgPeriphCmd)
	}
	fields := make(map[uint8]uint64)
	for {
		f, err := decoder.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			t.Fatalf("decode field: %v", err)
		}
		if v, ok := f.Value.(uint64); ok {
			fields[f.FieldNum] = v
		}
	}
	return fields
}

// TestPeriphGateBypassesChannelDomain proves the periph gate list never
// consults channel/manifest/capability facts: the node has none of them, yet
// gpio_set is admitted.
func TestPeriphGateBypassesChannelDomain(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	s := NewService(db, periphActions(t))
	s.SetDispatchEnabled(true)

	params := json.RawMessage(`{"pin":5,"level":1}`)
	token := issuePeriphConfirmation(t, s, node.ID, deviceaction.ActionGPIOSet, params)
	exec, replayed, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: node.ID, ActorUserID: 7, ActionID: deviceaction.ActionGPIOSet,
		Params: params, IdempotencyKey: "periph-gpio-1", SourceIP: "127.0.0.1",
		ConfirmationToken: token, Reason: "test",
	})
	if err != nil || replayed {
		t.Fatalf("create gpio_set err=%v replayed=%v", err, replayed)
	}
	if exec.NodeID != node.NodeID || exec.DeviceType != deviceaction.DeviceTypeGPIO || exec.Status != StatusQueued {
		t.Fatalf("execution projection wrong: node=%s type=%s status=%s", exec.NodeID, exec.DeviceType, exec.Status)
	}
	if exec.EdgeDeviceID != node.ID {
		t.Fatalf("EdgeDeviceID must carry the node DB id, got %d want %d", exec.EdgeDeviceID, node.ID)
	}
	var outbox models.CommandOutbox
	if err := db.Where("command_id = ?", exec.CommandID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
}

// TestPeriphGateRejectsMissingConfig proves gatePeriphConfig fails closed
// when the GPIO pin has no config row, and when the row is disabled.
// Medium-risk periph actions skip confirmation (confirmationRequired=false),
// so Create is called directly without a token — the gate rejection is the test target.
func TestPeriphGateRejectsMissingConfig(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	s := NewService(db, periphActions(t))
	s.SetDispatchEnabled(true)

	// pin 9 has no GPIOConfig row.
	params := json.RawMessage(`{"pin":9,"level":1}`)
	if _, _, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: node.ID, ActorUserID: 7, ActionID: deviceaction.ActionGPIOSet,
		Params: params, IdempotencyKey: "periph-gpio-missing", SourceIP: "127.0.0.1",
		Reason: "test",
	}); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("missing gpio config must yield ErrActionUnavailable, got %v", err)
	}

	// pin 6 exists but disabled.
	disabled := models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1}
	if err := db.Create(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&disabled).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	params = json.RawMessage(`{"pin":6,"level":1}`)
	if _, _, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: node.ID, ActorUserID: 7, ActionID: deviceaction.ActionGPIOSet,
		Params: params, IdempotencyKey: "periph-gpio-disabled", SourceIP: "127.0.0.1",
		Reason: "test",
	}); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("disabled gpio config must yield ErrActionUnavailable, got %v", err)
	}
}

// TestPeriphGateRejectsOfflineNode proves the shared node_status gate still
// applies to periph actions. Medium-risk periph actions skip confirmation,
// so Create is called directly without a token.
func TestPeriphGateRejectsOfflineNode(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	if err := db.Model(&node).Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, periphActions(t))
	s.SetDispatchEnabled(true)

	params := json.RawMessage(`{"pin":5,"level":1}`)
	if _, _, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: node.ID, ActorUserID: 7, ActionID: deviceaction.ActionGPIOSet,
		Params: params, IdempotencyKey: "periph-offline", SourceIP: "127.0.0.1",
		Reason: "test",
	}); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("offline node must yield ErrActionUnavailable, got %v", err)
	}
}

// TestPeriphTransportDispatchGPIO verifies the PeriphCmd frame bytes for
// gpio_set: msg type 0x1B, periph_type=1, resource_id=pin, action from level.
func TestPeriphTransportDispatchGPIO(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	pub := &capturePublisher{}
	transport := NewPeriphTransport(db, pub, periphActions(t))

	for _, tc := range []struct {
		name       string
		level      int
		wantAction uint8
	}{
		{"high", 1, gpioActionSetHigh},
		{"low", 0, gpioActionSetLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, err := deviceaction.CanonicalizeParams(
				mustPeriphDef(t, deviceaction.DeviceTypeGPIO, deviceaction.ActionGPIOSet).InputSchema,
				json.RawMessage(`{"pin":5,"level":`+itoa(tc.level)+`}`))
			if err != nil {
				t.Fatal(err)
			}
			execution := models.CommandExecution{
				CommandID: "11111111-1111-1111-1111-111111111111", EdgeDeviceID: node.ID,
				NodeID: node.NodeID, DeviceType: deviceaction.DeviceTypeGPIO,
				ActionID: deviceaction.ActionGPIOSet, ActionVersion: 1,
				RequestHash: "hash", ParamsJSON: string(params),
				DeadlineAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
			}
			attempt := models.CommandAttempt{CommandID: execution.CommandID, AttemptNo: 1}
			result, err := transport.Dispatch(context.Background(), execution, attempt)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if result.WireDigest == "" || result.PublishedAt.IsZero() {
				t.Fatal("dispatch result must carry wire digest and publish time")
			}
			if pub.topic != mqtt.ControlTopicForNode(node.NodeID) {
				t.Fatalf("topic = %s", pub.topic)
			}
			fields := decodePeriphFrame(t, pub.payload)
			if fields[2] != uint64(periphTypeGPIO) {
				t.Errorf("periph_type = %d, want %d", fields[2], periphTypeGPIO)
			}
			if fields[3] != 5 {
				t.Errorf("resource_id = %d, want pin 5", fields[3])
			}
			if fields[4] != uint64(tc.wantAction) {
				t.Errorf("action = %d, want %d", fields[4], tc.wantAction)
			}
			if fields[1] == 0 {
				t.Error("request_id must be non-zero")
			}
			if _, hasValue := fields[5]; hasValue {
				t.Error("gpio_set must not encode field 5 (value)")
			}
		})
	}
}

// TestPeriphTransportDispatchPWM verifies pwm_set_duty resolves the reported
// hardware_id to the configured channel and carries duty in field 5.
func TestPeriphTransportDispatchPWM(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	pub := &capturePublisher{}
	transport := NewPeriphTransport(db, pub, periphActions(t))

	params, err := deviceaction.CanonicalizeParams(
		mustPeriphDef(t, deviceaction.DeviceTypePWM, deviceaction.ActionPWMSetDuty).InputSchema,
		json.RawMessage(`{"hardware_id":"pwm0","duty":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	execution := models.CommandExecution{
		CommandID: "22222222-2222-2222-2222-222222222222", EdgeDeviceID: node.ID,
		NodeID: node.NodeID, DeviceType: deviceaction.DeviceTypePWM,
		ActionID: deviceaction.ActionPWMSetDuty, ActionVersion: 1,
		RequestHash: "hash", ParamsJSON: string(params),
		DeadlineAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}
	attempt := models.CommandAttempt{CommandID: execution.CommandID, AttemptNo: 1}
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	fields := decodePeriphFrame(t, pub.payload)
	if fields[2] != uint64(periphTypePWM) {
		t.Errorf("periph_type = %d, want %d", fields[2], periphTypePWM)
	}
	if fields[3] != 2 {
		t.Errorf("resource_id = %d, want configured channel 2", fields[3])
	}
	if fields[4] != uint64(pwmActionSetDuty) {
		t.Errorf("action = %d, want %d", fields[4], pwmActionSetDuty)
	}
	if fields[5] != 5000 {
		t.Errorf("value = %d, want duty 5000", fields[5])
	}
}

// TestPeriphTransportDispatchFailsClosed covers definition mismatch,
// uncanonicalizable params and disabled config at the transport layer.
func TestPeriphTransportDispatchFailsClosed(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	pub := &capturePublisher{}
	transport := NewPeriphTransport(db, pub, periphActions(t))

	base := models.CommandExecution{
		CommandID: "33333333-3333-3333-3333-333333333333", EdgeDeviceID: node.ID,
		NodeID: node.NodeID, DeviceType: deviceaction.DeviceTypeGPIO,
		ActionID: deviceaction.ActionGPIOSet, ActionVersion: 1,
		RequestHash: "hash", DeadlineAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}
	attempt := models.CommandAttempt{CommandID: base.CommandID, AttemptNo: 1}

	// Params that never canonicalized (unknown key).
	bad := base
	bad.ParamsJSON = `{"pin":5,"bogus":1}`
	if _, err := transport.Dispatch(context.Background(), bad, attempt); err == nil {
		t.Fatal("invalid persisted params must fail")
	}

	// Pin without config row.
	missing := base
	missing.ParamsJSON = `{"pin":9,"level":1}`
	if _, err := transport.Dispatch(context.Background(), missing, attempt); err == nil {
		t.Fatal("missing gpio config must fail")
	}

	// Version drift must fail (frozen definition semantics).
	drifted := base
	drifted.ParamsJSON = `{"level":1,"pin":5}`
	drifted.ActionVersion = 99
	if _, err := transport.Dispatch(context.Background(), drifted, attempt); err == nil {
		t.Fatal("action version drift must fail")
	}
}

// TestMultiTransportRoutesByAction proves the dispatcher-facing router sends
// periph actions to PeriphTransport and channel actions to ChannelCmdV2.
func TestMultiTransportRoutesByAction(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	pub := &capturePublisher{}
	actions := periphActions(t)
	multi := NewMultiTransport(NewChannelCmdV2Transport(db, pub, actions), NewPeriphTransport(db, pub, actions))

	execution := models.CommandExecution{
		CommandID: "44444444-4444-4444-4444-444444444444", EdgeDeviceID: node.ID,
		NodeID: node.NodeID, DeviceType: deviceaction.DeviceTypeGPIO,
		ActionID: deviceaction.ActionGPIOSet, ActionVersion: 1,
		RequestHash: "hash", ParamsJSON: `{"level":1,"pin":5}`,
		DeadlineAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}
	attempt := models.CommandAttempt{CommandID: execution.CommandID, AttemptNo: 1}
	if _, err := multi.Dispatch(context.Background(), execution, attempt); err != nil {
		t.Fatalf("multi dispatch gpio_set: %v", err)
	}
	if pub.topic != mqtt.ControlTopicForNode(node.NodeID) || len(pub.payload) == 0 || pub.payload[0] != frame.MsgPeriphCmd {
		t.Fatalf("periph route did not publish a PeriphCmd frame")
	}

	// Nil sub-transport fails closed for its family.
	half := NewMultiTransport(nil, NewPeriphTransport(db, pub, actions))
	channelExecution := models.CommandExecution{CommandID: "55555555-5555-5555-5555-555555555555", ActionID: "read_rainfall"}
	if _, err := half.Dispatch(context.Background(), channelExecution, attempt); err == nil {
		t.Fatal("channel route with nil ChannelCmdV2 transport must fail closed")
	}
}

func mustPeriphDef(t *testing.T, deviceType, actionID string) deviceaction.Definition {
	t.Helper()
	def, ok := periphActions(t).Get(deviceType, actionID)
	if !ok {
		t.Fatalf("definition %s/%s missing", deviceType, actionID)
	}
	return def
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	return "1"
}
