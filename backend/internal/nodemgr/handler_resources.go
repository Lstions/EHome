package nodemgr

import (
	"encoding/json"
	"fmt"
	"strconv"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
)

// === Binary ResourceReport (0x19) decoding ===
//
// Top-level fields:
//   Field 1: platform (string)
//   Field 2: resource_count (varint)
//   Field 3: buses_blob (bytes) - nested sub-message
//   Field 4: channels_blob (bytes) - nested sub-message

// --- Decoded structures ---

type uartEntry struct {
	ID           string `json:"id"`
	Port         uint64 `json:"port"`
	DefaultTxPin uint64 `json:"default_tx_pin"`
	DefaultRxPin uint64 `json:"default_rx_pin"`
	MaxBaud      uint64 `json:"max_baud"`
	Flags        uint64 `json:"flags"`
	DmaSupported bool   `json:"dma_supported"`
}

type i2cEntry struct {
	ID            string `json:"id"`
	Port          uint64 `json:"port"`
	DefaultSdaPin uint64 `json:"default_sda_pin"`
	DefaultSclPin uint64 `json:"default_scl_pin"`
	MaxFreqHz     uint64 `json:"max_freq_hz"`
	Flags         uint64 `json:"flags"`
	DmaSupported  bool   `json:"dma_supported"`
}

type spiEntry struct {
	ID             string `json:"id"`
	Port           uint64 `json:"port"`
	DefaultMosiPin uint64 `json:"default_mosi_pin"`
	DefaultMisoPin uint64 `json:"default_miso_pin"`
	DefaultSclkPin uint64 `json:"default_sclk_pin"`
	DefaultCsPin   uint64 `json:"default_cs_pin"`
	MaxFreqHz      uint64 `json:"max_freq_hz"`
	Flags          uint64 `json:"flags"`
	DmaSupported   bool   `json:"dma_supported"`
}

type gpioEntry struct {
	ID  string `json:"id"`
	Pin uint64 `json:"pin"`
}

type adcEntry struct {
	ID      string `json:"id"`
	Unit    uint64 `json:"unit"`
	Channel uint64 `json:"channel"`
	Pin     uint64 `json:"pin"`
	MaxBits uint64 `json:"max_bits"`
}

type busesData struct {
	UART []uartEntry `json:"uart,omitempty"`
	I2C  []i2cEntry  `json:"i2c,omitempty"`
	SPI  []spiEntry  `json:"spi,omitempty"`
	GPIO []gpioEntry `json:"gpio,omitempty"`
	ADC  []adcEntry  `json:"adc,omitempty"`
}

type channelEntry struct {
	ID          uint64   `json:"id"`
	BusType     uint64   `json:"bus_type"`
	HardwareID  uint64   `json:"hardware_id"`
	IntervalMs  uint64   `json:"interval_ms"`
	Enabled     bool     `json:"enabled"`
	BusConfig   []byte   `json:"-"`
	TemplateIDs []uint64 `json:"template_ids,omitempty"`
	DmaEnabled  bool     `json:"dma_enabled"`
}

// --- Sub-message decoders ---

func decodeUARTEntry(data []byte) uartEntry {
	var e uartEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return e
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			e.ID = frame.GetString(field)
		case 2:
			e.Port = frame.GetUint64(field)
		case 3:
			e.DefaultTxPin = frame.GetUint64(field)
		case 4:
			e.DefaultRxPin = frame.GetUint64(field)
		case 5:
			e.MaxBaud = frame.GetUint64(field)
		case 6:
			e.Flags = frame.GetUint64(field)
			e.DmaSupported = (e.Flags & 1) != 0
		}
	}
	return e
}

func decodeI2CEntry(data []byte) i2cEntry {
	var e i2cEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return e
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			e.ID = frame.GetString(field)
		case 2:
			e.Port = frame.GetUint64(field)
		case 3:
			e.DefaultSdaPin = frame.GetUint64(field)
		case 4:
			e.DefaultSclPin = frame.GetUint64(field)
		case 5:
			e.MaxFreqHz = frame.GetUint64(field)
		case 6:
			e.Flags = frame.GetUint64(field)
			e.DmaSupported = (e.Flags & 1) != 0
		}
	}
	return e
}

func decodeSPIEntry(data []byte) spiEntry {
	var e spiEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return e
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			e.ID = frame.GetString(field)
		case 2:
			e.Port = frame.GetUint64(field)
		case 3:
			e.DefaultMosiPin = frame.GetUint64(field)
		case 4:
			e.DefaultMisoPin = frame.GetUint64(field)
		case 5:
			e.DefaultSclkPin = frame.GetUint64(field)
		case 6:
			e.DefaultCsPin = frame.GetUint64(field)
		case 7:
			e.MaxFreqHz = frame.GetUint64(field)
		case 8:
			e.Flags = frame.GetUint64(field)
			e.DmaSupported = (e.Flags & 1) != 0
		}
	}
	return e
}

func decodeGPIOEntry(data []byte) gpioEntry {
	var e gpioEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return e
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			e.ID = frame.GetString(field)
		case 2:
			e.Pin = frame.GetUint64(field)
		}
	}
	return e
}

func decodeADCEntry(data []byte) adcEntry {
	var e adcEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return e
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			e.ID = frame.GetString(field)
		case 2:
			e.Unit = frame.GetUint64(field)
		case 3:
			e.Channel = frame.GetUint64(field)
		case 4:
			e.Pin = frame.GetUint64(field)
		case 5:
			e.MaxBits = frame.GetUint64(field)
		}
	}
	return e
}

func decodeBusesBlob(data []byte) busesData {
	var buses busesData
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return buses
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1: // uart_entry (repeated)
			buses.UART = append(buses.UART, decodeUARTEntry(frame.GetBytes(field)))
		case 2: // i2c_entry (repeated)
			buses.I2C = append(buses.I2C, decodeI2CEntry(frame.GetBytes(field)))
		case 3: // spi_entry (repeated)
			buses.SPI = append(buses.SPI, decodeSPIEntry(frame.GetBytes(field)))
		case 4: // gpio_entry (repeated)
			buses.GPIO = append(buses.GPIO, decodeGPIOEntry(frame.GetBytes(field)))
		case 5: // adc_entry (repeated)
			buses.ADC = append(buses.ADC, decodeADCEntry(frame.GetBytes(field)))
		}
	}
	return buses
}

func decodeChannelEntry(data []byte) channelEntry {
	var ch channelEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return ch
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			ch.ID = frame.GetUint64(field)
		case 2:
			ch.BusType = frame.GetUint64(field)
		case 3:
			ch.HardwareID = frame.GetUint64(field)
		case 4:
			ch.IntervalMs = frame.GetUint64(field)
		case 5:
			ch.Enabled = frame.GetBool(field)
		case 6:
			ch.BusConfig = frame.GetBytes(field)
		case 7:
			ch.TemplateIDs = append(ch.TemplateIDs, frame.GetUint64(field))
		case 8:
			ch.DmaEnabled = frame.GetBool(field)
		}
	}
	return ch
}

func decodeChannelsBlob(data []byte) []channelEntry {
	var channels []channelEntry
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return channels
	}
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 1 { // channel_entry (repeated)
			channels = append(channels, decodeChannelEntry(frame.GetBytes(field)))
		}
	}
	return channels
}

// busTypeToString converts numeric bus type to string
func busTypeToString(bt uint64) string {
	switch bt {
	case 1:
		return "UART"
	case 2:
		return "I2C"
	case 3:
		return "SPI"
	case 4:
		return "GPIO"
	case 5:
		return "ADC"
	default:
		return fmt.Sprintf("UNKNOWN_%d", bt)
	}
}

// handleResourceReport processes ResourceReport (type=0x19) using binary frame decoding.
// Binary format:
//
//	Field 1: platform (string)
//	Field 2: resource_count (varint)
//	Field 3: buses_blob (bytes - nested sub-message)
//	Field 4: channels_blob (bytes - nested sub-message)
func (m *Manager) handleResourceReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ResourceReport: %v", deviceID, err)
		return
	}

	var platform string
	var resourceCount uint64
	var busesBlob []byte
	var channelsBlob []byte

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			platform = frame.GetString(field)
		case 2:
			resourceCount = frame.GetUint64(field)
		case 3:
			busesBlob = frame.GetBytes(field)
		case 4:
			channelsBlob = frame.GetBytes(field)
		}
	}

	logger.Infof("[%s] ResourceReport: platform=%s count=%d buses_blob=%d channels_blob=%d",
		deviceID, platform, resourceCount, len(busesBlob), len(channelsBlob))

	// Decode buses sub-message
	var buses busesData
	if len(busesBlob) > 0 {
		buses = decodeBusesBlob(busesBlob)
	}

	// Decode channels sub-message
	var channels []channelEntry
	if len(channelsBlob) > 0 {
		channels = decodeChannelsBlob(channelsBlob)
	}

	// Build JSON for DB storage — wrap with {"buses": ...} for API compatibility
	busesWrapped := map[string]interface{}{"buses": buses}
	busesJSON, _ := json.Marshal(busesWrapped)

	hardwareInfo := map[string]interface{}{
		"platform":       platform,
		"resource_count": resourceCount,
		"buses":          buses,
		"channel_count":  len(channels),
	}
	hwInfoJSON, _ := json.Marshal(hardwareInfo)

	logger.Infof("[%s] ResourceReport decoded: %d uart, %d i2c, %d spi, %d gpio, %d adc, %d channels",
		deviceID, len(buses.UART), len(buses.I2C), len(buses.SPI), len(buses.GPIO), len(buses.ADC), len(channels))

	// Find node
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Warnf("[%s] Node not found for ResourceReport", deviceID)
		return
	}

	// Update node: capabilities (buses JSON), hardware_info (full JSON), platform
	updates := map[string]interface{}{
		"capabilities":  string(busesJSON),
		"hardware_info": string(hwInfoJSON),
		"platform":      platform,
	}
	m.db.Model(&node).Updates(updates)

	// Upsert channels
	for _, ch := range channels {
		busTypeStr := busTypeToString(ch.BusType)
		hwIDStr := strconv.FormatUint(ch.HardwareID, 16)

		// Build bus_config JSON from raw bytes
		busConfigStr := ""
		if len(ch.BusConfig) > 0 {
			busConfigStr = fmt.Sprintf("%x", ch.BusConfig)
		}

		// Build template_ids comma-separated string
		templateIDsStr := ""
		for i, tid := range ch.TemplateIDs {
			if i > 0 {
				templateIDsStr += ","
			}
			templateIDsStr += strconv.FormatUint(tid, 10)
		}

		channel := models.Channel{
			NodeID:       node.NodeID,
			HardwareType: busTypeStr,
			HardwareID:   hwIDStr,
			BusType:      busTypeStr,
			BusConfig:    busConfigStr,
			IntervalMs:   int(ch.IntervalMs),
			TemplateIDs:  templateIDsStr,
			Enabled:      ch.Enabled,
		}

		// Upsert: find by node_id + hardware_id, update or create
		var existing models.Channel
		if err := m.db.Where("node_id = ? AND hardware_id = ? AND deleted_at IS NULL", node.NodeID, hwIDStr).First(&existing).Error; err == nil {
			// Update existing
			m.db.Model(&existing).Updates(map[string]interface{}{
				"hardware_type": busTypeStr,
				"bus_type":      busTypeStr,
				"bus_config":    busConfigStr,
				"interval_ms":   int(ch.IntervalMs),
				"template_ids":  templateIDsStr,
				"enabled":       ch.Enabled,
			})
		} else {
			// Create new
			channel.NodeID = node.NodeID
			m.db.Create(&channel)
		}
		logger.Infof("[%s] Upserted channel: hw_id=%s type=%s enabled=%v", deviceID, hwIDStr, busTypeStr, ch.Enabled)
	}

	// Build buses map for WebSocket event
	var busesMap map[string]interface{}
	json.Unmarshal(busesJSON, &busesMap)

	// WebSocket push: node_resources_updated
	m.wsHub.BroadcastEvent(events.NodeResourcesUpdated, map[string]interface{}{
		"node_id":        deviceID,
		"resource_count": resourceCount,
		"platform":       platform,
		"buses":          busesMap,
		"channel_count":  len(channels),
	})
}
