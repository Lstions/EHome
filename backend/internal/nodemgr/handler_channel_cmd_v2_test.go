package nodemgr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

type v2CapturePublisher struct {
	topic   string
	payload []byte
}

func (p *v2CapturePublisher) Publish(topic string, payload []byte) error {
	p.topic = topic
	p.payload = append([]byte(nil), payload...)
	return nil
}
func (p *v2CapturePublisher) PublishQoS2(string, []byte) error     { return nil }
func (p *v2CapturePublisher) PublishRetained(string, []byte) error { return nil }

func TestSN3001BaudSideEffectUpdatesChannelAndPublishesEvent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-baud-side-effect", Name: "node", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{
		NodeID: node.NodeID, HardwareType: "uart", BusType: "UART",
		BusConfig: "1415000012C0", Enabled: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, nil, nil, nil, nil, nil, drivers.NewRegistry())
	if err := manager.applySN3001ControlSideEffect(models.CommandExecution{
		NodeID: node.NodeID, ChannelID: channel.ID, DeviceType: "sn3001_rain",
		ActionID: "set_baud_rate", ParamsJSON: `{"value":"9600"}`,
	}); err != nil {
		t.Fatal(err)
	}
	var got models.Channel
	if err := db.First(&got, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.BusConfig != "141500002580" {
		t.Fatalf("bus_config=%q, want 141500002580", got.BusConfig)
	}
	select {
	case event := <-manager.EventBus().Subscribe():
		if event.Type != CfgChangeChannel || event.Action != CfgActionUpdate || event.NodeID != node.NodeID || event.EntityID != fmt.Sprint(channel.ID) {
			t.Fatalf("unexpected config event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("baud side effect did not publish config event")
	}
	// Replaying the same final is idempotent and must not enqueue another sync.
	if err := manager.applySN3001ControlSideEffect(models.CommandExecution{
		NodeID: node.NodeID, ChannelID: channel.ID, DeviceType: "sn3001_rain",
		ActionID: "set_baud_rate", ParamsJSON: `{"value":"9600"}`,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-manager.EventBus().Subscribe():
		t.Fatalf("idempotent replay published unexpected event=%+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestChannelCmdV2FinalRequiresIdentityAndDriverParse(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-v2-final", Name: "node", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&node).Update("hardware_info", fmt.Sprintf(`{"channels":[{"id":%d,"enabled":true}]}`, channel.ID)).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	commandID := "00112233-4455-6677-8899-aabbccddeeff"
	digestHex := "102132435465768798a9bacbdcedfe0f00112233445566778899aabbccddeeff"
	execution := models.CommandExecution{CommandID: commandID, EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ActionID: "read_rainfall", ActionVersion: 1, ActorUserID: 1, IdempotencyScope: "test", IdempotencyKey: "test-final-0001", RequestHash: "request", ParamsJSON: "{}", Status: commandexec.StatusDispatched, DeadlineAt: time.Now().Add(time.Minute), CreatedAt: time.Now()}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	attempt := models.CommandAttempt{CommandID: commandID, AttemptNo: 1, Status: commandexec.StatusDispatched, EnvelopeID: "test-envelope", WireDigest: digestHex, BootID: "boot-1", FencingToken: 1, CreatedAt: time.Now()}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	manager := NewManager(db, nil, nil, nil, nil, nil, registry)
	manager.SetCommandExecutionService(commandexec.NewService(db, deviceaction.NewBuiltInRegistry(registry)))

	var commandBytes, digest [16]byte
	decodedID, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	decodedDigest, _ := hex.DecodeString(digestHex[:32])
	copy(commandBytes[:], decodedID)
	copy(digest[:], decodedDigest)
	enc := frame.NewEncoder(frame.MsgChannelCmdV2Final)
	enc.EncodeBytes(1, commandBytes[:])
	enc.EncodeBytes(2, digest[:])
	enc.EncodeVarint(3, 1)
	enc.EncodeString(4, "boot-1")
	enc.EncodeVarint(5, 1)
	enc.EncodeVarint(6, 1)
	enc.EncodeVarint(7, 0)
	enc.EncodeBytes(8, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0})
	manager.handleChannelCmdV2Response(node.NodeID, enc.Bytes())
	var got models.CommandExecution
	if err := db.First(&got, "command_id = ?", commandID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != commandexec.StatusSucceeded {
		t.Fatalf("final status=%s", got.Status)
	}
	var verified []drivers.SensorData
	if err := json.Unmarshal([]byte(got.VerifiedResultJSON), &verified); err != nil || len(verified) == 0 || verified[0].Name != "rainfall" {
		t.Fatalf("verified result=%q parsed=%+v err=%v", got.VerifiedResultJSON, verified, err)
	}
	var gotAttempt models.CommandAttempt
	if err := db.Where("command_id = ? AND attempt_no = ?", commandID, 1).First(&gotAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if gotAttempt.Status != commandexec.StatusSucceeded || gotAttempt.CompletedAt == nil {
		t.Fatalf("attempt=%+v", gotAttempt)
	}

	// A stale boot cannot overwrite the terminal record or create a new inbox event.
	enc = frame.NewEncoder(frame.MsgChannelCmdV2Final)
	enc.EncodeBytes(1, commandBytes[:])
	enc.EncodeBytes(2, digest[:])
	enc.EncodeVarint(3, 1)
	enc.EncodeString(4, "stale-boot")
	enc.EncodeVarint(5, 2)
	enc.EncodeVarint(6, 1)
	enc.EncodeVarint(7, 0)
	enc.EncodeBytes(8, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0})
	manager.handleChannelCmdV2Response(node.NodeID, enc.Bytes())
	var inboxCount int64
	db.Model(&models.CommandInbox{}).Where("command_id = ?", commandID).Count(&inboxCount)
	if inboxCount != 1 {
		t.Fatalf("stale boot created inbox event count=%d", inboxCount)
	}
}

func TestReadActionOutboxToFinalDriverVerificationSlice(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	node := models.Node{NodeID: "node-v2-e2e", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-e2e", ResourceReportedAt: &now, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&node).Update("hardware_info", fmt.Sprintf(`{"channels":[{"id":%d,"enabled":true}]}`, channel.ID)).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	actions := deviceaction.NewBuiltInRegistry(registry)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	service := commandexec.NewService(db, actions)
	service.SetDispatchEnabled(true)
	execution, replayed, err := service.Create(context.Background(), commandexec.CreateInput{EdgeDeviceID: edge.ID, ActorUserID: 1, ActionID: "read_rainfall", Params: []byte(`{}`), IdempotencyKey: "e2e-read-action-0001"})
	if err != nil || replayed {
		t.Fatalf("create execution=%+v replayed=%v err=%v", execution, replayed, err)
	}
	publisher := &v2CapturePublisher{}
	dispatcher := commandexec.NewDispatcher(db, commandexec.NewChannelCmdV2Transport(db, publisher, actions), "e2e-test")
	processed, err := dispatcher.ProcessOnce(context.Background())
	if err != nil || !processed || publisher.topic == "" {
		t.Fatalf("dispatch processed=%v topic=%q err=%v", processed, publisher.topic, err)
	}
	command, err := frame.DecodeChannelCmdV2(publisher.payload)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, nil, nil, nil, nil, nil, registry)
	manager.SetCommandExecutionService(service)
	ack := frame.NewEncoder(frame.MsgChannelCmdV2Ack)
	ack.EncodeBytes(1, command.CommandID[:])
	ack.EncodeBytes(2, command.PayloadDigest[:])
	ack.EncodeVarint(3, uint64(command.Attempt))
	ack.EncodeString(4, command.BootID)
	ack.EncodeVarint(5, 1)
	ack.EncodeVarint(6, 1)
	ack.EncodeVarint(7, 0)
	manager.handleChannelCmdV2Response(node.NodeID, ack.Bytes())
	final := frame.NewEncoder(frame.MsgChannelCmdV2Final)
	final.EncodeBytes(1, command.CommandID[:])
	final.EncodeBytes(2, command.PayloadDigest[:])
	final.EncodeVarint(3, uint64(command.Attempt))
	final.EncodeString(4, command.BootID)
	final.EncodeVarint(5, 2)
	final.EncodeVarint(6, 1)
	final.EncodeVarint(7, 0)
	// Valid PRS-3001 Modbus response: Driver parsing is the final verifier.
	final.EncodeBytes(8, []byte{1, 3, 6, 0, 0, 0, 0, 0, 20, 0, 0})
	manager.handleChannelCmdV2Response(node.NodeID, final.Bytes())
	got, err := service.Get(context.Background(), execution.CommandID)
	if err != nil || got.Status != commandexec.StatusSucceeded {
		t.Fatalf("execution=%+v err=%v", got, err)
	}
	var verified []drivers.SensorData
	if err := json.Unmarshal([]byte(got.VerifiedResultJSON), &verified); err != nil || len(verified) == 0 || verified[0].Name != "rainfall" {
		t.Fatalf("verified result=%q parsed=%+v err=%v", got.VerifiedResultJSON, verified, err)
	}
	var attempt models.CommandAttempt
	if err := db.Where("command_id = ?", execution.CommandID).First(&attempt).Error; err != nil || attempt.Status != commandexec.StatusSucceeded || attempt.CompletedAt == nil {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	var inboxCount int64
	if err := db.Model(&models.CommandInbox{}).Where("command_id = ?", execution.CommandID).Count(&inboxCount).Error; err != nil || inboxCount != 2 {
		t.Fatalf("inbox count=%d err=%v", inboxCount, err)
	}
}
