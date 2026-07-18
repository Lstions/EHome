package nodemgr

import (
	"fmt"

	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/google/uuid"
)

// SyncAction represents the decision outcome for a sync event.
type SyncAction int

const (
	SyncActionNone  SyncAction = iota // No action needed — device is in sync
	SyncActionFull                    // Send full ConfigManifest
	SyncActionPatch                   // Send incremental patch (future, v2.2)
	SyncActionDefer                   // Defer — within dedup window
)

// SyncDecision is the output of a SyncGate decision for a single device.
type SyncDecision struct {
	Action     SyncAction
	Reason     string // Human-readable reason for logging
	SyncID     string // UUID for observability
	ManifestID string // Manifest ID to send (if action is Full)
	DeviceID   string // Target device
}

// HelloMsg carries the parsed v2.1 Hello fields for SyncGate decision.
type HelloMsg struct {
	NodeID          string
	FirmwareVersion string
	Model           string
	ChannelCount    uint64
	ConfigEpoch     uint64
	NvsHasConfig    bool
	LastManifest    string
	ProtocolVersion string
}

// StatusReportMsg carries the parsed v2.2 StatusReport fields for SyncGate decision.
type StatusReportMsg struct {
	UptimeSec    uint64
	Status       string
	ChannelCount uint64
	ConfigEpoch  uint64
	SyncState    string
	ConfigHash   string // v2.2: config_hash from device
}

// ConfigQueryMsg carries the parsed ConfigSyncRequest fields.
type ConfigQueryMsg struct {
	Reason            string
	CurrentEpoch      uint64
	CurrentManifestID string
}

// SyncGate is the unified synchronization decision center.
// All sync entry points route through SyncGate for consistent decision-making.
type SyncGate struct {
	mgr      *Manager
	eventBus *ConfigEventBus
}

// NewSyncGate creates a new SyncGate.
func NewSyncGate(mgr *Manager, eventBus *ConfigEventBus) *SyncGate {
	return &SyncGate{
		mgr:      mgr,
		eventBus: eventBus,
	}
}

// recordDecision records a sync decision metric.
func recordDecision(d SyncDecision) {
	actionStr := "none"
	switch d.Action {
	case SyncActionFull:
		actionStr = "full"
	case SyncActionPatch:
		actionStr = "patch"
	case SyncActionDefer:
		actionStr = "defer"
	}
	metrics.SyncDecisionsTotal.WithLabelValues(d.Reason, actionStr).Inc()
}

// decide is the single decision point for all sync logic.
// deviceHash: the hash reported by the device (from Hello last_manifest or StatusReport config_hash)
// nvsEmpty: true if device reports NVS has no config
// deviceChannelCount: number of channels the device reports
func (g *SyncGate) decide(deviceID string, deviceHash string, nvsEmpty bool,
	deviceChannelCount uint64) SyncDecision {

	syncID := uuid.New().String()

	if nvsEmpty {
		serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "nvs_empty",
			SyncID:     syncID,
			ManifestID: serverHash.ManifestID,
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	if serverHash.Hash == "" {
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "no_server_config",
			SyncID:   syncID,
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}

	if deviceHash == serverHash.ManifestID {
		// hash 匹配 = 设备已持有正确配置。
		// 但 channel_count=0 且 nvs_has=1 表示设备 in-memory 配置为空
		// （重启后 NVS 有旧 manifest_id 但 config_mgr 未加载），
		// 必须强制推送让设备重建配置。
		if deviceChannelCount == 0 && !nvsEmpty && serverHash.ChannelCount > 0 {
			d := SyncDecision{
				Action:     SyncActionFull,
				Reason:     "force_push:hash_match_but_zero_channels",
				SyncID:     syncID,
				ManifestID: serverHash.ManifestID,
				DeviceID:   deviceID,
			}
			recordDecision(d)
			return d
		}
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "hash_match",
			SyncID:   syncID,
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}

	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "hash_mismatch",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// OnHello makes a sync decision when a device sends Hello.
func (g *SyncGate) OnHello(deviceID string, hello *HelloMsg) SyncDecision {
	return g.decide(deviceID, hello.LastManifest, !hello.NvsHasConfig, hello.ChannelCount)
}

// OnStatusReport makes a sync decision when a device sends StatusReport.
// CRITICAL: old firmware does not send config_hash — must short-circuit to avoid
// pushing config every 5 seconds (empty string != serverHash is always true).
func (g *SyncGate) OnStatusReport(deviceID string, rpt *StatusReportMsg) SyncDecision {
	if rpt.ConfigHash == "" {
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "no_config_hash_wait_for_hello",
			SyncID:   uuid.New().String(),
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}
	return g.decide(deviceID, rpt.ConfigHash, false, rpt.ChannelCount)
}

// OnConfigChange handles a ConfigChangeEvent from the bus.
// Global/empty node IDs have no broadcast semantics: callers must fan out to
// concrete affected nodes after their database transaction commits.
func (g *SyncGate) OnConfigChange(evt ConfigChangeEvent) []SyncDecision {
	if evt.NodeID == "" || evt.NodeID == "0" {
		logger.Warnf("rejecting config change with non-concrete node_id=%q", evt.NodeID)
		return nil
	}
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(evt.NodeID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     fmt.Sprintf("config_changed: type=%s action=%s", evt.Type, evt.Action),
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   evt.NodeID,
	}
	recordDecision(d)
	return []SyncDecision{d}
}

// OnServerStartup returns decisions for all online nodes.
// Pushes full config to every online device — ensures devices get
// any config changes that happened while the server was down.
func (g *SyncGate) OnServerStartup() []SyncDecision {
	deviceIDs := g.mgr.GetOnlineDeviceIDs()
	decisions := make([]SyncDecision, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		syncID := uuid.New().String()
		serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "server_startup",
			SyncID:     syncID,
			ManifestID: serverHash.ManifestID,
			DeviceID:   deviceID,
		}
		recordDecision(d)
		decisions = append(decisions, d)
	}
	return decisions
}

// OnConfigQuery handles an explicit config query from a device (0x13).
func (g *SyncGate) OnConfigQuery(deviceID string, q *ConfigQueryMsg) SyncDecision {
	return g.decide(deviceID, q.CurrentManifestID, false, 0)
}

// OnOfflineReconnect handles an offline→online transition for a device.
func (g *SyncGate) OnOfflineReconnect(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "offline_reconnect",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// OnFactoryReset handles a factory reset event for a device.
func (g *SyncGate) OnFactoryReset(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "factory_reset",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// Start begins consuming events from the ConfigEventBus and processing them.
func (g *SyncGate) Start() {
	go func() {
		ch := g.eventBus.Subscribe()
		for evt := range ch {
			decisions := g.OnConfigChange(evt)
			for _, d := range decisions {
				if d.Action == SyncActionFull {
					logger.Infof("[sync_id=%s] ConfigChange push: device=%s reason=%s",
						d.SyncID, d.DeviceID, d.Reason)
					g.mgr.SendConfigManifestWithDecision(d)
				}
			}
		}
		logger.Infof("SyncGate event consumer stopped")
	}()
}
