package commandexec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

type capturePublisher struct {
	topic   string
	payload []byte
}

func enabledReadActions(t *testing.T) *deviceaction.Registry {
	t.Helper()
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	return actions
}

func (p *capturePublisher) Publish(topic string, payload []byte) error {
	p.topic, p.payload = topic, append([]byte(nil), payload...)
	return nil
}
func (p *capturePublisher) PublishQoS2(string, []byte) error     { return nil }
func (p *capturePublisher) PublishRetained(string, []byte) error { return nil }

func markChannelReported(t *testing.T, db *gorm.DB, node *models.Node, channelID uint) {
	t.Helper()
	info := fmt.Sprintf(`{"channels":[{"id":%d,"enabled":true}]}`, channelID)
	if err := db.Model(node).Update("hardware_info", info).Error; err != nil {
		t.Fatal(err)
	}
	node.HardwareInfo = info
}

func TestChannelCmdV2TransportCompilesTrustedRead(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-time.Minute)
	node := models.Node{NodeID: "node-v2", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-42", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, enabledReadActions(t))
	transport.now = func() time.Time { return now }
	execution := models.CommandExecution{CommandID: "00112233-4455-6677-8899-aabbccddeeff", EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, CommandEngineRevision: node.CommandEngineRevision, DeadlineAt: now.Add(time.Minute)}
	attempt := models.CommandAttempt{AttemptNo: 1, WireDigest: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}
	result, err := transport.Dispatch(context.Background(), execution, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if result.BootID != node.BootID || publisher.topic != "nodes/node-v2/control" {
		t.Fatalf("result=%+v topic=%s", result, publisher.topic)
	}
	cmd, err := frame.DecodeChannelCmdV2(publisher.payload)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.BootID != node.BootID || cmd.EdgeDeviceID != uint32(edge.ID) || cmd.ChannelID != uint32(channel.ID) || cmd.ReadSize != 9 || cmd.RXTimeoutMS != 1000 || cmd.PostTXDelayMS != 100 || len(cmd.TXData) != 8 || cmd.TXData[1] != 0x03 {
		t.Fatalf("compiled command=%+v", cmd)
	}

	// Defense in depth: even a directly registered/enabled action must not
	// cross the production transport until the high-risk engine exists.
	unsafeActions := enabledReadActions(t)
	if err := unsafeActions.Register(deviceaction.Definition{ID: "medium_read", Version: 1, Name: "medium", DeviceType: edge.Type, Semantics: "read", Risk: "medium", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	transport.actions = unsafeActions
	publisher.payload = nil
	execution.ActionID = "medium_read"
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err == nil {
		t.Fatal("medium-risk action crossed the low-risk-only transport")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("rejected medium-risk action was published")
	}
}

func TestChannelCmdV2TransportRejectsStaleOrDisabledCapability(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-MaxCapabilityAge - time.Second)
	node := models.Node{NodeID: "node-stale", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-42", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, enabledReadActions(t))
	transport.now = func() time.Time { return now }
	execution := models.CommandExecution{CommandID: "00112233-4455-6677-8899-aabbccddeeff", EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, CommandEngineRevision: node.CommandEngineRevision, DeadlineAt: now.Add(time.Minute)}
	attempt := models.CommandAttempt{AttemptNo: 1, WireDigest: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err == nil {
		t.Fatal("stale capability was accepted")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("stale capability published")
	}
}

func TestChannelCmdV2TransportRejectsMissingFinallyCapability(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-time.Minute)
	node := models.Node{NodeID: "node-no-final", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-42", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":false,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, enabledReadActions(t))
	transport.now = func() time.Time { return now }
	execution := models.CommandExecution{CommandID: "00112233-4455-6677-8899-aabbccddeeff", EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, CommandEngineRevision: node.CommandEngineRevision, DeadlineAt: now.Add(time.Minute)}
	attempt := models.CommandAttempt{AttemptNo: 1, WireDigest: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err == nil {
		t.Fatal("node without final capability was accepted")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("node without final capability published")
	}
}

func TestChannelCmdV2TransportRejectsChangedCommandEngineRevision(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-time.Minute)
	node := models.Node{NodeID: "node-revision", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-42", ResourceReportedAt: &reported, CommandEngineRevision: 2, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, enabledReadActions(t))
	transport.now = func() time.Time { return now }
	execution := models.CommandExecution{CommandID: "00112233-4455-6677-8899-aabbccddeeff", EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, CommandEngineRevision: 1, DeadlineAt: now.Add(time.Minute)}
	attempt := models.CommandAttempt{AttemptNo: 1, WireDigest: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err == nil {
		t.Fatal("execution created for a previous engine revision was published")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("revision-mismatched execution published")
	}
}

func TestChannelCmdV2TransportRejectsInvalidPersistedParams(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	reported := now.Add(-time.Minute)
	node := models.Node{NodeID: "node-invalid-params", Name: "node", Status: "online", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-42", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	markChannelReported(t, db, &node, channel.ID)
	edge := models.EdgeDevice{NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Name: "rain", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &capturePublisher{}
	transport := NewChannelCmdV2Transport(db, publisher, enabledReadActions(t))
	transport.now = func() time.Time { return now }
	execution := models.CommandExecution{CommandID: "00112233-4455-6677-8899-aabbccddeeff", EdgeDeviceID: edge.ID, NodeID: node.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, CommandEngineRevision: node.CommandEngineRevision, ParamsJSON: `{"unexpected":true}`, DeadlineAt: now.Add(time.Minute)}
	attempt := models.CommandAttempt{AttemptNo: 1, WireDigest: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}
	if _, err := transport.Dispatch(context.Background(), execution, attempt); err == nil {
		t.Fatal("invalid persisted params were compiled")
	}
	if len(publisher.payload) != 0 {
		t.Fatal("invalid persisted params were published")
	}
}
