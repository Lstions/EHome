package nodemgr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/uartcfg"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// manifestSnapshot is the consistent read set used for both config hash
// computation and ConfigManifest encoding. It is loaded inside a single
// REPEATABLE READ transaction so the manifestID's hash input and the encoded
// byte stream come from the same snapshot — that is what makes the device's
// echoed config_hash ever match the server (F2: manifest 快照化 + hash 一致性).
type manifestSnapshot struct {
	node           models.Node
	templates      []models.ConfigTemplate
	channels       []models.Channel
	edgeDevices    []models.EdgeDevice
	deviceConfigs  []models.DeviceConfig
	dmaConfigs     []models.DmaChannelConfig
	gpioConfigs    []models.GPIOConfig
	pwmConfigs     []models.PWMConfig
	edgesByChannel map[uint][]models.EdgeDevice // enabled edges grouped by channel (single query)

	// driverCommands caches driver-declared command templates per edge.Type.
	// 2026-10-09 (§206.3): pruneUnreferencedTemplates needs the SAME command set
	// the encoder uses, otherwise it would prune templates still in use.
	driverCommands map[string][]drivers.CommandTemplate
}

// loadManifestSnapshot reads every entity that participates in hash calculation
// or manifest encoding, using the same query orders as buildHashData. When
// templates is nil it loads them; callers that reconciled inside the same
// transaction pass a nil slice to force a reload. edgeDevices is loaded once
// and grouped by channel, replacing the previous duplicate per-channel edge
// queries in the encoder and validateManifestScheduleCapacity.
func (m *Manager) loadManifestSnapshot(tx *gorm.DB, node models.Node, templates []models.ConfigTemplate) (*manifestSnapshot, error) {
	snap := &manifestSnapshot{
		node:           node,
		edgesByChannel: make(map[uint][]models.EdgeDevice),
		driverCommands: make(map[string][]drivers.CommandTemplate),
	}
	if templates == nil {
		if err := tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&snap.templates).Error; err != nil {
			return nil, fmt.Errorf("load templates: %w", err)
		}
	} else {
		snap.templates = templates
	}
	if err := tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&snap.channels).Error; err != nil {
		return nil, fmt.Errorf("load channels: %w", err)
	}
	if err := tx.Order("id ASC").Where("node_id = ? AND enabled = true", node.NodeID).Find(&snap.edgeDevices).Error; err != nil {
		return nil, fmt.Errorf("load edge devices: %w", err)
	}
	deviceConfigIDs := make([]uint, 0, len(snap.edgeDevices))
	seenDeviceConfigIDs := make(map[uint]struct{}, len(snap.edgeDevices))
	for _, ed := range snap.edgeDevices {
		if ed.DeviceConfigID > 0 {
			if _, ok := seenDeviceConfigIDs[ed.DeviceConfigID]; !ok {
				seenDeviceConfigIDs[ed.DeviceConfigID] = struct{}{}
				deviceConfigIDs = append(deviceConfigIDs, ed.DeviceConfigID)
			}
		}
	}
	if len(deviceConfigIDs) > 0 {
		if err := tx.Order("id ASC").Where("id IN ?", deviceConfigIDs).Find(&snap.deviceConfigs).Error; err != nil {
			return nil, fmt.Errorf("load device configs: %w", err)
		}
	}
	snap.dmaConfigs = parseManifestDMAConfigs(node)
	if err := tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&snap.gpioConfigs).Error; err != nil {
		return nil, fmt.Errorf("load GPIO configs: %w", err)
	}
	if err := tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&snap.pwmConfigs).Error; err != nil {
		return nil, fmt.Errorf("load PWM configs: %w", err)
	}
	for _, ed := range snap.edgeDevices {
		snap.edgesByChannel[ed.ChannelID] = append(snap.edgesByChannel[ed.ChannelID], ed)
	}
	// Populate the driver-command cache so prune uses the SAME command set as
	// the encoder (one registry lookup per distinct edge.Type).
	for _, ed := range snap.edgeDevices {
		if _, ok := snap.driverCommands[ed.Type]; ok {
			continue
		}
		var drv drivers.Driver
		if m.driverRegistry != nil {
			drv, _ = m.driverRegistry.Get(ed.Type)
		}
		snap.driverCommands[ed.Type] = getCommandTemplatesFromDriver(drv)
	}
	pruneUnreferencedTemplates(snap)
	return snap, nil
}

// pruneUnreferencedTemplates 丢弃"不被任何**启用**边设备引用"的模板。
//
// 2026-10-09（§206.3，真机证实）：此前 snap.templates 与编码循环都不看 enabled，
// 于是 enabled=false 的边设备仍然：
//
//	① 创建时由 createTemplatesFromDriver 建了 ConfigTemplate（它不看 dev.Enabled）；
//	② 该模板被无条件编进 ConfigManifest（本文件 field 3 的循环）。
//
// 真机证据（构造 1 个 enabled=false 设备 + 1 个挂到 UART1 的模板）：
//
//	后端 "ConfigManifest sent: ... 1 templates, 3 channels"
//	设备侧解析到的 edge_device 数 = 0
//
// ⇒ 设备收到 0 个从机却带 1 个模板 = 无引用的死数据，白占固件 MAX_TEMPLATES=16。
//
//	与 §205.3 那个 "18 > 16" 现场事故是同一后果（都是"建了不需要的模板"）。
//
// ⚠ 为什么是"丢弃无引用"而不是"删除禁用设备的模板"：
//
//	ConfigTemplate 是共享池 —— 多条通道/多个设备可共用同一 write_data 的模板
//	（models.ConfigTemplate.EdgeDeviceID 可空，注释写明"multi-drop 共享池留 NULL"）。
//	所以判据必须是"有没有被启用设备引用"，不能按 owner 删。
//
// ⚠ 归属列 EdgeDeviceID 不足以单独判定：reconcile 自愈路径建的无归属模板
//
//	（EdgeDeviceID == NULL）可能正被启用设备使用 ⇒ 必须按引用关系判定。
//
// 引用来源取两条路径的并集，与编码器口径一致：
//
//	· v2 多命令路径：启用设备的驱动命令（Schedulable && interval>0）→ write_data 匹配；
//	· legacy 单命令路径：通道 template_ids 里的 id。
func pruneUnreferencedTemplates(snap *manifestSnapshot) {
	// 无启用设备 ⇒ 没有任何模板该下发（设备只会收到 0 个从机组）。
	if len(snap.edgeDevices) == 0 {
		snap.templates = nil
		return
	}
	used := make(map[uint64]struct{}, len(snap.templates))
	usedWrite := make(map[string]struct{})

	chByID := make(map[uint]models.Channel, len(snap.channels))
	for _, ch := range snap.channels {
		chByID[ch.ID] = ch
	}
	// 模板按归一化 write_data 建索引，供 v2 路径反查。
	byWrite := make(map[string]uint64, len(snap.templates))
	for _, t := range snap.templates {
		byWrite[strings.ToUpper(strings.TrimSpace(t.WriteData))] = uint64(t.ID)
	}

	for _, edge := range snap.edgeDevices {
		// ① legacy 单命令路径：通道 template_ids 引用的 id。
		if ch, ok := chByID[edge.ChannelID]; ok {
			if id := findTemplateID(ch, edge); id != 0 {
				used[id] = struct{}{}
			}
		}
		// ② v2 多命令路径：该设备"会被轮询"的命令的 write_data。
		//    ⚠ 这里必须用与编码器**同一份**驱动命令集与 interval 口径，
		//    否则会把编码器仍要用的模板剪掉（剪多 = 静默少下发）。
		intervals := make(map[string]int)
		if len(edge.CommandIntervals) > 0 {
			_ = json.Unmarshal(edge.CommandIntervals, &intervals)
		}
		for _, cmd := range snap.driverCommands[edge.Type] {
			if !CommandIsManifestCandidate(cmd, intervals, snap.templates) {
				continue
			}
			w := strings.ToUpper(strings.TrimSpace(cmd.WriteData))
			usedWrite[w] = struct{}{}
			if id, ok := byWrite[w]; ok {
				used[id] = struct{}{}
			}
		}
	}

	kept := make([]models.ConfigTemplate, 0, len(snap.templates))
	for _, t := range snap.templates {
		if _, ok := used[uint64(t.ID)]; ok {
			kept = append(kept, t)
			continue
		}
		// 兜底：write_data 被某启用设备引用但 id 索引未命中（重复 write_data 时
		// byWrite 只留最后一个）⇒ 按 write_data 保留，宁可多留也不剪掉在用的。
		if _, ok := usedWrite[strings.ToUpper(strings.TrimSpace(t.WriteData))]; ok {
			kept = append(kept, t)
		}
	}
	snap.templates = kept
}

// parseManifestDMAConfigs extracts dma_configs from node.Config JSON. Shared
// between hash calculation and manifest encoding so both operate on the same
// parsed slice.
func parseManifestDMAConfigs(node models.Node) []models.DmaChannelConfig {
	var dmaConfigs []models.DmaChannelConfig
	if node.Config == "" {
		return dmaConfigs
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(node.Config), &cfg); err != nil {
		logger.Warnf("[%s] Failed to parse node.Config JSON in sender: %v", node.NodeID, err)
		return dmaConfigs
	}
	dc, ok := cfg["dma_configs"]
	if !ok {
		return dmaConfigs
	}
	dcJSON, err := json.Marshal(dc)
	if err != nil {
		return dmaConfigs
	}
	if err := json.Unmarshal(dcJSON, &dmaConfigs); err != nil {
		logger.Warnf("[%s] Failed to parse dma_configs for sender: %v", node.NodeID, err)
		return dmaConfigs
	}
	logger.Infof("[%s] Loaded %d dma_configs from node.Config", node.NodeID, len(dmaConfigs))
	return dmaConfigs
}

// calcHashFromSnapshot computes the deterministic config hash and manifest ID
// for a snapshot. This is the single source of truth for manifest identity:
// every ConfigManifest encoded from a snapshot derives its manifestID from
// here (when the decision does not already carry one), so the device-echoed
// hash always matches the server hash for an unchanged database.
func (m *Manager) calcHashFromSnapshot(snap *manifestSnapshot) ConfigHashResult {
	hashData := m.buildHashData(snap.templates, snap.channels, snap.edgeDevices, snap.deviceConfigs, snap.dmaConfigs, snap.gpioConfigs, snap.pwmConfigs)
	// v2.5: include log_stream config in hash so changes trigger manifest push
	hashData = append(hashData, []byte(fmt.Sprintf("ls:%v:%d:", snap.node.LogStreamEnabled, snap.node.LogStreamLevel))...)
	hash := m.hashMgr.CalcConfigHash(hashData)
	return ConfigHashResult{
		Hash:         hash,
		ManifestID:   fmt.Sprintf("v2-%s", hash),
		ChannelCount: len(snap.channels),
	}
}

// validateManifestScheduleCapacityFromSnapshot mirrors config_mgr's fixed
// arrays for the selected wire format, counting exactly what
// encodeConfigManifest will emit. It reads edge devices from the snapshot
// (single query) instead of issuing a per-channel DB query.
func validateManifestScheduleCapacityFromSnapshot(snap *manifestSnapshot, registry *drivers.Registry, channels []models.Channel, useV2 bool, limits manifestLimits) error {
	if len(channels) > limits.maxChannels {
		return fmt.Errorf("manifest has %d channels; collector limit is %d", len(channels), limits.maxChannels)
	}
	for _, channel := range channels {
		if !useV2 {
			count := 0
			for _, value := range strings.Split(channel.TemplateIDs, ",") {
				if strings.TrimSpace(value) != "" {
					count++
				}
			}
			if count > limits.maxTemplateIDs {
				return fmt.Errorf("channel %d has %d template ids; collector limit is %d", channel.ID, count, limits.maxTemplateIDs)
			}
			continue
		}

		edges := snap.edgesByChannel[channel.ID]
		if len(edges) > maxEdgeDevicesPerChannel {
			return fmt.Errorf("channel %d has %d edge devices; collector limit is %d", channel.ID, len(edges), maxEdgeDevicesPerChannel)
		}
		for _, edge := range edges {
			var drv drivers.Driver
			if registry != nil {
				drv, _ = registry.Get(edge.Type)
			}
			commandCount := 0 // F3: legacy single-command only counts when a valid template_id exists.
			driverCommands := getCommandTemplatesFromDriver(drv)
			if len(driverCommands) > 0 {
				intervals := make(map[string]int)
				if len(edge.CommandIntervals) > 0 {
					_ = json.Unmarshal(edge.CommandIntervals, &intervals)
				}
				for _, command := range driverCommands {
					if CommandIsManifestCandidate(command, intervals, snap.templates) {
						commandCount++
					}
				}
			} else if findTemplateID(channel, edge) != 0 && manifestTemplateExists(snap.templates, findTemplateID(channel, edge)) {
				// Legacy single-command branch: encoded only when the channel's
				// template_ids resolves to a template that exists in this snapshot.
				commandCount = 1
			}
			if commandCount > MaxCommandsPerEdgeDevice {
				return fmt.Errorf("edge device %d on channel %d has %d commands; collector limit is %d", edge.ID, channel.ID, commandCount, MaxCommandsPerEdgeDevice)
			}
		}
	}
	return nil
}

// CommandIsManifestCandidate reports whether a driver command would be encoded
// as a per-command sub-frame in the ConfigManifest: Schedulable, effective
// interval (stored override → template default) > 0, and a matching
// ConfigTemplate exists in the snapshot.
// 演进方案 C4: this predicate is the single authority for the "manifest
// candidate set" of the four-set contract test.
func CommandIsManifestCandidate(command drivers.CommandTemplate, storedIntervals map[string]int, templates []models.ConfigTemplate) bool {
	if !command.Schedulable {
		return false
	}
	interval := command.IntervalMs
	if value, ok := storedIntervals[command.ID]; ok {
		interval = value
	}
	return interval > 0 && findTemplateIDForCommand(templates, command.WriteData) != 0
}

// manifestTemplateExists reports whether a template with the given ID is
// present in the snapshot's template set. Used by the legacy single-command
// branch to refuse dangling template_id references (F3).
func manifestTemplateExists(templates []models.ConfigTemplate, id uint64) bool {
	for _, t := range templates {
		if uint64(t.ID) == id {
			return true
		}
	}
	return false
}

// encodeConfigManifest produces the ConfigManifest (0x04) wire bytes from a
// single consistent snapshot. manifestID is written verbatim into field 1, so
// the identifier SyncGate derived (or that was computed from this snapshot via
// calcHashFromSnapshot) and the encoded bytes are guaranteed same-source.
// ProtocolVersion >= 2.3 uses field 9 (edge_device_groups); older versions use
// field 3+4.
func encodeConfigManifest(snap *manifestSnapshot, channels []models.Channel, useV2 bool, decision SyncDecision, registry *drivers.Registry, manifestID string) ([]byte, error) {
	node := snap.node
	deviceID := decision.DeviceID

	enc := frame.NewEncoder(frame.MsgConfigMfst)
	enc.EncodeString(1, manifestID)

	// v2.2: field 2 epoch removed, field 9 sync_reason removed

	// Encode templates (field 3, repeated sub-structure)
	// v2 path still needs templates — C6's schedule_v2_channel uses
	// config_mgr_get_template(template_id) to look up write_data/read_length/delay_ms.
	for _, tmpl := range snap.templates {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(tmpl.ID))
		if tmpl.WriteData != "" {
			writeHex := tmpl.WriteData
			if strings.HasPrefix(writeHex, `\x`) || strings.HasPrefix(writeHex, "0x") {
				writeHex = writeHex[2:]
			}
			if writeBytes, err := hex.DecodeString(writeHex); err == nil && len(writeBytes) > 0 {
				subEnc.EncodeBytes(2, writeBytes)
			}
		}
		if tmpl.ReadLength > 0 {
			subEnc.EncodeVarint(3, uint64(tmpl.ReadLength))
		}
		if tmpl.DelayMs > 0 {
			subEnc.EncodeVarint(4, uint64(tmpl.DelayMs))
		}
		enc.EncodeSubFrame(3, subEnc.Bytes())
	}

	// Encode channels (field 4, repeated sub-structure)
	for _, ch := range channels {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(ch.ID))

		if !useV2 {
			// Old path: field 2 hardware_id, field 3 template_ids, field 4 interval_ms
			subEnc.EncodeString(2, ch.HardwareID)

			// Packed repeated template_ids (field 3)
			if ch.TemplateIDs != "" {
				for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
					if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
						subEnc.EncodeVarint(3, id)
					}
				}
			}

			subEnc.EncodeVarint(4, uint64(ch.IntervalMs))
		}

		subEnc.EncodeBool(5, ch.Enabled)

		// Bus type — these numbers are the firmware's BUS_TYPE_* values and must
		// stay in lockstep with esp32-collector/components/bus_dma/include/bus_dma.h.
		// A type missing here is silently encoded as nothing (field omitted, firmware
		// sees 0 = unknown), which is why USB had to be added alongside its driver.
		busTypeMap := map[string]uint8{
			"UART": 1, "1": 1,
			"I2C": 2, "2": 2,
			"SPI": 3, "3": 3,
			// USB is string-only: "4" is not accepted as an alias anywhere on this
			// path either, because legacy numeric channels reuse 4 for GPIO.
			"USB": 4,
			"ADC": 5, "5": 5,
		}
		/* ⭐ 记住解析结果：下面"UART 的 bus_config 必须 >= 6 字节"那条补全要用到它。
		 * ⚠ 用 busTypeMap 的**同一结果**而不是另写一次 EqualFold —— 两处判断一旦漂移，
		 *   就会出现"编码时按 A 类型、补全时按 B 类型"的静默错配（P4：同一语义一份定义）。 */
		encodedBusType, hasBusType := busTypeMap[strings.ToUpper(ch.BusType)]
		if hasBusType {
			subEnc.EncodeVarint(6, uint64(encodedBusType))
		}

		// Bus config
		busConfigData := ch.BusConfig
		if busConfigData == "" {
			busConfigData = ch.Config
		}
		if busConfigData != "" {
			/* Try hex decode first — PostgreSQL bytea may already be binary in-memory */
			var decodedBytes []byte
			switch {
			case strings.HasPrefix(busConfigData, `\x`):
				decodedBytes, _ = hex.DecodeString(busConfigData[2:])
			default:
				if d, err := hex.DecodeString(busConfigData); err == nil && len(d) > 0 {
					decodedBytes = d
				}
			}

			/* ⭐ 2026-10-07（真机实测缺陷）：UART 的 bus_config 必须 >= 6 字节才下发。
			 *
			 * 固件判据（bus_manager.c:443）：
			 *   `if (ch->bus_config_len < 6) return ESP_ERR_INVALID_SIZE;`
			 * 它必须读到 byte2..5 的 big-endian 波特率。而**后端此前原样下发**，于是：
			 *   接口 201「通道创建成功」⇒ manifest 下发 ⇒ 设备
			 *   `BUS_MGR: preinstall rejected by resource plan` ⇒ `ConfigResult success=0`；
			 *   而**操作员在界面上看到的是成功**（本仓反复记的"后端说成功、设备静默失败"）。
			 *
			 * ⇒ 这里补齐，而**不是**在入库时拒绝短值：2 字节是合法的"只配了引脚、还没配
			 *   波特率"，仿真套件与存量库都在用（channel_update_uart_busconfig_test.go 的
			 *   TestChannelUpdate_UART2ByteRouteAccepted 是 P0 护栏，且断言**调用方给的值必须
			 *   原样保留**）。⇒ 入库保持原样、**下发时**补全，两个契约各自成立。
			 *
			 * ⇒ 引脚沿用已有字节；波特率用 9600（defaultUARTBaudrate，与建通道兜底同一档）；
			 *   DMA 位为 0（不擅自替用户开 DMA —— 该位由独立的 dma_enabled 字段承载）。
			 * ⚠ 已 >= 6 字节时**逐字节不动**：否则会篡改用户已配的波特率/DMA 位。 */
			const busTypeUART = 1 /* 与 busTypeMap 的 "UART" 同值，取自同一张表的语义 */
			if hasBusType && encodedBusType == busTypeUART {
				/* 补齐逻辑走 internal/uartcfg —— 与 api 侧**同一份实现**（P4）。
				 * 已 >= MinLen 时返回 (nil,false) ⇒ 逐字节保留用户已配的波特率/DMA 位。 */
				if padded, changed := uartcfg.PadShortUART(decodedBytes); changed {
					logger.Warnf("channel %d UART bus_config 仅 %d 字节（< 固件下限 %d）⇒ 下发前补默认波特率 %d 到 %d 字节",
						ch.ID, len(decodedBytes), uartcfg.MinLen, uartcfg.DefaultBaudrate, len(padded))
					decodedBytes = padded
				}
			}

			if decodedBytes != nil {
				subEnc.EncodeBytes(7, decodedBytes)
			} else {
				subEnc.EncodeString(7, busConfigData)
			}
		}

		// Field 8: dma_enabled
		subEnc.EncodeBool(8, ch.DmaEnabled)

		if useV2 {
			// New path: field 9 edge_device_groups (repeated sub-messages).
			// Edges come from the shared snapshot — single query, no per-channel re-read.
			edges := snap.edgesByChannel[ch.ID]
			logger.Infof("[%s] ConfigManifest ch=%d: useV2=true, found %d edge_devices", deviceID, ch.ID, len(edges))

			for _, edge := range edges {
				grpEnc := frame.SubEncoder()
				grpEnc.EncodeVarint(1, uint64(edge.ID))
				grpEnc.EncodeVarint(2, parseHardwareID(edge.HardwareID))

				// Per-command intervals: check driver CommandTemplates for multi-command support
				cmdIntervals := make(map[string]int)
				if len(edge.CommandIntervals) > 0 {
					json.Unmarshal(edge.CommandIntervals, &cmdIntervals)
				}
				var drv drivers.Driver
				if registry != nil {
					drv, _ = registry.Get(edge.Type)
				}
				driverCmds := getCommandTemplatesFromDriver(drv)

				if len(driverCmds) > 0 {
					// Multiple commands: encode only Schedulable commands
					for _, t := range driverCmds {
						if !t.Schedulable {
							continue // one-shot trigger, not for ConfigManifest
						}
						interval := t.IntervalMs
						if v, ok := cmdIntervals[t.ID]; ok {
							interval = v
						}
						if interval <= 0 {
							continue // disabled
						}
						tmplID := findTemplateIDForCommand(snap.templates, t.WriteData)
						if tmplID == 0 {
							logger.Warnf("[%s] No ConfigTemplate found for command %s, skipping",
								deviceID, t.ID)
							continue
						}
						cmdEnc := frame.SubEncoder()
						cmdEnc.EncodeVarint(1, tmplID)
						cmdEnc.EncodeVarint(2, uint64(interval))
						cmdEnc.EncodeBool(3, true)
						grpEnc.EncodeSubFrame(3, cmdEnc.Bytes())
					}
				} else {
					// Single command: use edge default interval
					interval := edge.IntervalMs
					if v, ok := cmdIntervals["default"]; ok {
						interval = v
					}
					tmplID := findTemplateID(ch, edge)
					if tmplID == 0 || !manifestTemplateExists(snap.templates, tmplID) {
						// F3: no usable template_id (channel.template_ids empty,
						// dangling, or unparseable). Never encode varint 0 (wire-legal
						// but meaningless on the device) nor a dangling reference —
						// skip this edge's command sub-frame. The edge group below is
						// still encoded via EncodeSubFrame(9) with zero commands; the
						// firmware parses an empty command group (command_count=0) and
						// schedules nothing for it, so the device stops sampling this
						// edge until the channel's template_ids are repaired.
						logger.Warnf("[%s] edge_device %d (channel %d): no valid template_id in channel.template_ids %q (resolved %d), skipping command encoding", deviceID, edge.ID, ch.ID, ch.TemplateIDs, tmplID)
						metrics.ManifestCommandSkippedNoTemplate.WithLabelValues(deviceID).Inc()
					} else {
						cmdEnc := frame.SubEncoder()
						cmdEnc.EncodeVarint(1, tmplID)
						cmdEnc.EncodeVarint(2, uint64(interval))
						cmdEnc.EncodeBool(3, edge.Enabled)
						grpEnc.EncodeSubFrame(3, cmdEnc.Bytes())
					}
				}

				subEnc.EncodeSubFrame(9, grpEnc.Bytes())
			}
		}

		enc.EncodeSubFrame(4, subEnc.Bytes())
	}

	// Field 5: dma_channel_configs (repeated DmaChannelConfig sub-messages)
	for _, dc := range snap.dmaConfigs {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(dc.DmaID))
		enabled := uint64(0)
		if dc.Enabled {
			enabled = 1
		}
		subEnc.EncodeVarint(2, enabled)
		if dc.BindTo != "" {
			subEnc.EncodeString(3, dc.BindTo)
		}
		enc.EncodeSubFrame(5, subEnc.Bytes())
	}

	// v2.5: field 10 = log_stream config (sub-frame: 1=enabled, 2=level)
	{
		lsEnc := frame.SubEncoder()
		lsEnc.EncodeBool(1, node.LogStreamEnabled)
		lsEnc.EncodeVarint(2, uint64(node.LogStreamLevel))
		enc.EncodeSubFrame(10, lsEnc.Bytes())
	}

	// Field 11: gpio_configs (repeated sub-messages, v3.0)
	// Only encode for protocol_version >= 2.4 (older firmware ignores unknown fields)
	if protocolVersionAtLeast(node.ProtocolVersion, protocolVersion{major: 2, minor: 4}) {
		for _, gc := range snap.gpioConfigs {
			if !gc.Enabled {
				continue // hash input includes disabled rows; the wire only carries enabled ones
			}
			subEnc := frame.SubEncoder()
			subEnc.EncodeVarint(1, uint64(gc.Pin))          // sub-field 1: pin
			subEnc.EncodeVarint(2, uint64(gc.Direction))    // sub-field 2: direction
			subEnc.EncodeVarint(3, uint64(gc.InitialLevel)) // sub-field 3: initial_level
			enc.EncodeSubFrame(11, subEnc.Bytes())
		}

		// Field 12: pwm_configs (repeated sub-messages, v3.0)
		pwmForEncode := make([]models.PWMConfig, 0, len(snap.pwmConfigs))
		for _, pc := range snap.pwmConfigs {
			if pc.Enabled {
				pwmForEncode = append(pwmForEncode, pc)
			}
		}
		// Preserve the historical deterministic encode order: channel ASC, hardware_id ASC.
		sort.Slice(pwmForEncode, func(i, j int) bool {
			if pwmForEncode[i].Channel != pwmForEncode[j].Channel {
				return pwmForEncode[i].Channel < pwmForEncode[j].Channel
			}
			return pwmForEncode[i].HardwareID < pwmForEncode[j].HardwareID
		})
		for _, pc := range pwmForEncode {
			subEnc := frame.SubEncoder()
			subEnc.EncodeVarint(1, uint64(pc.Channel))    // sub-field 1: channel
			subEnc.EncodeVarint(2, uint64(pc.Pin))        // sub-field 2: pin
			subEnc.EncodeVarint(3, uint64(pc.Frequency))  // sub-field 3: frequency
			subEnc.EncodeVarint(4, uint64(pc.Duty))       // sub-field 4: duty
			subEnc.EncodeVarint(5, uint64(pc.Resolution)) // sub-field 5: resolution
			subEnc.EncodeBool(6, pc.AutoStart)            // sub-field 6: auto_start
			enc.EncodeSubFrame(12, subEnc.Bytes())
		}
	}

	// v2.2: field 8 = sync_id (field 9 sync_reason removed)
	enc.EncodeString(8, decision.SyncID)

	payload := enc.Bytes()

	// DEBUG: Log first 120 bytes of ConfigManifest hex
	hexLen := len(payload)
	if hexLen > 120 {
		hexLen = 120
	}
	logger.Infof("[%s] ConfigManifest hex (%d bytes): %s", deviceID, len(payload), hex.EncodeToString(payload[:hexLen]))
	return payload, nil
}
