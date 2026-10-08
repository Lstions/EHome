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
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// manifestCapabilities is the subset of a node's ResourceReport that governs
// which GPIO pins / PWM resources may appear in a ConfigManifest. Binding is
// fail-closed: any GPIO/PWM config whose resource is absent from the report
// rejects the whole manifest.
type manifestCapabilities struct {
	Buses struct {
		GPIO []struct {
			Pin int `json:"pin"`
		} `json:"gpio"`
		PWM []struct {
			ID                string `json:"id"`
			Channel           uint8  `json:"channel"`
			MaxResolutionBits uint8  `json:"max_resolution_bits"`
		} `json:"pwm"`
	} `json:"buses"`
}

// normalizedManifestBusType maps a channel bus type (name or legacy numeric)
// to the canonical wire name. The third return value reports whether the type
// is a peripheral (GPIO/PWM) — peripheral channels are not encoded as
// transport channels and their legacy enabled form is a hard error.
func normalizedManifestBusType(value string) (string, bool, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "UART", "1":
		return "UART", true, false
	case "I2C", "2":
		return "I2C", true, false
	case "SPI", "3":
		return "SPI", true, false
	case "GPIO", "4":
		return "GPIO", true, true
	case "ADC", "5":
		return "ADC", true, false
	// USB: ESP32-C6 native USB (USB-Serial-JTAG / USB-CDC) used as a data bus,
	// firmware BUS_TYPE_USB. Deliberately string-only: the firmware bus_type
	// numbering (UART=1, I2C=2, SPI=3, USB=4, ADC=5, PWM=6, GPIO=7) is NOT the
	// legacy numbered alias set this function accepts (here "4" = GPIO), so any
	// numeric alias for USB would collide with an existing meaning on the wire.
	// Peripheral=false: USB is a transport, never a GPIO/PWM resource.
	case "USB":
		return "USB", true, false
	case "PWM", "6":
		return "PWM", true, true
	default:
		return "", false, false
	}
}

// decodeManifestTransportPins extracts the GPIO pins occupied by a transport
// channel from its hex bus_config, so authority checks can detect conflicts
// between channels, GPIO configs and PWM routing.
func decodeManifestTransportPins(ch models.Channel, busType string) ([]int, error) {
	raw := strings.TrimSpace(ch.BusConfig)
	raw = strings.TrimPrefix(raw, `\x`)
	data, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
	}
	switch busType {
	case "UART", "I2C":
		if len(data) < 2 {
			return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		return []int{int(data[0]), int(data[1])}, nil
	case "SPI":
		if len(data) != 9 {
			return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		pins := []int{int(data[0])}
		if len(data) >= 9 {
			pins = append(pins, int(data[6]), int(data[7]), int(data[8]))
		}
		return pins, nil
	case "ADC":
		return nil, nil
	// USB carries no GPIO route: a CDC/JTAG data bus has no tx/rx pins and no
	// baudrate, so there is nothing for the pin-authority check to claim.
	// bus_config may therefore be empty OR any length — a host-side length gate
	// here would only reject manifests the collector accepts (config_mgr's
	// channel_uses_pin returns false for unknown bus types, and
	// bus_config_get_dma_enabled defaults to true). Non-hex text is still
	// rejected by the decode above, exactly as for every other bus type.
	case "USB":
		return nil, nil
	default:
		return nil, fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
	}
}

// validateManifestAuthority verifies that every transport channel, GPIO and
// PWM config that will be encoded is backed by the node's current
// ResourceReport, and that no two encoded resources claim the same GPIO pin.
// Returns the channels that are legal to encode (peripheral/disabled channels
// filtered out).
func validateManifestAuthority(node models.Node, allChannels []models.Channel, gpios []models.GPIOConfig, pwms []models.PWMConfig) ([]models.Channel, error) {
	var caps manifestCapabilities
	if strings.TrimSpace(node.Capabilities) == "" || json.Unmarshal([]byte(node.Capabilities), &caps) != nil {
		return nil, fmt.Errorf("node has not reported usable hardware resources")
	}
	gpioPins := make(map[int]bool, len(caps.Buses.GPIO))
	for _, resource := range caps.Buses.GPIO {
		gpioPins[resource.Pin] = true
	}
	pwmResources := make(map[string]struct{ channel, max uint8 }, len(caps.Buses.PWM))
	for _, resource := range caps.Buses.PWM {
		pwmResources[resource.ID] = struct{ channel, max uint8 }{resource.Channel, resource.MaxResolutionBits}
	}
	owners := make(map[int]string)
	claim := func(pin int, owner string) error {
		if prior, exists := owners[pin]; exists {
			return fmt.Errorf("GPIO pin %d conflict between %s and %s", pin, prior, owner)
		}
		owners[pin] = owner
		return nil
	}
	channels := make([]models.Channel, 0, len(allChannels))
	for _, ch := range allChannels {
		busType, known, peripheral := normalizedManifestBusType(ch.BusType)
		if !known {
			busType, known, peripheral = normalizedManifestBusType(ch.HardwareType)
		}
		if peripheral {
			if ch.Enabled {
				return nil, fmt.Errorf("legacy peripheral channel %d is still enabled", ch.ID)
			}
			continue
		}
		if !known {
			if ch.Enabled {
				return nil, fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
			}
			channels = append(channels, ch)
			continue
		}
		if ch.Enabled {
			pins, err := decodeManifestTransportPins(ch, busType)
			if err != nil {
				return nil, err
			}
			for _, pin := range pins {
				if err := claim(pin, fmt.Sprintf("channel %d", ch.ID)); err != nil {
					return nil, err
				}
			}
		}
		channels = append(channels, ch)
	}
	for _, cfg := range gpios {
		if !cfg.Enabled {
			continue // disabled GPIOs are not encoded into the manifest; hash input includes them but authority only governs the wire
		}
		if !gpioPins[cfg.Pin] {
			return nil, fmt.Errorf("GPIO pin %d is absent from current ResourceReport", cfg.Pin)
		}
		if cfg.Direction > 3 || cfg.InitialLevel > 1 {
			return nil, fmt.Errorf("GPIO pin %d has invalid scalar configuration", cfg.Pin)
		}
		if err := claim(cfg.Pin, fmt.Sprintf("GPIO %d", cfg.Pin)); err != nil {
			return nil, err
		}
	}
	for _, cfg := range pwms {
		if !cfg.Enabled {
			continue // disabled PWM configs are not encoded into the manifest; authority only governs the wire
		}
		resource, ok := pwmResources[cfg.HardwareID]
		if !ok || resource.channel != cfg.Channel {
			return nil, fmt.Errorf("PWM resource %q no longer matches current ResourceReport", cfg.HardwareID)
		}
		if !gpioPins[cfg.Pin] {
			return nil, fmt.Errorf("PWM %s route GPIO %d is absent from current ResourceReport", cfg.HardwareID, cfg.Pin)
		}
		if cfg.Duty > 10000 || cfg.Frequency == 0 || cfg.Resolution < 4 || cfg.Resolution > 20 ||
			resource.max == 0 || cfg.Resolution > resource.max || uint64(cfg.Frequency)*(uint64(1)<<cfg.Resolution) > 40000000 {
			return nil, fmt.Errorf("PWM %s has infeasible scalar configuration", cfg.HardwareID)
		}
		if err := claim(cfg.Pin, fmt.Sprintf("PWM %s", cfg.HardwareID)); err != nil {
			return nil, err
		}
	}
	// ⚠ 2026-10-09（§198）：到这里 owners 已收齐**全部**将要编码的引脚
	//（UART/I2C/SPI 通道 + GPIO + PWM），正是检查"保留脚"的唯一合适位置。
	// 只告警不拒绝，理由见 warnReservedPins 的注释（真源在固件，后端是可能过期的副本）。
	warnReservedPins(node, owners)
	return channels, nil
}

// validateManifestTemplateCapacity mirrors config_mgr's fixed template array on
// the collector. The server must not publish a manifest it already knows the
// collector cannot apply.
func validateManifestTemplateCapacity(templates []models.ConfigTemplate, maxTemplates int) error {
	if len(templates) > maxTemplates {
		return fmt.Errorf("manifest has %d templates; collector limit is %d", len(templates), maxTemplates)
	}
	return nil
}

// reconcileDriverTemplates ensures every driver CommandTemplate has a matching
// ConfigTemplate in DB. Auto-creates missing templates for self-healing.
// Capacity is checked before any mutation, so self-healing cannot manufacture a
// manifest the collector will reject.
//
// F2 fail-closed: runs inside the SendConfigManifestWithDecision transaction.
// A single template Create failure now returns an error (aborting the whole
// transaction, leaving no orphan templates) instead of the old warn+continue.
func reconcileDriverTemplates(db *gorm.DB, driverRegistry *drivers.Registry, nodeID string, existingTemplates []models.ConfigTemplate, maxTemplates int) (bool, error) {
	if driverRegistry == nil {
		return false, nil
	}
	// Collect all edge devices for this node and their driver commands
	type cmdNeed struct {
		chID       uint
		writeData  string
		readLength uint32
		delayMs    uint32
	}
	needed := make(map[string]cmdNeed) // key = normalized write_data

	var edges []models.EdgeDevice
	db.Where("node_id = ? AND enabled = true", nodeID).Find(&edges)
	for _, edge := range edges {
		drv, err := driverRegistry.Get(edge.Type)
		if err != nil {
			continue
		}
		provider, ok := drv.(drivers.CommandTemplateProvider)
		if !ok {
			continue
		}
		// Per-command interval overrides for this device (may be empty).
		cmdIntervals := make(map[string]int)
		if len(edge.CommandIntervals) > 0 {
			_ = json.Unmarshal(edge.CommandIntervals, &cmdIntervals)
		}
		for _, cmd := range provider.GetCommandTemplates() {
			if !cmd.Schedulable || cmd.WriteData == "" {
				continue
			}
			/* Only commands that are ACTUALLY POLLED need a ConfigTemplate.
			 *
			 * 修复（2026-10-05 现场）：此前这里只过滤 Schedulable/WriteData，
			 * 没有过滤 interval，于是把驱动声明的**全部**模板都创建进 DB，
			 * 再由 sender_snapshot.go:238 全量编码进 ConfigManifest。
			 * 而编码器（sender_snapshot.go:347-357）和
			 * CommandIsManifestCandidate（sender_snapshot.go:197）都只把
			 * `Schedulable && effectiveInterval > 0` 视为候选 —— 两处口径不一致。
			 *
			 * 现场后果（S3 节点 30EDA0A9A808，接 JBD BMS + Techfine 逆变器）：
			 *   JBD 声明 5 个模板，仅 read_basic_info(5000ms) 启用，其余 4 个为 0；
			 *   Techfine 声明 11 个，仅 read_status(1000ms) 启用，其余 10 个为 0；
			 *   合计应有 2 个，但这里算成 5+11=16，再加 2 个历史残留 = 18 > 16，
			 *   于是 SendConfigManifest 每次都被自己拒绝：
			 *     "template reconciliation would create 18 templates;
			 *      collector limit is 16"
			 *   配置永远下发不到设备 → 逆变器通道从未生效 → UART1 一个字节
			 *   都没发出（现场表现为 TTL→RS232 板的 TX/RX 灯完全不亮，
			 *   极易被误判成接线或电平转换故障）。
			 *
			 * interval 的解析必须与 CommandIsManifestCandidate 一致：
			 * storedIntervals 里有该命令的覆盖值就用覆盖值，否则用模板默认值。
			 */
			effectiveInterval := cmd.IntervalMs
			if v, ok := cmdIntervals[cmd.ID]; ok {
				effectiveInterval = v
			}
			if effectiveInterval <= 0 {
				continue // 不轮询：不建模板，也不占容量
			}
			key := strings.ToUpper(strings.TrimSpace(cmd.WriteData))
			needed[key] = cmdNeed{
				chID:       edge.ChannelID,
				writeData:  cmd.WriteData,
				readLength: cmd.ReadLength,
				delayMs:    cmd.DelayMs,
			}
		}
	}

	// Check which needed templates already exist
	existingKeys := make(map[string]bool)
	for _, t := range existingTemplates {
		existingKeys[strings.ToUpper(strings.TrimSpace(t.WriteData))] = true
	}

	missing := 0
	for key := range needed {
		if !existingKeys[key] {
			missing++
		}
	}
	if len(existingTemplates)+missing > maxTemplates {
		return false, fmt.Errorf("template reconciliation would create %d templates; collector limit is %d", len(existingTemplates)+missing, maxTemplates)
	}

	// Create missing templates after capacity preflight succeeds.
	created := false
	for key, need := range needed {
		if existingKeys[key] {
			continue
		}
		tmpl := models.ConfigTemplate{
			NodeID:     nodeID,
			WriteData:  need.writeData,
			ReadLength: need.readLength,
			DelayMs:    need.delayMs,
		}
		if err := db.Create(&tmpl).Error; err != nil {
			// F2 fail-closed: a failed Create aborts the whole transaction.
			return created, fmt.Errorf("auto-create ConfigTemplate (tx_hex_chars=%d): %w", len(need.writeData), err)
		}
		// Append template ID to channel's template_ids
		newID := strconv.FormatUint(uint64(tmpl.ID), 10)
		if err := db.Model(&models.Channel{}).Where("id = ?", need.chID).Update("template_ids",
			gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newID, newID)).Error; err != nil {
			return created, fmt.Errorf("append template_id %d to channel %d: %w", tmpl.ID, need.chID, err)
		}
		logger.Infof("[reconcile] Auto-created ConfigTemplate id=%d tx_hex_chars=%d for channel=%d",
			tmpl.ID, len(need.writeData), need.chID)
		created = true
	}
	return created, nil
}

// getCommandTemplatesFromDriver returns command templates from a driver, or nil.
func getCommandTemplatesFromDriver(drv drivers.Driver) []drivers.CommandTemplate {
	if drv == nil {
		return nil
	}
	if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
		return provider.GetCommandTemplates()
	}
	return nil
}

// findTemplateIDForCommand finds a ConfigTemplate ID that matches the given write_data hex.
// Returns 0 if no matching template is found — the caller should skip that command.
func findTemplateIDForCommand(templates []models.ConfigTemplate, writeData string) uint64 {
	normalized := strings.ToUpper(strings.TrimSpace(writeData))
	if normalized == "" {
		return 0
	}
	for _, t := range templates {
		tWrite := strings.ToUpper(strings.TrimSpace(t.WriteData))
		if tWrite == normalized {
			return uint64(t.ID)
		}
	}
	return 0 // no match — caller must skip this command
}

// findTemplateID returns the first template ID from Channel.TemplateIDs for
// the given channel and edge device. Returns 0 when the channel carries no
// template_ids or none of them parse — the caller must skip encoding rather
// than fall back to a magic template (F3: no more silent fallback=1).
func findTemplateID(ch models.Channel, edge models.EdgeDevice) uint64 {
	if ch.TemplateIDs != "" {
		for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
			if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
				return id
			}
		}
	}
	return 0
}

// reservedPinName 返回该平台某引脚是保留脚时的**用途名**（BOOT / USB_D- / USB_D+ / LED）。
//
// ⚠ 与 handler_periph.go:382 的 reservedPinForPlatform **有意分工、但数据同源**：
//
//	那个函数返回**单个** pin 且只用于**硬拒绝** GPIO/PWM 外设；
//	本函数给出**完整清单**且只用于**告警**。
//	⚠ 两张表应合并成一份（现为手工副本，固件扩清单时不会自动跟上）—— 登记为待办（§198）。
//
// 数据来源：esp32-collector/components/hw_profile/include/hw_tables.h 的 HW_RESERVED_*。
func reservedPinName(platform string, pin int) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(platform)) {
	case "ESP32S3", "ESP32-S3", "S3":
		switch pin {
		case 0:
			return "BOOT/strapping", true
		case 19:
			return "USB_D-", true
		case 20:
			return "USB_D+", true
		case 48:
			return "RGB LED (WS2812)", true
		}
	case "ESP32C6", "ESP32-C6", "C6":
		switch pin {
		case 9:
			return "BOOT/strapping", true
		case 12:
			return "USB_D-", true
		case 13:
			return "USB_D+", true
		case 8:
			return "RGB LED (WS2812)", true
		}
	}
	return "", false
}

// warnReservedPins 对"将要编码进 manifest 的引脚里落在保留脚上的"发告警。
//
// ⚠⚠ 2026-10-09（§198）新增，**只告警、不拒绝**。这是有意的取舍：
//
//	① **真源在固件**：哪些脚保留由 hw_tables.h 的 HW_RESERVED_* 定义
//	   （S3: 0/19/20/48，C6: 9/12/13/8）。后端这份是**手工副本**。
//	② 把可能过期的副本当**硬拒绝**判据，会在固件新增保留脚时误伤合法配置 ——
//	   而固件侧 validate_manifest_resources 才是权威（它真知道本芯片的保留脚）。
//	③ 但**静默**也不行：S3P 实测上报 capabilities 里 I2C1.default_scl_pin = **48**，
//	   而 48 正是该型号的 RGB LED 保留脚（main.c 的 rgb_led_init(48) 真在驱动它）。
//	   后端**照单全收**并编码下发 ⇒ 设备报 ESP_ERR_INVALID_ARG ⇒
//	   排查者只看到"引脚仲裁失败"，不知道"后端早就该提醒这个脚是保留脚"。
//
// ⇒ 折中：编码前告警（可观测），判据仍归固件（权威）。
//
// ⚠ 为什么加在 claim 的调用点而不是 channelRoutePins：
//
//	claim 是**所有**会被编码进 manifest 的资源的唯一汇聚点
//	（UART/I2C/SPI 通道 + GPIO + PWM 都走它），而
//	handler_periph.go 的 validateReportedGPIO 只覆盖 GPIO/PWM ——
//	**总线通道的引脚（channelRoutePins）从来没查过保留脚**。
func warnReservedPins(node models.Node, owners map[int]string) {
	if len(owners) == 0 {
		return
	}
	pins := make([]int, 0, len(owners))
	for pin := range owners {
		pins = append(pins, pin)
	}
	sort.Ints(pins)
	for _, pin := range pins {
		name, ok := reservedPinName(node.Platform, pin)
		if !ok {
			continue
		}
		// ⚠ 格式串写在一行或用 +：Go 不支持相邻字符串字面量跨行拼接（本轮踩过两次）。
		logger.Warnf("[%s] manifest 将编码保留脚 GPIO%d（%s 的 %s）：若固件无法认领该脚，"+
			"整份 manifest 会被拒（不止这一条通道）；"+
			"⚠ 后端保留脚表是固件 hw_tables.h 的手工副本，可能过期 —— 见 §198。",
			node.NodeID, pin, node.Platform, name)
	}
}
