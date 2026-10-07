package api

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type reportedPWMResource struct {
	ID                string `json:"id"`
	Channel           uint8  `json:"channel"`
	MaxResolutionBits uint8  `json:"max_resolution_bits"`
}

type reportedGPIOResource struct {
	Pin int `json:"pin"`
}

type reportedUARTResource struct {
	ID string `json:"id"`
	// Port 是固件上报的 UART 控制器序号（hw_tables.c 的 .port）。用来把 hardware_id 的
	// 三种历史写法（"UART1" / "uart1" / "0x01"）归一到同一个资源，见 matchReportedUART。
	// 缺省 0 时该字段不参与匹配（否则会把所有资源都当成 port 0）。
	Port         int `json:"port"`
	DefaultTxPin int `json:"default_tx_pin"`
	DefaultRxPin int `json:"default_rx_pin"`
	// MaxBaud 是资源**能力上限**（不是当前生效波特率）。新建 UART 通道兜底补齐
	// bus_config 时用它把默认速率夹在设备支持的范围内（见 ensureUARTBusConfig）。
	MaxBaud uint64 `json:"max_baud"`
}

type reportedI2CResource struct {
	DefaultSdaPin int `json:"default_sda_pin"`
	DefaultSclPin int `json:"default_scl_pin"`
}

type reportedSPIResource struct {
	DefaultMosiPin int `json:"default_mosi_pin"`
	DefaultMisoPin int `json:"default_miso_pin"`
	DefaultSclkPin int `json:"default_sclk_pin"`
	DefaultCsPin   int `json:"default_cs_pin"`
}

type reportedPeripheralResources struct {
	Buses struct {
		PWM  []reportedPWMResource  `json:"pwm"`
		GPIO []reportedGPIOResource `json:"gpio"`
		UART []reportedUARTResource `json:"uart"`
		I2C  []reportedI2CResource  `json:"i2c"`
		SPI  []reportedSPIResource  `json:"spi"`
	} `json:"buses"`
}

func validateEnabledChannelPin(db *gorm.DB, nodeID string, pin int) error {
	var channels []models.Channel
	if err := db.Where("node_id = ? AND enabled = ?", nodeID, true).Find(&channels).Error; err != nil {
		return fmt.Errorf("load enabled channels: %w", err)
	}
	for _, ch := range channels {
		busType := strings.ToUpper(strings.TrimSpace(ch.BusType))
		if busType == "GPIO" || busType == "4" || busType == "PWM" || busType == "6" {
			return fmt.Errorf("legacy peripheral channel %d is still enabled", ch.ID)
		}
		if busType == "ADC" || busType == "USB" {
			// Pins-less transports route no GPIO, so they cannot conflict with a
			// new GPIO/PWM resource. (USB = ESP32-C6 native USB data bus; its
			// bus_config is empty and carries no pins.)
			continue
		}
		raw := strings.TrimPrefix(strings.TrimSpace(ch.BusConfig), `\x`)
		cfg, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		uses := false
		switch busType {
		case "UART", "I2C":
			if len(cfg) < 2 {
				return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
			}
			uses = int(cfg[0]) == pin || int(cfg[1]) == pin
		case "SPI":
			if len(cfg) != 9 {
				return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
			}
			uses = int(cfg[0]) == pin
			if len(cfg) >= 9 {
				uses = uses || int(cfg[6]) == pin || int(cfg[7]) == pin || int(cfg[8]) == pin
			}
		default:
			return fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
		}
		if uses {
			return fmt.Errorf("GPIO pin %d conflicts with enabled channel %d", pin, ch.ID)
		}
	}
	return nil
}

func resolveReportedPWMResources(db *gorm.DB, node *models.Node, hardwareID string, pin int) (reportedPWMResource, error) {
	var resources reportedPeripheralResources
	if node.Capabilities == "" || json.Unmarshal([]byte(node.Capabilities), &resources) != nil {
		return reportedPWMResource{}, fmt.Errorf("node has not reported usable hardware resources")
	}
	var pwm *reportedPWMResource
	for i := range resources.Buses.PWM {
		if resources.Buses.PWM[i].ID == hardwareID {
			pwm = &resources.Buses.PWM[i]
			break
		}
	}
	if pwm == nil {
		return reportedPWMResource{}, fmt.Errorf("PWM resource %q was not reported by node", hardwareID)
	}
	for _, gpio := range resources.Buses.GPIO {
		if gpio.Pin == pin {
			// PWM 是本次事故的**直接**入口（PWM0 配到 S3 的 GPIO0，duty 3%）：
			// 即使旧固件把它上报成可用 GPIO，这里也必须拒绝。
			if err := checkNotReservedPin(node, pin); err != nil {
				return reportedPWMResource{}, err
			}
			if err := validateEnabledChannelPin(db, node.NodeID, pin); err != nil {
				return reportedPWMResource{}, err
			}
			return *pwm, nil
		}
	}
	return reportedPWMResource{}, fmt.Errorf("GPIO pin %d was not reported by node", pin)
}

func validateCurrentPWMConfig(db *gorm.DB, node *models.Node, cfg *models.PWMConfig) (reportedPWMResource, error) {
	resource, err := resolveReportedPWMResources(db, node, cfg.HardwareID, cfg.Pin)
	if err != nil {
		return reportedPWMResource{}, err
	}
	if resource.Channel != cfg.Channel {
		return reportedPWMResource{}, fmt.Errorf("PWM resource %q channel no longer matches current report", cfg.HardwareID)
	}
	return resource, nil
}

func reportedBusPinConflict(resources *reportedPeripheralResources, pin int) bool {
	for _, bus := range resources.Buses.UART {
		if bus.DefaultTxPin == pin || bus.DefaultRxPin == pin {
			return true
		}
	}
	for _, bus := range resources.Buses.I2C {
		if bus.DefaultSdaPin == pin || bus.DefaultSclPin == pin {
			return true
		}
	}
	for _, bus := range resources.Buses.SPI {
		if bus.DefaultMosiPin == pin || bus.DefaultMisoPin == pin || bus.DefaultSclkPin == pin || bus.DefaultCsPin == pin {
			return true
		}
	}
	return false
}

// errUARTCapabilityUnavailable 标记「节点没有上报可用的 UART 资源能力」这一类失败，
// 供调用方映射为 400（用户输入/前置条件问题），而不是 500（服务端故障）。
// 关键语义：**宁可拒绝创建，也不落库空 bus_config** —— 后者会制造一个"以后改不了波特率"
// 的通道，用户当下看不到任何异常，等点「改波特率」才报错。
var errUARTCapabilityUnavailable = errors.New("uart capability unavailable")

// errUARTBusConfigMalformed 标记「调用方显式给了 bus_config，但它连引脚路由都不是」
// （例如 "zz" 非 hex）。与上一条一样映射 400：这是用户输入问题，不是服务端故障。
//
// 为什么不静默改写成能力值：显式提供的引脚是**用户的意图**，静默改写会让用户
// 以为自己的引脚被采纳了（实际被丢弃），比明确拒绝更危险。
//
// ⚠ 2026-10-03 修正（审查发现的 P0）：这里曾经用 withUARTBaudrate 当判据，
// 于是把「2 字节」也判成非法。**那是错的**，两个约束被混为一谈：
//
//	· 引脚路由合法性：UART/I2C 只要 >=2 字节（tx,rx）即合法 ——
//	  见 channelRoutePins（handler_device.go:96-100）。仿真套件与存量库都在用 2 字节。
//	· 可改波特率：需要 >=6 字节（字节 2..5 存波特率）—— withUARTBaudrate 的要求。
//
// 用「可改波特率」当「合法性」判据，会把合法的 2 字节引脚路由一并拒绝：
// 实测仿真套件 4 处 UART bus_config 字面量（chan.go:482、edge.go:608、
// scene.go:247、auto.go:242）全是 <6 字节且断言 201 ⇒ -tags=simulation 门禁大面积变红。
//
// 正确的分工：本函数只保证**引脚路由合法**（>=2 且 hex）；
// 「能否改波特率」是另一个独立事实，由 minUARTBusConfigLenForBaudrate 显式判定，
// 不再拿它当拒绝理由 —— 拒绝一个能正常下发引脚、只是暂时改不了波特率的通道，
// 属于用错误的手段达成正确目标（用户连通道都建不出来了）。
var errUARTBusConfigMalformed = errors.New("uart bus_config malformed")

// uartBusConfigMinRouteLen 是 UART 引脚路由的**最小**长度：tx(1B) + rx(1B)。
// 与 channelRoutePins 的判据保持一致，不要各写一份。
const uartBusConfigMinRouteLen = 2

// uartBusConfigMinBaudLen 是「可改波特率」所需的**最小**长度：
// 字节 2..5 存 big-endian uint32 波特率，故至少 6 字节。
const uartBusConfigMinBaudLen = 6

// ensureUARTBusConfig 保证 UART 通道落库的 bus_config 一定可解析、可改波特率。
//
// 背景（2026-10-03 实测）：前端组装 bus_config 依赖 capabilities.buses.uart 里能按 id 找到
// 该资源；资源能力尚未上报、或 hardware_id 与资源 id 不完全相等时，前端**静默**产出空串，
// 后端又原样落库 ⇒ 用户得到一个"改不了波特率"的通道（reconfigure 报 bus_config 为空）。
// 后端不能信任调用方一定带了 bus_config，故在此兜底。
//
// 规则（三种输入，三种归宿）：
//   - 非空且**是合法引脚路由**（hex 且 >=2 字节）⇒ 原样保留；
//     此时若长度 <6，它**暂时改不了波特率**，但那是可接受的中间状态：
//     通道能正常下发引脚、能被节点解析，用户后续可用补丁式 PUT 补齐波特率字段。
//     刻意**不**因此拒绝 —— 见 errUARTBusConfigMalformed 上方的说明。
//   - 非空但**连引脚路由都不是**（非 hex / <2 字节）⇒ errUARTBusConfigMalformed（400）；
//   - **空** ⇒ 按节点 ResourceReport（nodes.capabilities 的 buses.uart[]）里的
//     default_tx_pin/default_rx_pin + 默认波特率补齐（造出的一定是 10 字节、可改波特率）；
//   - 能力里查不到该资源 / 节点无能力 ⇒ errUARTCapabilityUnavailable（映射 400）。
func ensureUARTBusConfig(node *models.Node, ch *models.Channel) error {
	if ch == nil {
		return fmt.Errorf("channel is required")
	}
	if trimmed := strings.TrimSpace(ch.BusConfig); trimmed != "" {
		// 只校验「引脚路由是否合法」，不要求「可改波特率」。
		// 复用 channelRoutePins 的同一判据，避免两处阈值漂移。
		if _, err := channelRoutePins(*ch); err != nil {
			return fmt.Errorf("%w：UART 通道 bus_config 非法（%q）：%v", errUARTBusConfigMalformed, ch.BusConfig, err)
		}

		/* ⚠ 2026-10-07 记：这里**刻意**不把短 bus_config 补齐成 >=6 字节，尽管固件对下发
		 * 要求 >=6（bus_manager.c:443）。原因是本函数的契约是"校验/补齐**空值**"，
		 * 而**已给出的值必须原样保留** —— 这是 P0 回归护栏
		 * （TestChannelUpdate_UART2ByteRouteAccepted:121 明确断言"调用方显式给的引脚路由必须原样保留"）。
		 *
		 * ⇒ "短值下发前补齐"属于**组装下发字节时**的职责，不在本函数。
		 *   缺陷（后端 201 但设备 ESP_ERR_INVALID_SIZE）见设计文档 §160。 */
		return nil
	}
	if node == nil {
		return fmt.Errorf("%w: 节点不存在，无法确定 UART 资源能力", errUARTCapabilityUnavailable)
	}
	var resources reportedPeripheralResources
	if strings.TrimSpace(node.Capabilities) == "" || json.Unmarshal([]byte(node.Capabilities), &resources) != nil {
		return fmt.Errorf("%w: 节点 %s 尚未上报硬件资源能力，无法为 UART 通道补齐 bus_config（请等待资源上报后重试）",
			errUARTCapabilityUnavailable, node.NodeID)
	}
	entry, ok, err := matchReportedUART(resources.Buses.UART, strings.TrimSpace(ch.HardwareID))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: 节点 %s 上报的资源里没有 UART 资源 %q（已上报：%s）",
			errUARTCapabilityUnavailable, node.NodeID, ch.HardwareID, describeReportedUARTPorts(resources.Buses.UART))
	}
	if entry.DefaultTxPin <= 0 || entry.DefaultRxPin <= 0 {
		return fmt.Errorf("%w: UART 资源 %s 上报的默认引脚无效（TX=%d, RX=%d）",
			errUARTCapabilityUnavailable, entry.ID, entry.DefaultTxPin, entry.DefaultRxPin)
	}
	baud := defaultUARTBaudrate
	if entry.MaxBaud > 0 && entry.MaxBaud < uint64(baud) {
		// 能力上限低于默认值时退到上限，不造一个设备明确不支持的速率。
		baud = int(entry.MaxBaud)
	}
	// byte 6 是 DMA flags（固件 bus_dma.h:59-62 / config_mgr GetDmaEnabled）：
	// 传 true 得到 0x01，与生产既有三条 UART 行一致。帧格式 8N1 由固件硬编码，
	// 不编进 bus_config（UART 分支不读 byte 7..9）。
	ch.BusConfig = buildUARTBusConfig(entry.DefaultTxPin, entry.DefaultRxPin, baud, true)
	return nil
}

// matchReportedUART 把通道的 hardware_id 匹配到节点上报的某条 UART 资源。
//
// 为什么不能只做一次 EqualFold（第一版就是这么写的，2026-10-04 被仿真门禁打回）：
// hardware_id 在真实数据里有**三种**写法，全都必须能命中：
//
//	"UART1"  生产通道 / 前端下拉框
//	"uart1"  固件 hw_tables.c 的小写 id（仿真 harness 也用它）
//	"0x01"   仿真夹具与部分前端历史写法：把第几个串口写成十六进制
//
// 只比字符串会漏掉后两种，于是**建通道直接 400** —— 不是少补一个字段，
// 而是把本来能用的请求整个拒掉。仿真 140 个场景里因此有 9 个变红，
// 而 CI 的 backend-scenarios 是既有绿灯门禁（0ad5975 success），
// 所以这是回归，不是新约束。
//
// 归一规则：同时接受 id 与 port 两条线索 ——
//
//	· id 相同（大小写不敏感）；或
//	· hardware_id 是 "0xNN" / "NN" 纯数字形式，且 NN == entry.Port。
//
// 返回 (entry, found, err)：err 仅在认出了写法但指向不存在的资源时为非空。
func matchReportedUART(entries []reportedUARTResource, hardwareID string) (reportedUARTResource, bool, error) {
	if hardwareID == "" {
		// 没有 hardware_id：只有资源唯一时才敢猜，否则交给调用方报错。
		if len(entries) == 1 {
			return entries[0], true, nil
		}
		return reportedUARTResource{}, false, nil
	}

	// 线索 1：id 直接相等（"UART1" == "uart1"）。
	for i := range entries {
		if strings.EqualFold(strings.TrimSpace(entries[i].ID), hardwareID) {
			return entries[i], true, nil
		}
	}

	// 线索 2：把 "0x01" / "1" 这类写法解成端口号，再按 entry.Port 匹配。
	if port, ok := parseUARTPortToken(hardwareID); ok {
		for i := range entries {
			if entries[i].Port == port {
				return entries[i], true, nil
			}
		}
		// 端口写法认得出来，但节点没上报这个端口 —— 明确说清。
		return reportedUARTResource{}, false,
			fmt.Errorf("%w: hardware_id %q 指的是 UART 端口 %d，但节点上报的 UART 资源里没有该端口（已上报：%s）",
				errUARTCapabilityUnavailable, hardwareID, port, describeReportedUARTPorts(entries))
	}

	return reportedUARTResource{}, false, nil
}

// parseUARTPortToken 只接受两类明确的端口写法，避免把任意字符串误解析成 0：
//   - 十六进制："0x01" / "0X1"
//   - 十进制："1"
func parseUARTPortToken(token string) (int, bool) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, false
	}
	base := 10
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		base = 16
		trimmed = trimmed[2:]
		if trimmed == "" {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(trimmed, base, 8)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

func describeReportedUARTPorts(entries []reportedUARTResource) string {
	if len(entries) == 0 {
		return "（无）"
	}
	parts := make([]string, 0, len(entries))
	for i := range entries {
		parts = append(parts, fmt.Sprintf("%s(port=%d)", entries[i].ID, entries[i].Port))
	}
	return strings.Join(parts, ", ")
}

// reservedPinForPlatform 返回该平台**不可分配给用户外设**的引脚。
//
// 为什么服务端也要挡（2026-10-04 现场事故的纵深防线）：
// 固件已把保留引脚从 ResourceReport 剔除（hw_profile.c 的过滤 +
// hw_tables.c 的 HW_GPIO_FLAG_RESERVED），正常路径下这里不会命中。
// 但有两条路径能绕过固件侧上报：
//  1. 现场设备跑的是**旧固件**（上报里还带 GPIO0），而服务端已升级；
//  2. 有人手工改库 / 灌入伪造的 capabilities（本次事故正是我用 SQL 手写
//     bus_config 造通道，当时服务端没有任何 strapping 引脚的概念）。
//
// 事故后果不是"配置没生效"，而是"设备每 8.8s 擦一次 NVS 并重启"——
// 一个 UI 上完全合法的 PWM 配置把设备变成了砖。这种破坏性配置值得服务端
// 再挡一次：宁可返回 422 说清原因，也不能下发出去。
//
// 判定按 node.Platform（由 Hello/ResourceReport 写入；prod 实测为
// ESP32S3 / ESP32C6）。未知平台返回 not-ok：不阻碍新硬件接入，因为固件侧
// 的过滤对任何平台都生效。
func reservedPinForPlatform(platform string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(platform)) {
	case "ESP32S3", "ESP32-S3", "S3":
		// GPIO0 = BOOT 按键 / strapping（factory_reset.c 的 BOOT_BUTTON_GPIO、
		// bus_dma.c 的 BOOT_STRAP_GPIO）。把它拉低即等同"长按 BOOT"。
		return 0, true
	case "ESP32C6", "ESP32-C6", "C6":
		// GPIO9 = BOOT 按键（C6 的 GPIO8 是 RGB LED，但 LED 由固件内部驱动、
		// 不在 hw_gpios 上报清单里，用户本来也配不到，故不在此列）。
		return 9, true
	default:
		return 0, false
	}
}

// checkNotReservedPin 在引脚属于该平台保留引脚时返回可读错误。
func checkNotReservedPin(node *models.Node, pin int) error {
	reserved, known := reservedPinForPlatform(node.Platform)
	if !known || pin != reserved {
		return nil
	}
	return fmt.Errorf("GPIO pin %d on %s is the BOOT/strapping pin and cannot be "+
		"assigned to a peripheral: a low level on it is indistinguishable from "+
		"holding the BOOT button, which triggers a factory reset (NVS erase + "+
		"reboot). Choose another pin", pin, node.Platform)
}

func validateReportedGPIO(db *gorm.DB, node *models.Node, pin int) error {
	if err := checkNotReservedPin(node, pin); err != nil {
		return err
	}
	var resources reportedPeripheralResources
	if node.Capabilities == "" || json.Unmarshal([]byte(node.Capabilities), &resources) != nil || len(resources.Buses.GPIO) == 0 {
		return fmt.Errorf("node has not reported usable GPIO resources")
	}
	for _, gpio := range resources.Buses.GPIO {
		if gpio.Pin == pin {
			return validateEnabledChannelPin(db, node.NodeID, pin)
		}
	}
	return fmt.Errorf("GPIO pin %d was not reported by node", pin)
}

func validatePWMFrequency(frequency uint32, resolution uint8) error {
	if frequency == 0 {
		return fmt.Errorf("frequency must be nonzero")
	}
	if resolution < 4 || resolution > 20 || uint64(frequency)*(uint64(1)<<resolution) > 40000000 {
		return fmt.Errorf("frequency and resolution exceed PWM controller capability")
	}
	return nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}

var errPeripheralPinConflict = errors.New("peripheral pin conflict")

// errChannelPinConflict 表示候选通道与**同节点其它通道**抢同一 GPIO。
// 与 errPeripheralPinConflict（通道 vs GPIO/PWM 配置）分开，是为了让 HTTP
// 层能给出可行动的提示（"换个串口/引脚"），而不是笼统的 409。
var errChannelPinConflict = errors.New("channel route conflicts with another channel")

func createGPIOConfigWithPinExclusion(db *gorm.DB, nodeID string, cfg *models.GPIOConfig) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", nodeID, cfg.Pin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errPeripheralPinConflict
		}
		if err := validateEnabledChannelPin(tx, nodeID, cfg.Pin); err != nil {
			return errPeripheralPinConflict
		}
		desiredEnabled := cfg.Enabled
		if err := tx.Create(cfg).Error; err != nil {
			return err
		}
		if !desiredEnabled {
			if err := tx.Model(cfg).Update("enabled", false).Error; err != nil {
				return err
			}
			cfg.Enabled = false
		}
		return nil
	})
}

func createPWMConfigWithPinExclusion(db *gorm.DB, nodeID string, cfg *models.PWMConfig) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", nodeID, cfg.Pin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errPeripheralPinConflict
		}
		if err := validateEnabledChannelPin(tx, nodeID, cfg.Pin); err != nil {
			return errPeripheralPinConflict
		}
		desiredEnabled := cfg.Enabled
		if err := tx.Create(cfg).Error; err != nil {
			return err
		}
		if !desiredEnabled {
			if err := tx.Model(cfg).Update("enabled", false).Error; err != nil {
				return err
			}
			cfg.Enabled = false
		}
		return nil
	})
}

// Peripheral control constants (v3.0)
//
// PeriphCmd field 2: periph_type
const (
	PeriphTypeGPIO uint8 = 1
	PeriphTypePWM  uint8 = 2
)

// GPIO action enum (PeriphCmd field 4)
const (
	GPIOActionSetLow   uint8 = 0
	GPIOActionSetHigh  uint8 = 1
	GPIOActionRead     uint8 = 2
	GPIOActionConfig   uint8 = 3
	GPIOActionDeconfig uint8 = 4
	GPIOActionToggle   uint8 = 5
)

// PWM action enum (PeriphCmd field 4)
const (
	PWMActionSetDuty       uint8 = 0
	PWMActionSetFreq       uint8 = 1
	PWMActionStart         uint8 = 2
	PWMActionStop          uint8 = 3
	PWMActionRead          uint8 = 4
	PWMActionSetResolution uint8 = 5
)

// GPIO direction encoding: 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
const (
	GPIODirInput       uint8 = 0
	GPIODirOutput      uint8 = 1
	GPIODirInputPullUp uint8 = 2
	GPIODirInputPullDn uint8 = 3
)

// registerPeriphRoutes sets up GPIO + PWM peripheral control routes.
// Uses :id parameter name (consistent with existing /nodes/:id routes).
func registerPeriphRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()
	n := v1.Group("/nodes")

	// ================================================================
	// GPIO API
	// ================================================================

	// GET /api/v1/nodes/:id/gpio — list all GPIO configs for a node
	n.GET("/:id/gpio", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var configs []models.GPIOConfig
		db.Where("node_id = ?", node.NodeID).Order("pin ASC").Find(&configs)
		Success(c, configs)
	})

	// POST /api/v1/nodes/:id/gpio — configure a GPIO pin
	n.POST("/:id/gpio", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Pin          *int   `json:"pin"`
			Direction    uint8  `json:"direction"`
			InitialLevel uint8  `json:"initial_level"`
			Label        string `json:"label"`
			Enabled      *bool  `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Pin == nil || *req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		pin := *req.Pin
		if req.Direction > GPIODirInputPullDn {
			Error(c, http.StatusBadRequest, "invalid direction (0-3)")
			return
		}
		if req.InitialLevel > 1 {
			Error(c, http.StatusBadRequest, "initial_level must be 0 or 1")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var pwmConflict int64
		if err := db.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, pin).Count(&pwmConflict).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if pwmConflict > 0 {
			Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by PWM", pin))
			return
		}
		cfg := models.GPIOConfig{
			NodeID:       node.NodeID,
			Pin:          pin,
			Direction:    req.Direction,
			InitialLevel: req.InitialLevel,
			Label:        req.Label,
			Enabled:      true,
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if err := createGPIOConfigWithPinExclusion(db, node.NodeID, &cfg); err != nil {
			if errors.Is(err, errPeripheralPinConflict) {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by PWM", pin))
				return
			}
			if isUniqueConstraintError(err) {
				Error(c, http.StatusConflict, fmt.Sprintf("GPIO pin %d already configured for this node", pin))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))

		if cfg.Enabled {
			configBytes := []byte{req.Direction, req.InitialLevel}
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionConfig, 0, configBytes); err != nil {
				logger.Warnf("[%s] Failed to send GPIO CONFIG: %v", node.NodeID, err)
			}
		}

		SuccessWithCode(c, http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/gpio/:pin — update GPIO config
	n.PUT("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		var req struct {
			Direction    *uint8  `json:"direction"`
			InitialLevel *uint8  `json:"initial_level"`
			Label        *string `json:"label"`
			Enabled      *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Direction != nil {
			if *req.Direction > GPIODirInputPullDn {
				Error(c, http.StatusBadRequest, "invalid direction (0-3)")
				return
			}
			updates["direction"] = *req.Direction
		}
		if req.InitialLevel != nil {
			if *req.InitialLevel > 1 {
				Error(c, http.StatusBadRequest, "initial_level must be 0 or 1")
				return
			}
			updates["initial_level"] = *req.InitialLevel
		}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if len(updates) > 0 {
			if err := db.Transaction(func(tx *gorm.DB) error {
				var lockedNode models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", node.NodeID).First(&lockedNode).Error; err != nil {
					return err
				}
				var candidate models.GPIOConfig
				if err := tx.Where("id = ?", cfg.ID).First(&candidate).Error; err != nil {
					return err
				}
				if req.Enabled != nil {
					candidate.Enabled = *req.Enabled
				}
				if candidate.Enabled {
					if err := validateEnabledChannelPin(tx, node.NodeID, candidate.Pin); err != nil {
						return errPeripheralPinConflict
					}
					var pwmCount int64
					if err := tx.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, candidate.Pin).Count(&pwmCount).Error; err != nil {
						return err
					}
					if pwmCount > 0 {
						return errPeripheralPinConflict
					}
				}
				return tx.Model(&cfg).Updates(updates).Error
			}); err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))

		action := GPIOActionDeconfig
		var configBytes []byte
		if cfg.Enabled {
			action = GPIOActionConfig
			configBytes = []byte{cfg.Direction, cfg.InitialLevel}
		}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(cfg.Pin), action, 0, configBytes); err != nil {
			logger.Warnf("[%s] Failed to apply GPIO enabled state: %v", node.NodeID, err)
		}

		Success(c, cfg)
	})

	// DELETE /api/v1/nodes/:id/gpio/:pin — deconfigure GPIO pin
	n.DELETE("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if err := db.Delete(&cfg).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionDelete, node.NodeID, fmt.Sprint(cfg.ID))

		// Only an enabled, currently reported resource may need runtime teardown.
		if cfg.Enabled && validateReportedGPIO(db, node, pin) == nil {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionDeconfig, 0, nil); err != nil {
				logger.Warnf("[%s] Failed to send GPIO DECONFIG: %v", node.NodeID, err)
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/set — set output level {level: 0|1}
	n.POST("/:id/gpio/:pin/set", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "GPIO config is disabled")
			return
		}
		var req struct {
			Level  *uint8 `json:"level"`
			Toggle bool   `json:"toggle"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		var action uint8
		var value uint32
		if req.Toggle {
			action = GPIOActionToggle
		} else if req.Level == nil || *req.Level > 1 {
			Error(c, http.StatusBadRequest, "level must be 0 or 1")
			return
		} else if *req.Level == 0 {
			action = GPIOActionSetLow
		} else {
			action = GPIOActionSetHigh
		}

		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypeGPIO, uint8(pin), action, value, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "command sent", "action": action, "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/read — read pin level
	n.POST("/:id/gpio/:pin/read", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "GPIO config is disabled")
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionRead, 0, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "read command sent", "request_id": requestID})
	})

	// ================================================================
	// PWM API
	// ================================================================

	// GET /api/v1/nodes/:id/pwm — list all PWM configs for a node
	n.GET("/:id/pwm", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var configs []models.PWMConfig
		db.Where("node_id = ?", node.NodeID).Order("pin ASC").Find(&configs)
		Success(c, configs)
	})

	// POST /api/v1/nodes/:id/pwm — configure a PWM pin
	n.POST("/:id/pwm", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			HardwareID string `json:"hardware_id"`
			Pin        *int   `json:"pin"`
			Frequency  uint32 `json:"frequency"`
			Duty       uint16 `json:"duty"`
			Resolution uint8  `json:"resolution"`
			AutoStart  bool   `json:"auto_start"`
			Label      string `json:"label"`
			Enabled    *bool  `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.HardwareID) == "" {
			Error(c, http.StatusBadRequest, "hardware_id is required")
			return
		}
		if req.Pin == nil || *req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		resource, err := resolveReportedPWMResources(db, node, req.HardwareID, *req.Pin)
		if err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if req.Resolution == 0 {
			req.Resolution = 14 // default
		}
		if resource.MaxResolutionBits == 0 || req.Resolution < 4 || req.Resolution > 20 || req.Resolution > resource.MaxResolutionBits {
			Error(c, http.StatusBadRequest, "resolution exceeds reported PWM resource capability")
			return
		}
		if err := validatePWMFrequency(req.Frequency, req.Resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		var gpioConflict int64
		if err := db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&gpioConflict).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if gpioConflict > 0 {
			Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
			return
		}
		cfg := models.PWMConfig{
			NodeID:     node.NodeID,
			HardwareID: resource.ID,
			Channel:    resource.Channel,
			Pin:        *req.Pin,
			Frequency:  req.Frequency,
			Duty:       req.Duty,
			Resolution: req.Resolution,
			AutoStart:  req.AutoStart,
			Label:      req.Label,
			Enabled:    true,
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if err := createPWMConfigWithPinExclusion(db, node.NodeID, &cfg); err != nil {
			if errors.Is(err, errPeripheralPinConflict) {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
				return
			}
			if isUniqueConstraintError(err) {
				Error(c, http.StatusConflict, "PWM hardware resource or output pin already configured for this node")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))
		SuccessWithCode(c, http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/pwm/:hardware_id — update PWM config
	n.PUT("/:id/pwm/:hardware_id", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Pin        *int    `json:"pin"`
			Frequency  *uint32 `json:"frequency"`
			Duty       *uint16 `json:"duty"`
			Resolution *uint8  `json:"resolution"`
			AutoStart  *bool   `json:"auto_start"`
			Label      *string `json:"label"`
			Enabled    *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		resource, err := validateCurrentPWMConfig(db, node, &cfg)
		if err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Pin != nil {
			if *req.Pin < 0 {
				Error(c, http.StatusBadRequest, "pin must be non-negative")
				return
			}
			resource, err := resolveReportedPWMResources(db, node, cfg.HardwareID, *req.Pin)
			if err != nil || resource.Channel != cfg.Channel {
				Error(c, http.StatusUnprocessableEntity, "PWM hardware resource or GPIO pin was not reported by node")
				return
			}
			var conflicts int64
			if err := db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&conflicts).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
			if conflicts > 0 {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
				return
			}
			updates["pin"] = *req.Pin
		}
		frequency := cfg.Frequency
		resolution := cfg.Resolution
		if req.Frequency != nil {
			frequency = *req.Frequency
		}
		if req.Resolution != nil {
			resolution = *req.Resolution
		}
		if err := validatePWMFrequency(frequency, resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Frequency != nil {
			updates["frequency"] = *req.Frequency
		}
		if req.Duty != nil {
			if *req.Duty > 10000 {
				Error(c, http.StatusBadRequest, "duty must be 0-10000")
				return
			}
			updates["duty"] = *req.Duty
		}
		if req.Resolution != nil {
			if resource.MaxResolutionBits == 0 || *req.Resolution < 4 || *req.Resolution > 20 || *req.Resolution > resource.MaxResolutionBits {
				Error(c, http.StatusBadRequest, "resolution exceeds reported PWM resource capability")
				return
			}
			updates["resolution"] = *req.Resolution
		}
		if req.AutoStart != nil {
			updates["auto_start"] = *req.AutoStart
		}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if len(updates) > 0 {
			err := db.Transaction(func(tx *gorm.DB) error {
				var lockedNode models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", node.NodeID).First(&lockedNode).Error; err != nil {
					return err
				}
				if req.Pin != nil {
					var count int64
					if err := tx.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&count).Error; err != nil {
						return err
					}
					if count > 0 {
						return errPeripheralPinConflict
					}
				}
				candidatePin := cfg.Pin
				if req.Pin != nil {
					candidatePin = *req.Pin
				}
				if err := validateEnabledChannelPin(tx, node.NodeID, candidatePin); err != nil {
					return errPeripheralPinConflict
				}
				return tx.Model(&cfg).Updates(updates).Error
			})
			if err != nil {
				if errors.Is(err, errPeripheralPinConflict) {
					Error(c, http.StatusConflict, "PWM pin conflicts with an existing GPIO/transport route")
					return
				}
				if isUniqueConstraintError(err) {
					Error(c, http.StatusConflict, "PWM output pin already configured for this node")
					return
				}
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))
		if req.Enabled != nil && !cfg.Enabled {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil); err != nil {
				if restoreErr := db.Model(&cfg).Update("enabled", true).Error; restoreErr != nil {
					Error(c, http.StatusInternalServerError, fmt.Sprintf("stop failed: %v; restore failed: %v", err, restoreErr))
					return
				}
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		Success(c, cfg)
	})

	// DELETE /api/v1/nodes/:id/pwm/:hardware_id — deconfigure PWM hardware resource
	n.DELETE("/:id/pwm/:hardware_id", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		_, currentErr := resolveReportedPWMResources(db, node, cfg.HardwareID, cfg.Pin)
		if cfg.Enabled && currentErr == nil {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil); err != nil {
				logger.Warnf("[%s] PWM STOP before authoritative cleanup failed: %v", node.NodeID, err)
			}
		}
		if err := db.Delete(&cfg).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionDelete, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/start — start PWM output
	n.POST("/:id/pwm/:hardware_id/start", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Build PWM START config: [pin:1B][freq:4B LE][duty:2B LE][resolution:1B]
		configBytes := make([]byte, 8)
		configBytes[0] = uint8(cfg.Pin)
		binary.LittleEndian.PutUint32(configBytes[1:5], cfg.Frequency)
		binary.LittleEndian.PutUint16(configBytes[5:7], cfg.Duty)
		configBytes[7] = cfg.Resolution

		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStart, 0, configBytes)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "start command sent", "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/stop — stop PWM output
	n.POST("/:id/pwm/:hardware_id/stop", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "stop command sent", "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/duty — set duty cycle {duty: 0-10000}
	n.POST("/:id/pwm/:hardware_id/duty", func(c *gin.Context) {
		nodeMgr.LockPeriphIntent()
		defer nodeMgr.UnlockPeriphIntent()
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Duty *uint16 `json:"duty" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Duty == nil {
			if err == nil {
				err = fmt.Errorf("duty is required")
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if *req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		oldDuty := cfg.Duty
		if err := db.Model(&cfg).Update("duty", *req.Duty).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithPreviousValue(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionSetDuty, uint32(*req.Duty), nil, uint32(oldDuty))
		if err != nil {
			if restoreErr := db.Model(&cfg).Update("duty", oldDuty).Error; restoreErr != nil {
				Error(c, http.StatusInternalServerError, fmt.Sprintf("send failed: %v; restore failed: %v", err, restoreErr))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "duty command sent", "duty": *req.Duty, "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/freq — set frequency {frequency: Hz}
	n.POST("/:id/pwm/:hardware_id/freq", func(c *gin.Context) {
		nodeMgr.LockPeriphIntent()
		defer nodeMgr.UnlockPeriphIntent()
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Frequency uint32 `json:"frequency" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := validatePWMFrequency(req.Frequency, cfg.Resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// SET_FREQ config: [resolution:1B]
		configBytes := []byte{cfg.Resolution}

		oldFrequency := cfg.Frequency
		if err := db.Model(&cfg).Update("frequency", req.Frequency).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithPreviousValue(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionSetFreq, req.Frequency, configBytes, oldFrequency)
		if err != nil {
			if restoreErr := db.Model(&cfg).Update("frequency", oldFrequency).Error; restoreErr != nil {
				Error(c, http.StatusInternalServerError, fmt.Sprintf("send failed: %v; restore failed: %v", err, restoreErr))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "frequency command sent", "frequency": req.Frequency, "request_id": requestID})
	})

	// GET /api/v1/nodes/:id/pwm/:hardware_id/state — read current PWM state (duty)
	n.GET("/:id/pwm/:hardware_id/state", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Send READ command to device (best-effort) — result comes back via PeriphRsp WebSocket event
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionRead, 0, nil)
		if err != nil {
			logger.Warnf("[%s] Failed to send PWM READ: %v", node.NodeID, err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "runtime state unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"hardware_id": cfg.HardwareID,
			"channel":     cfg.Channel,
			"pin":         cfg.Pin,
			"frequency":   cfg.Frequency,
			"duty":        cfg.Duty,
			"resolution":  cfg.Resolution,
			"auto_start":  cfg.AutoStart,
			"enabled":     cfg.Enabled,
			"request_id":  requestID,
		})
	})
}
