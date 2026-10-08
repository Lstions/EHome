package nodemgr

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/protoframe"

	"gorm.io/gorm"
)

// These bounds mirror config_mgr's fixed manifest storage on the currently
// supported ESP32 collectors.  The device must still validate untrusted input,
// but the server must not publish a manifest it already knows the collector
// cannot apply.  A future negotiated capability can replace these protocol
// baseline limits without weakening the fail-closed default.
const (
	maxManifestTemplates     = 16
	maxManifestChannels      = 8
	maxLegacyTemplateIDs     = 8
	maxEdgeDevicesPerChannel = 5
	// MaxCommandsPerEdgeDevice mirrors config_mgr's MAX_COMMANDS_PER_DEVICE.
	// Exported so the API layer can refuse to SAVE a configuration that the
	// collector could never receive, instead of letting it fail at push time.
	MaxCommandsPerEdgeDevice = 3

	// MaxManifestWireBytes is the largest ConfigManifest payload that fits in a
	// SINGLE MQTT downlink event on the ESP32 collectors. It is a hard wire
	// bound, not a memory-tuning knob: esp-mqtt fragments an inbound PUBLISH
	// that does not fit the receive buffer into several MQTT_EVENT_DATA events
	// and the firmware has NO downlink reassembly, so every fragment is handed
	// to msg_handler_process() as if it were a whole frame. A manifest above
	// this bound is therefore not merely delayed — it is undeliverable
	// (first fragment fails to parse → ConfigResult(false), the rest is
	// discarded as an unknown mid-frame type), while the backend used to see
	// only "published successfully" (V3 设计文档 §8 R1).
	//
	// Derivation (all inputs read from the firmware tree):
	//   CONFIG_MQTT_BUFFER_SIZE = 2048        (esp32-collector/sdkconfig.defaults:24)
	//   topic = "nodes/<node_id>/control"     (ehome_mqtt.c:665)
	//   per-event payload = 2048
	//                     − 1                 (fixed header byte 1: type + flags)
	//                     − 2                 (remaining-length varint, 2-byte form)
	//                     − (2 + len(topic))  (2-byte topic length prefix + topic)
	//                     − 2                 (QoS 1 packet identifier)
	//   For the shortest deployed node_id (12 hex chars → 26 B topic) that is
	//   2015 B; for 16 chars (30 B topic) 2011 B; for 32 chars 1995 B. The
	//   product issues 12-char hex node_ids, so 2011 B is the conservative
	//   (16-char) figure and leaves 4 B of slack on real hardware.
	MaxManifestWireBytes = 2011
)

// checkManifestWireBytes fails closed when an encoded ConfigManifest cannot fit
// in one MQTT downlink event. It is deliberately a pure function on the encoded
// length: the send path calls it with len(payload) of the bytes it is about to
// publish, and the boundary itself (2011 pass / 2012 reject) is pinned directly.
//
// The caller must pass the length of the SAME payload it is about to publish —
// never a re-encoded copy — so the check and the bytes on the wire are
// same-source (see SendConfigManifestWithDecision).
//
// # Why the MQTT bound is applied even to nodes on TCP (2026-10-07)
//
// This bound is transport-INDEPENDENT on purpose, and that is worth stating
// because it looks like an oversight otherwise: a 3.0 node with a live TCP
// session could carry up to protoframe.PayloadMax (16368 B), yet a 3100 B
// manifest is still rejected here.
//
// The reason is that the downlink may STILL end up on MQTT: downlink.Bridge
// falls back to the legacy publisher when the TCP session is absent, when
// framing fails, or when SendToNode errors. A bound that only held for TCP
// would let an oversized manifest through at the gate and then fail (or worse,
// be mishandled) on the fallback path. The gate must therefore hold for the
// WORST transport, not the one that happens to be up right now.
//
// Making the bound transport-aware is design work, not a bug fix — see
// ESP32-3.0-重构方案 §5.2 (new bound derived from the reassembly buffer) and
// the §489 row retiring this gate. Until then the conservative bound stands,
// and the error message says so rather than blaming MQTT alone.
func checkManifestWireBytes(encodedBytes int) error {
	if encodedBytes > MaxManifestWireBytes {
		return fmt.Errorf("ConfigManifest is %d bytes; the bound is %d bytes, "+
			"which is MQTT's single-event limit (CONFIG_MQTT_BUFFER_SIZE=2048 minus framing and topic; "+
			"esp-mqtt fragments, and the firmware has no downlink reassembly). "+
			"This bound is applied REGARDLESS of transport on purpose: the same downlink may still be "+
			"carried by MQTT if the node's TCP session drops (downlink.Bridge falls back), so it has to "+
			"hold for the worst transport. TCP alone would carry up to %d B — making this bound "+
			"transport-aware is design work (ESP32-3.0-重构方案 §5.2 / §489), not done yet. "+
			"Refusing to publish a manifest the collector may be unable to receive",
			encodedBytes, MaxManifestWireBytes, protoframe.PayloadMax)
	}
	return nil
}

type manifestLimits struct {
	maxTemplates   int
	maxChannels    int
	maxTemplateIDs int
}

func manifestLimitsFor(node models.Node) (manifestLimits, error) {
	limits := manifestLimits{maxTemplates: maxManifestTemplates, maxChannels: maxManifestChannels, maxTemplateIDs: maxLegacyTemplateIDs}
	if strings.TrimSpace(node.HardwareInfo) == "" {
		return limits, nil
	}
	var info struct {
		ManifestCapacity *manifestCapacityReport `json:"manifest_capacity"`
	}
	if err := json.Unmarshal([]byte(node.HardwareInfo), &info); err != nil {
		return manifestLimits{}, fmt.Errorf("decode node manifest capacity: %w", err)
	}
	if info.ManifestCapacity == nil {
		return limits, nil // compatibility with pre-capacity-report firmware
	}
	if info.ManifestCapacity.MaxTemplates == 0 || info.ManifestCapacity.MaxChannels == 0 || info.ManifestCapacity.MaxTemplateIDs == 0 {
		return manifestLimits{}, fmt.Errorf("node manifest capacity is incomplete")
	}
	limits.maxTemplates = int(info.ManifestCapacity.MaxTemplates)
	limits.maxChannels = int(info.ManifestCapacity.MaxChannels)
	limits.maxTemplateIDs = int(info.ManifestCapacity.MaxTemplateIDs)
	return limits, nil
}

var nextLegacyWriteRequestID = uint32(time.Now().UnixNano())

// SendPeriphCmd sends a peripheral control command (GPIO/PWM) to a device.
// Uses QoS 1. ESP-MQTT subscribes at QoS 1 and does not complete the QoS 2
// handshake for inbound commands, which leaves broker inflight deliveries stuck.
// Request IDs provide command correlation; peripheral actions are idempotent.
// resourceID is a GPIO pin for GPIO commands and a reported PWM channel for PWM commands.
func (m *Manager) SendPeriphCmd(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte) error {
	_, err := m.SendPeriphCmdWithID(deviceID, periphType, resourceID, action, value, config)
	return err
}

func (m *Manager) SendPeriphCmdWithID(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte) (uint32, error) {
	return m.sendPeriphCmdWithPreviousValue(deviceID, periphType, resourceID, action, value, config, 0)
}

func (m *Manager) SendPeriphCmdWithPreviousValue(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte, previousValue uint32) (uint32, error) {
	return m.sendPeriphCmdWithPreviousValue(deviceID, periphType, resourceID, action, value, config, previousValue)
}

func (m *Manager) sendPeriphCmdWithPreviousValue(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte, previousValue uint32) (uint32, error) {
	if atomic.LoadUint32(&nextPeriphRequestID) == 0 {
		var seed [4]byte
		if _, err := rand.Read(seed[:]); err == nil {
			atomic.CompareAndSwapUint32(&nextPeriphRequestID, 0, uint32(seed[0])<<24|uint32(seed[1])<<16|uint32(seed[2])<<8|uint32(seed[3]))
		}
	}
	requestID := atomic.AddUint32(&nextPeriphRequestID, 1)
	meta := periphRequestMeta{deviceID: deviceID, periphType: periphType, resourceID: resourceID, action: action, created: time.Now(), previousValue: previousValue, provisionalValue: value}
	if periphType == 2 && m.db != nil {
		var cfg models.PWMConfig
		if err := m.db.Where("node_id = ? AND channel = ?", deviceID, resourceID).First(&cfg).Error; err == nil {
			meta.hardwareID, meta.pin = cfg.HardwareID, cfg.Pin
		}
	}
	m.periphMu.Lock()
	if m.periphPending == nil {
		m.periphPending = make(map[uint32]periphRequestMeta)
	}
	if m.periphLatest == nil {
		m.periphLatest = make(map[string]uint32)
	}
	latestKey := fmt.Sprintf("%s:%d:%d:%d", deviceID, periphType, resourceID, action)
	previousLatest, hadPreviousLatest := m.periphLatest[latestKey]
	if previousMeta, ok := m.periphPending[previousLatest]; ok && periphType == 2 && (action == 0 || action == 1) && previousMeta.action == action {
		meta.previousValue = previousMeta.previousValue
	}
	m.periphPending[requestID] = meta
	m.periphLatest[latestKey] = requestID
	for id, meta := range m.periphPending {
		if time.Since(meta.created) > time.Minute {
			delete(m.periphPending, id)
			key := fmt.Sprintf("%s:%d:%d:%d", meta.deviceID, meta.periphType, meta.resourceID, meta.action)
			if m.periphLatest[key] == id {
				delete(m.periphLatest, key)
			}
		}
	}
	enc := frame.NewEncoder(frame.MsgPeriphCmd)
	enc.EncodeVarint(1, uint64(requestID))  // field 1: request_id
	enc.EncodeVarint(2, uint64(periphType)) // field 2: periph_type
	enc.EncodeVarint(3, uint64(resourceID)) // field 3: resource_id
	enc.EncodeVarint(4, uint64(action))     // field 4: action
	if value > 0 {
		enc.EncodeVarint(5, uint64(value)) // field 5: value (optional)
	}
	if len(config) > 0 {
		enc.EncodeBytes(6, config) // field 6: config (optional)
	}

	logger.Infof("[%s] SendPeriphCmd: type=%d resource_id=%d action=%d value=%d config_len=%d reqID=%d",
		deviceID, periphType, resourceID, action, value, len(config), requestID)

	if m.downlink == nil {
		// Unwired publisher: tests construct a Manager without one. Refuse and
		// release the pending entry rather than pretending the command went out.

		delete(m.periphPending, requestID)
		if m.periphLatest[latestKey] == requestID {
			if hadPreviousLatest {
				if _, stillPending := m.periphPending[previousLatest]; stillPending {
					m.periphLatest[latestKey] = previousLatest
				} else {
					delete(m.periphLatest, latestKey)
				}
			} else {
				delete(m.periphLatest, latestKey)
			}
		}
		m.periphMu.Unlock()
		return 0, fmt.Errorf("no downlink publisher configured")
	}
	if err := m.downlink.Publish(deviceID, enc.Bytes()); err != nil {
		delete(m.periphPending, requestID)
		if m.periphLatest[latestKey] == requestID {
			if hadPreviousLatest {
				if _, stillPending := m.periphPending[previousLatest]; stillPending {
					m.periphLatest[latestKey] = previousLatest
				} else {
					delete(m.periphLatest, latestKey)
				}
			} else {
				delete(m.periphLatest, latestKey)
			}
		}
		m.periphMu.Unlock()
		return 0, err
	}
	m.periphMu.Unlock()
	return requestID, nil
}

// nextPeriphRequestID is the atomic counter for PeriphCmd request IDs.
var nextPeriphRequestID uint32

// SendPing sends a Ping message to a device and registers the timestamp in
// the PingTracker for anti-forgery verification and retry-on-timeout.
// F7.6: Track the ping for retry on timeout
func (m *Manager) SendPing(deviceID string) error {
	ts := time.Now().UnixMicro()
	enc := frame.NewEncoder(frame.MsgPing)
	enc.EncodeVarint(1, uint64(ts))

	// F7.6: Register pending ping for retry/timeout + anti-forgery
	// verification (the tracked timestamp replaces the former Redis TTL key;
	// the tracker's 10s timeout cleanup preserves the TTL semantics).
	if m.pingTracker != nil {
		m.pingTracker.Track(deviceID, ts, func(latencyMs int64, success bool) {
			if !success {
				logger.Warnf("[%s] Ping failed after %d retries (timeout)", deviceID, m.pingTracker.maxRetry)
				m.wsHub.BroadcastEvent(events.PingResult, map[string]interface{}{
					"device_id":  deviceID,
					"node_id":    deviceID, // v2.2 新增
					"latency_ms": -1,
					"timestamp":  time.Now().Unix(),
					"verified":   false,
					"reason":     "timeout",
				})
			}
		})
	}

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendWriteCommand sends a WriteCommand to a device
// P3-5: Uses QoS 2 (exactly-once) for critical write operations
func (m *Manager) SendWriteCommand(deviceID string, channelID uint32, data []byte, readSize uint32) error {
	digest := sha256.Sum256(data)
	logger.Infof("[sender] SendWriteCommand: device=%s ch=%d tx_bytes=%d tx_digest=%x readSize=%d", deviceID, channelID, len(data), digest[:8], readSize)
	requestID := atomic.AddUint32(&nextLegacyWriteRequestID, 1)
	if requestID == 0 {
		requestID = atomic.AddUint32(&nextLegacyWriteRequestID, 1)
	}

	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(requestID))
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendScanRequest sends a ScanRequest to a device (I2C mode)
func (m *Manager) SendScanRequest(deviceID string, hardwareID uint32) error {
	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, fmt.Sprintf("scan-%d", time.Now().Unix()))
	enc.EncodeVarint(2, uint64(hardwareID))

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendModbusScanRequest sends a ScanRequest to a device (Modbus mode)
// field 1: request_id (string)
// field 3: scan_type = 2 (MODBUS)
// field 4: start_addr
// field 5: end_addr
// field 6: timeout_ms (per-address timeout)
func (m *Manager) SendModbusScanRequest(deviceID string, startAddr, endAddr, timeoutMs int) (string, error) {
	requestID := fmt.Sprintf("scan-%d", time.Now().UnixMilli())

	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, requestID)
	enc.EncodeVarint(3, 2) // scan_type = MODBUS

	if startAddr > 0 {
		enc.EncodeVarint(4, uint64(startAddr))
	} else {
		enc.EncodeVarint(4, 1) // default start from 1
	}
	if endAddr > 0 {
		enc.EncodeVarint(5, uint64(endAddr))
	} else {
		enc.EncodeVarint(5, 247) // default end at 247
	}
	if timeoutMs > 0 {
		enc.EncodeVarint(6, uint64(timeoutMs))
	} else {
		enc.EncodeVarint(6, 200) // default 200ms
	}

	if err := m.downlink.Publish(deviceID, enc.Bytes()); err != nil {
		return "", err
	}
	return requestID, nil
}

// SendQueryRequest sends a QueryReq (type=0x0E) to a device
func (m *Manager) SendQueryRequest(deviceID string, queryType uint32) error {
	enc := frame.NewEncoder(frame.MsgQueryReq)
	enc.EncodeString(1, fmt.Sprintf("query-%d", time.Now().UnixMilli()))
	enc.EncodeVarint(2, uint64(queryType))

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendHelloAck sends a nonce-correlated HelloAck (0x12, SVR→ESP).
// handshakeNonce is correlation data, not authentication.
func (m *Manager) SendHelloAck(deviceID string, serverTime uint64, features uint32, handshakeNonce uint32) error {
	if handshakeNonce == 0 {
		return fmt.Errorf("HelloAck requires non-zero handshake nonce")
	}
	enc := frame.NewEncoder(frame.MsgHelloAck)
	enc.EncodeVarint(1, serverTime)
	enc.EncodeVarint(2, uint64(features))
	enc.EncodeVarint(frame.HelloAckFieldHandshakeNonce, uint64(handshakeNonce))

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendConfigQuery sends a ConfigQuery (type=0x10) to a device
func (m *Manager) SendConfigQuery(deviceID string) error {
	enc := frame.NewEncoder(frame.MsgConfigQuery)
	enc.EncodeString(1, fmt.Sprintf("cfgq-%d", time.Now().UnixMilli()))

	return m.downlink.Publish(deviceID, enc.Bytes())
}

// SendQueryResources sends a QueryResources (0x1A) to a device, requesting it to send a ResourceReport.
// Returns the request_id for correlation.
func (m *Manager) SendQueryResources(deviceID string) (string, error) {
	requestID := fmt.Sprintf("res-%d", time.Now().UnixMilli())

	enc := frame.NewEncoder(frame.MsgQueryResources)
	enc.EncodeString(1, requestID)

	if err := m.downlink.Publish(deviceID, enc.Bytes()); err != nil {
		return "", fmt.Errorf("failed to publish QueryResources: %w", err)
	}

	logger.Infof("[%s] QueryResources sent: request_id=%s", deviceID, requestID)
	return requestID, nil
}

// SendDiagAck sends a DiagAck (0x1F, SVR→ESP) for a crash report.
//
// This is the contract the firmware's NVS retention depends on:
//
//	accepted=true  → the report is durably persisted; the device may release
//	                 its NVS copy of record_id.
//	accepted=false → persistence failed; the device MUST keep the record and
//	                 retry. Never send true unless the row is committed.
//
// Mirrors SendQueryResources: encode → downlink.Publish(deviceID).
func (m *Manager) SendDiagAck(deviceID string, recordID uint32, accepted bool) error {
	enc := frame.NewEncoder(frame.MsgDiagAck)
	enc.EncodeVarint(1, uint64(recordID))
	enc.EncodeBool(2, accepted)

	if err := m.downlink.Publish(deviceID, enc.Bytes()); err != nil {
		return fmt.Errorf("failed to publish DiagAck: %w", err)
	}
	return nil
}

// ServerMaxProtocolVersion is the highest protocol version this server supports.
// Negotiated version = min(device-reported, ServerMaxProtocolVersion).
//
// V3-2a (2026-10-05): widened "2.6" → "3.0". parseHello accepts any version
// within [MinSupportedProtocolVersion, ServerMaxProtocolVersion], so a 3.0
// device is no longer rejected outright — before this change the exact-equality
// check refused it and the device could never register.
//
// Behaviour for a 2.6 device is unchanged in every observable way:
//   - it is still accepted;
//   - negotiatedProtocolVersion("2.6") still returns "2.6" (min of the two),
//     so node.protocol_version and every version gate (>= 2.3 / >= 2.4 wire
//     format selection) evaluate exactly as before;
//   - a device reporting a version it cannot back up is not silently upgraded:
//     the negotiated value is min(device, server).
//
// The wire version string is deliberately NOT bumped (contract §0.2): devices
// keep reporting proto_ver=2.6 and DataBatch is enabled purely by the HelloAck
// capability bit, so this server tolerates both an old and a new firmware.
const ServerMaxProtocolVersion = "3.0"

// MinSupportedProtocolVersion is the lowest protocol version this server still
// accepts. It stays at the version currently deployed in the field, so V3-2a is
// strictly an upward widening: a 2.5 Hello is rejected exactly as it was before
// (pinned by TestParseHelloRequiresV26Nonce), and only versions *above* the old
// ceiling become newly acceptable. Widening the floor too would be a separate,
// deliberate compatibility decision — not a side effect of raising the ceiling.
const MinSupportedProtocolVersion = "2.6"

type protocolVersion struct {
	major uint64
	minor uint64
}

// parseProtocolVersion is the single parser used by negotiation and feature
// gates. An optional numeric patch component is accepted but does not affect
// major/minor protocol compatibility.
func parseProtocolVersion(v string) (protocolVersion, bool) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return protocolVersion{}, false
	}
	major, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return protocolVersion{}, false
	}
	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return protocolVersion{}, false
	}
	if len(parts) == 3 {
		if _, err := strconv.ParseUint(parts[2], 10, 32); err != nil {
			return protocolVersion{}, false
		}
	}
	return protocolVersion{major: major, minor: minor}, true
}

func compareProtocolVersions(a, b protocolVersion) int {
	if a.major < b.major || a.major == b.major && a.minor < b.minor {
		return -1
	}
	if a.major == b.major && a.minor == b.minor {
		return 0
	}
	return 1
}

func protocolVersionAtLeast(raw string, minimum protocolVersion) bool {
	version, ok := parseProtocolVersion(raw)
	return ok && compareProtocolVersions(version, minimum) >= 0
}

// parseHardwareID converts a hardware ID string to uint64.
// "0x76" → 118, "5" → 5, "" → 0
//
// ==================== KNOWN DIVERGENCE — READ BEFORE CHANGING ====================
// This is the SECOND hardware_id 口径 in the backend, and it is deliberately NOT
// the gate. The single truth source for "is this a device address" is
// deviceaction.ParseHardwareAddress (deviceaction/definition.go), which returns an
// error and enforces 1..254. parseHardwareID is parse-or-ZERO and never rejects:
//
//	input      parseHardwareID   deviceaction.ParseHardwareAddress
//	"0x76"     118              118            (agree)
//	"5"        5                5              (agree)
//	""         0                1 (default)    (differs: 0 vs legacy default 1)
//	"0"        0                1 (default)    (differs: 0 vs legacy default 1)
//	"255"      255              error (>254)   (differs: encoded vs rejected)
//	"0xFF"     255              error (>254)   (differs)
//	"0x00"     0                error (<1)     (differs)
//	"999999"   999999           error (>254)   (differs: illegal WIRE value)
//	"-1"       0                error          (differs: silently 0)
//	"UART1"    0                error          (differs: the 2026-09-20 incident value)
//
// Where the value goes: sender_snapshot.go encodes it into ConfigManifest field 9
// (edge_device_groups) sub-field 2. The ESP32 firmware stores it in
// config_edge_device_t.hardware_id and copies it into the scheduler, but has no
// consumer that addresses a bus with it — the physical address used on the wire
// comes from the compiled ChannelCmdV2 step, which goes through
// ParseHardwareAddress. So today this field is dead data, and the divergence is a
// latent second 口径 rather than an active mis-addressing bug.
//
// What is NOT fixed here on purpose (2026-09-21, G6 decision): making the mapper
// reject an illegal address would change ConfigManifest behaviour on the wire for
// rows that already hold such values, and "make the encoded value agree with the
// gate" is a protocol change that needs a coordinated firmware decision. The
// divergence is therefore PINNED by TestParseHardwareIDDivergesFromAddressGate so
// it cannot drift silently, and the WRITE side is closed instead: since G1 the
// /nodes/:id/config endpoint validates hardware_id through
// validateEdgeDeviceAddress, so new illegal values cannot be introduced here.
// Legacy rows written before the gate existed are the remaining exposure.
// ===============================================================================
func parseHardwareID(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err == nil {
			return v
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return v
	}
	return 0
}

// SendConfigManifestWithDecision sends a ConfigManifest (0x04) with v2.1 sync metadata.
// The decision carries sync_id, epoch, manifest_id, and reason from SyncGate.
// ProtocolVersion >= 2.3 uses field 9 (edge_device_groups); older versions use field 3+4.
//
// F2: The whole "reconcile → snapshot read → hash → encode" pipeline runs inside
// a single REPEATABLE READ transaction, so the manifestID written into field 1 and
// the encoded bytes (field 3/4/9/11/12) always come from the same snapshot. MQTT
// publish happens AFTER commit to keep the transaction short. reconcile failures
// now abort the whole manifest (fail-closed) instead of warn+continue.
func (m *Manager) SendConfigManifestWithDecision(decision SyncDecision) error {
	deviceID := decision.DeviceID

	// State that must only be acted on after commit.
	type outcome struct {
		snap       *manifestSnapshot
		limits     manifestLimits
		useV2      bool
		channels   []models.Channel
		manifestID string
	}
	var done outcome
	// persistFail marks the node failed. It runs after commit/rollback so it
	// cannot hold the tx lock.
	//
	// The write is deliberately NOT conditional on last_sync_id matching the
	// decision. A rejected manifest never reached the "persist syncing state"
	// step below (that runs only after a successful commit), so the row still
	// carries the PREVIOUS generation id - most often the one written by the
	// last SUCCESSFUL sync, where config_sync_state is already in_sync/applied.
	// Guarding on equality with the current decision therefore matched ZERO
	// rows: the rejection was logged while the database kept advertising
	// "in_sync", hiding every undeliverable config behind a false green.
	// Only the "row already owns THIS generation" case may skip the write,
	// because then a concurrent successful path owns the row.
	fail := func(err error) error {
		logger.Warnf("[sync_id=%s] ConfigManifest rejected: device=%s error=%v", decision.SyncID, deviceID, err)
		result := m.db.Model(&models.Node{}).
			Where("node_id = ? AND (last_sync_id IS NULL OR last_sync_id = '' OR last_sync_id <> ?)", deviceID, decision.SyncID).
			Updates(map[string]interface{}{"config_sync_state": "failed", "config_status": "failed", "last_sync_id": decision.SyncID})
		if result.Error != nil {
			return fmt.Errorf("%w; persist failed sync state: %v", err, result.Error)
		}
		if result.RowsAffected == 0 {
			// Either the node vanished or another path already owns this exact
			// generation. Both deserve visibility: silence here is what made the
			// original false green invisible.
			logger.Warnf("[sync_id=%s] ConfigManifest rejection not persisted: device=%s rows=0 (node missing or generation already owned)", decision.SyncID, deviceID)
		}
		return err
	}

	err := m.db.Transaction(func(tx *gorm.DB) error {
		// F2: REPEATABLE READ on PG so reconcile + snapshot read + hash + encode
		// all observe one consistent snapshot. SQLite is SERIALIZABLE by default
		// (strictly stronger) so the helper is a no-op there.
		SetTransactionIsolation(tx)

		var node models.Node
		if err := tx.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
			return fmt.Errorf("collector not found: %w", err)
		}
		limits, err := manifestLimitsFor(node)
		if err != nil {
			return err
		}

		// Self-healing: reconcile driver CommandTemplates with DB ConfigTemplates.
		// When a driver's command has no matching ConfigTemplate (e.g. device created
		// before createTemplatesFromDriver was deployed), auto-create the missing
		// template. This eliminates the fragile write_data hex-string matching gap.
		// Capacity is checked before any mutation; a mid-reconcile failure returns
		// an error that rolls back the whole transaction (no orphan templates).
		var templates []models.ConfigTemplate
		if err := tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&templates).Error; err != nil {
			return fmt.Errorf("load templates: %w", err)
		}
		if _, err := reconcileDriverTemplates(tx, m.driverRegistry, node.NodeID, templates, limits.maxTemplates); err != nil {
			return fmt.Errorf("reconcile driver templates: %w", err)
		}

		// Snapshot read: hash input and encoded bytes share this exact read set.
		snap, err := m.loadManifestSnapshot(tx, node, nil)
		if err != nil {
			return err
		}

		channels, err := validateManifestAuthority(node, snap.channels, snap.gpioConfigs, snap.pwmConfigs)
		if err != nil {
			return err
		}
		if err := validateManifestTemplateCapacity(snap.templates, limits.maxTemplates); err != nil {
			return err
		}
		useV2 := protocolVersionAtLeast(node.ProtocolVersion, protocolVersion{major: 2, minor: 3})
		if err := validateManifestScheduleCapacityFromSnapshot(snap, m.driverRegistry, channels, useV2, limits); err != nil {
			return err
		}

		// F2 (v2 修订1): the wire manifestID is ALWAYS derived from the post-reconcile
		// snapshot inside this transaction. decision.ManifestID was computed by
		// SyncGate BEFORE reconcile ran, so honoring it would make field 1 differ from
		// the hash of the very bytes being encoded → device-echoed config_hash would
		// never match the server hash → endless Hello re-push loop. Deriving here makes
		// hash input and encoded byte stream same-source by construction.
		derived := m.calcHashFromSnapshot(snap).ManifestID
		if decision.ManifestID != "" && decision.ManifestID != derived {
			logger.Warnf("[sync_id=%s] decision carried stale manifestID %q differs from snapshot-derived %q (reconcile created templates or concurrent change); wire uses the snapshot-derived ID",
				decision.SyncID, decision.ManifestID, derived)
		}
		manifestID := derived

		done = outcome{snap: snap, limits: limits, useV2: useV2, channels: channels, manifestID: manifestID}
		return nil
	})
	if err != nil {
		// Persist the failed sync state on the main connection (after commit).
		return fail(err)
	}

	// Publish AFTER the transaction committed. The manifest bytes come from the
	// committed snapshot, and the syncing state is persisted first so a valid
	// ACK cannot race ahead of backend authority.
	snap := done.snap
	payload, err := encodeConfigManifest(snap, done.channels, done.useV2, decision, m.driverRegistry, done.manifestID)
	if err != nil {
		return fail(fmt.Errorf("encode config manifest: %w", err))
	}

	// R1 byte gate: the encoded manifest must fit in ONE downlink message.
	// ⚠ See MaxManifestWireBytes: the bound was DERIVED from the MQTT receive
	// buffer and has not been re-derived for the 3.0 TCP path. It is kept
	// unchanged (conservative) rather than silently widened.
	// Checked on the exact bytes about to be published (same snapshot, same
	// encode call — never a re-encode), before any state is marked "syncing",
	// so an undeliverable manifest is rejected with a diagnosable error and the
	// node is driven to config_status=failed by fail() below instead of the
	// backend falsely reporting a successful publish.
	if err := checkManifestWireBytes(len(payload)); err != nil {
		return fail(err)
	}

	// Persist the exact generation before publish so an immediate valid ACK
	// cannot race ahead of backend authority.
	now := time.Now()
	if err := m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"config_version": done.manifestID, "config_sync_state": "syncing",
		"last_sync_at": now, "last_sync_id": decision.SyncID,
	}).Error; err != nil {
		return fail(fmt.Errorf("persist syncing state: %w", err))
	}

	if err := m.downlink.Publish(deviceID, payload); err != nil {
		logger.Infof("[%s] Failed to send config: %v", deviceID, err)
		return fail(fmt.Errorf("publish config manifest: %w", err))
	}
	logger.Infof("[sync_id=%s] ConfigManifest sent: device=%s id=%s reason=%s %d templates, %d channels",
		decision.SyncID, deviceID, done.manifestID, decision.Reason, len(snap.templates), len(done.channels))
	return nil
}

// validateManifestScheduleCapacityFromSnapshot mirrors config_mgr's fixed arrays for the
// selected wire format. It deliberately counts exactly what encodeConfigManifest will
// encode, including enabled edge devices on a disabled transport channel. See
// sender_snapshot.go — it uses the shared snapshot (single edges query).
