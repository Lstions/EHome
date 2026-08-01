package commandexec

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// boundedResetDriver provides a minimal bounded_sequence reset action whose
// plan is available (no AvailabilityCode) so SetEnabled can enable it.
// It implements ControlActionProvider, ControlActionPlanCompiler, and
// ControlActionVerifier.
type boundedResetDriver struct{}

func (boundedResetDriver) DeviceType() string      { return "bounded-reset-test" }
func (boundedResetDriver) DeviceName() string      { return "test" }
func (boundedResetDriver) OEM() string             { return "test" }
func (boundedResetDriver) Category() string        { return "test" }
func (boundedResetDriver) HardwareTypes() []string { return []string{"uart"} }
func (boundedResetDriver) GetSensorDefinitions() []drivers.SensorData {
	return nil
}
func (boundedResetDriver) ParseData([]byte) ([]drivers.SensorData, error) {
	return nil, nil
}

func (boundedResetDriver) ControlActions() []drivers.ControlAction {
	return []drivers.ControlAction{{
		ID: "reset_value", Version: 1, Name: "reset", Description: "test bounded reset",
		Semantics: "reset", Risk: "high", Enabled: true,
		ExecutionShape: "bounded_sequence", Verification: "readback",
		AtMostOnce: true, MaxSteps: 3,
		AvailabilityCode: "test_pending", AvailabilityReason: "test action awaiting evidence",
	}}
}

func (boundedResetDriver) CompileControlActionPlan(actionID string, params json.RawMessage) (drivers.CompiledControlPlan, error) {
	if actionID != "reset_value" {
		return drivers.CompiledControlPlan{}, fmt.Errorf("unknown action %q", actionID)
	}
	if len(params) != 0 && string(params) != "{}" {
		return drivers.CompiledControlPlan{}, fmt.Errorf("reset does not accept parameters")
	}
	return drivers.CompiledControlPlan{
		AtMostOnce: true,
		Steps: []drivers.CompiledControlPlanStep{
			{ID: "read_before", Kind: "read", TXData: []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0A}, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			{ID: "clear", Kind: "write", TXData: []byte{0x01, 0x06, 0x00, 0x00, 0x00, 0x5A, 0x09, 0xF1}, ReadSize: 8, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			{ID: "readback_zero", Kind: "readback", TXData: []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0A}, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
		},
	}, nil
}

func (boundedResetDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]drivers.SensorData, error) {
	if actionID != "reset_value" || len(raw) != 7 {
		return nil, fmt.Errorf("unexpected verifier input")
	}
	return []drivers.SensorData{{Name: "value", Value: 0, Unit: "raw"}}, nil
}

func boundedResetActions(t *testing.T) *deviceaction.Registry {
	t.Helper()
	driverRegistry := drivers.NewRegistry()
	driverRegistry.Register(boundedResetDriver{})
	actions := deviceaction.NewBuiltInRegistry(driverRegistry)
	if err := actions.SetEnabled("bounded-reset-test", "reset_value", true); err != nil {
		t.Fatal(err)
	}
	return actions
}

func setupBoundedResetEdge(t *testing.T, db *gorm.DB, nodeID, manifestID, capabilities string) (models.EdgeDevice, models.Node) {
	t.Helper()
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-time.Minute)
	node := models.Node{
		NodeID: nodeID, Name: "node", Status: "online",
		ConfigVersion: manifestID, ConfigStatus: "applied", ConfigSyncState: "in_sync",
		BootID: "boot-bounded", ResourceReportedAt: &reported, CommandEngineRevision: 1,
		CommandEngineCapabilities: capabilities,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{
		NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1,
		Type: "bounded-reset-test", Name: "reset-edge", Enabled: true, Status: "active",
	}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	return edge, node
}

func TestBoundedSequenceDispatchCompilesPlanAndPopulatesEnvelope(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	caps := `{"supports_channel_cmd_v2":true,"supports_bounded_batch":true,"supports_finally":true,"max_batch_steps":8,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`
	edge, _ := setupBoundedResetEdge(t, db, "node-bounded-ok", "manifest-bounded", caps)

	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, boundedResetActions(t))
	transport.now = func() time.Time { return now }

	execution := models.CommandExecution{
		CommandID:    "aabbccdd-1122-3344-5566-778899001122",
		EdgeDeviceID: edge.ID, NodeID: edge.NodeID, DeviceType: edge.Type,
		DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID,
		ManifestID: "manifest-bounded", ActionID: "reset_value", ActionVersion: 1,
		CommandEngineRevision: 1, ParamsJSON: "{}", DeadlineAt: now.Add(time.Minute),
	}
	attempt := models.CommandAttempt{AttemptNo: 1}
	result, err := transport.Dispatch(context.Background(), execution, attempt)
	if err != nil {
		t.Fatalf("dispatch err=%v", err)
	}
	if result.BootID != "boot-bounded" {
		t.Fatalf("BootID=%q want boot-bounded", result.BootID)
	}
	if len(publisher.payload) == 0 {
		t.Fatal("no payload published")
	}
	cmd, err := frame.DecodeChannelCmdV2(publisher.payload)
	if err != nil {
		t.Fatal(err)
	}
	// Top-level fields must be populated from the first plan step.
	if len(cmd.TXData) != 8 || cmd.TXData[0] != 0x01 || cmd.TXData[1] != 0x03 {
		t.Fatalf("envelope top-level TXData not from first step: % X", cmd.TXData)
	}
	if cmd.ReadSize != 7 || cmd.RXTimeoutMS != 1500 || cmd.PostTXDelayMS != 100 {
		t.Fatalf("envelope top-level fields not from first step: ReadSize=%d RXTimeoutMS=%d PostTXDelayMS=%d",
			cmd.ReadSize, cmd.RXTimeoutMS, cmd.PostTXDelayMS)
	}
	// Plan must have 3 steps with correct kind codes.
	if len(cmd.Plan) != 3 {
		t.Fatalf("expected 3 plan steps, got %d", len(cmd.Plan))
	}
	// Kind codes: read=0, write=1, readback=2
	expectedKinds := []uint32{0, 1, 2}
	for i, step := range cmd.Plan {
		if step.Kind != expectedKinds[i] {
			t.Fatalf("plan step %d kind=%d want %d", i, step.Kind, expectedKinds[i])
		}
	}
}

func TestBoundedSequenceDispatchRejectsZeroMaxBatchSteps(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	caps := `{"supports_channel_cmd_v2":true,"supports_bounded_batch":true,"supports_finally":true,"max_batch_steps":0,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`
	edge, _ := setupBoundedResetEdge(t, db, "node-bounded-zero", "manifest-zero", caps)

	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, boundedResetActions(t))
	transport.now = func() time.Time { return now }

	execution := models.CommandExecution{
		CommandID:    "bbccddee-2233-4455-6677-889900112233",
		EdgeDeviceID: edge.ID, NodeID: edge.NodeID, DeviceType: edge.Type,
		DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID,
		ManifestID: "manifest-zero", ActionID: "reset_value", ActionVersion: 1,
		CommandEngineRevision: 1, ParamsJSON: "{}", DeadlineAt: now.Add(time.Minute),
	}
	attempt := models.CommandAttempt{AttemptNo: 1}
	_, err := transport.Dispatch(context.Background(), execution, attempt)
	if err == nil {
		t.Fatal("dispatch with MaxBatchSteps=0 should have failed")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("dispatch with MaxBatchSteps=0 published a payload")
	}
}
