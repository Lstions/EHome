package nodemgr

import (
	"fmt"
	"time"

	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/google/uuid"
)

// SyncReason identifies why a sync was triggered.
type SyncReason string

const (
	SyncReasonHello            SyncReason = "hello"
	SyncReasonStatusReport     SyncReason = "status_report"
	SyncReasonConfigChange     SyncReason = "config_change"
	SyncReasonServerStartup    SyncReason = "server_startup"
	SyncReasonConfigQuery      SyncReason = "config_query"
	SyncReasonOfflineReconnect SyncReason = "offline_reconnect"
	SyncReasonFactoryReset     SyncReason = "factory_reset"
	SyncReasonPeriodic         SyncReason = "periodic"
	SyncReasonEpochLag         SyncReason = "epoch_lag"
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
	Epoch      uint64 // Server's current epoch at decision time
	ManifestID string // Manifest ID to send (if action is Full)
	DeviceID   string // Target device (set by caller after decision)
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

// StatusReportMsg carries the parsed v2.1 StatusReport fields for SyncGate decision.
type StatusReportMsg struct {
	UptimeSec    uint64
	Status       string
	ChannelCount uint64
	ConfigEpoch  uint64
	SyncState    string
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
	dedupTTL time.Duration
}

// NewSyncGate creates a new SyncGate.
func NewSyncGate(mgr *Manager, eventBus *ConfigEventBus) *SyncGate {
	return &SyncGate{
		mgr:      mgr,
		eventBus: eventBus,
		dedupTTL: 30 * time.Second,
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

// OnHello makes a sync decision when a device sends Hello.
// Three-axis decision: NVS state > Epoch > Hash (strictest to most lenient).
func (g *SyncGate) OnHello(deviceID string, hello *HelloMsg) SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	// Axis 1: NVS state (fixes G3 — factory reset stuck)
	if !hello.NvsHasConfig {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "nvs_empty_force_sync",
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	// Axis 2: Epoch comparison (fixes G1, G2)
	if hello.ConfigEpoch < serverEpoch {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     fmt.Sprintf("epoch_lag: device=%d server=%d", hello.ConfigEpoch, serverEpoch),
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	// Axis 3: Hash comparison (fallback)
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	if hello.LastManifest != serverHash.ManifestID {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "manifest_id_mismatch",
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	// Dedup: if hash changed or first time within TTL, still send
	if g.mgr.hashMgr.ShouldSendConfig(deviceID, serverHash.Hash) {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "dedup_expired_or_first_time",
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	d := SyncDecision{
		Action:   SyncActionNone,
		Reason:   "in_sync",
		SyncID:   syncID,
		Epoch:    serverEpoch,
		DeviceID: deviceID,
	}
	recordDecision(d)
	return d
}

// OnStatusReport makes a sync decision when a device sends StatusReport.
// Fixes G5: offline→online path now triggers config re-sync if epoch is behind.
func (g *SyncGate) OnStatusReport(deviceID string, rpt *StatusReportMsg) SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	// Epoch check: if device is behind, push config
	if rpt.ConfigEpoch < serverEpoch {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     fmt.Sprintf("status_epoch_lag: device=%d server=%d", rpt.ConfigEpoch, serverEpoch),
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	d := SyncDecision{
		Action:   SyncActionNone,
		Reason:   "status_in_sync",
		SyncID:   syncID,
		Epoch:    serverEpoch,
		DeviceID: deviceID,
	}
	recordDecision(d)
	return d
}

// OnConfigChange handles a ConfigChangeEvent from the bus.
// Returns one SyncDecision per affected node device.
func (g *SyncGate) OnConfigChange(evt ConfigChangeEvent) []SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	// Find the device_id for the affected node
	var deviceID string
	if evt.NodeID != "" {
		deviceID = evt.NodeID
	}
	if deviceID == "" {
		logger.Infof("[sync_id=%s] OnConfigChange: node %s has no device, skip",
			syncID, evt.NodeID)
		return nil
	}

	// For config change events, always push (epoch already incremented by Publish)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     fmt.Sprintf("config_change: type=%s action=%s entity=%d", evt.Type, evt.Action, evt.EntityID),
		SyncID:     syncID,
		Epoch:      serverEpoch,
		ManifestID: g.mgr.BuildManifestID(),
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return []SyncDecision{d}
}

// OnServerStartup returns decisions for all online nodes.
// Fixes G2: server restart no longer loses sync state.
func (g *SyncGate) OnServerStartup() []SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	var decisions []SyncDecision
	onlineDevices := g.mgr.GetOnlineDeviceIDs()
	for _, deviceID := range onlineDevices {
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "server_startup_push",
			SyncID:     syncID,
			Epoch:      serverEpoch,
			ManifestID: g.mgr.BuildManifestID(),
			DeviceID:   deviceID,
		}
		recordDecision(d)
		decisions = append(decisions, d)
	}
	logger.Infof("[sync_id=%s] OnServerStartup: %d online nodes to push", syncID, len(decisions))
	return decisions
}

// OnConfigQuery handles an explicit config query from a device (0x13).
func (g *SyncGate) OnConfigQuery(deviceID string, q *ConfigQueryMsg) SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)

	// If manifest matches and epoch matches, device is in sync
	if q.CurrentEpoch >= serverEpoch && q.CurrentManifestID == serverHash.ManifestID {
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "config_query_in_sync",
			SyncID:   syncID,
			Epoch:    serverEpoch,
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}

	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     fmt.Sprintf("config_query_mismatch: device_epoch=%d server_epoch=%d", q.CurrentEpoch, serverEpoch),
		SyncID:     syncID,
		Epoch:      serverEpoch,
		ManifestID: g.mgr.BuildManifestID(),
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

	// OnOfflineReconnect handles an offline→online transition for a device.
func (g *SyncGate) OnOfflineReconnect(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "offline_reconnect_push",
		SyncID:     syncID,
		Epoch:      serverEpoch,
		ManifestID: g.mgr.BuildManifestID(),
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

	// OnFactoryReset handles a factory reset event for a device.
func (g *SyncGate) OnFactoryReset(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverEpoch := g.eventBus.CurrentEpoch()

	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "factory_reset_force_sync",
		SyncID:     syncID,
		Epoch:      serverEpoch,
		ManifestID: g.mgr.BuildManifestID(),
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
