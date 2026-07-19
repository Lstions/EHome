package nodemgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

type pwmEntry struct {
	ID                string `json:"id"`
	Channel           uint64 `json:"channel"`
	TimerCount        uint64 `json:"timer_count"`
	MaxResolutionBits uint64 `json:"max_resolution_bits"`
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
	PWM  []pwmEntry  `json:"pwm,omitempty"`
}

// commandEngineReport is deliberately transport-only: it declares bounded
// execution resources but contains no driver or vendor protocol semantics.
type commandEngineReport struct {
	Revision             uint32 `json:"revision"`
	BootID               string `json:"boot_id"`
	SupportsChannelCmdV2 bool   `json:"supports_channel_cmd_v2"`
	SupportsBoundedBatch bool   `json:"supports_bounded_batch"`
	SupportsFinally      bool   `json:"supports_finally"`
	MaxBatchSteps        uint32 `json:"max_batch_steps"`
	MaxTXBytes           uint32 `json:"max_tx_bytes"`
	MaxRXBytes           uint32 `json:"max_rx_bytes"`
	MaxStepTimeoutMS     uint32 `json:"max_step_timeout_ms"`
	RAMDedupEntries      uint32 `json:"ram_dedup_entries"`
}

// manifestCapacityReport is the collector's ConfigManifest storage contract.
// It is intentionally independent of action semantics and comes from the
// firmware constants that own the actual allocation.
type manifestCapacityReport struct {
	MaxTemplates   uint32 `json:"max_templates"`
	MaxChannels    uint32 `json:"max_channels"`
	MaxTemplateIDs uint32 `json:"max_template_ids"`
}

func decodeManifestCapacity(data []byte) (manifestCapacityReport, error) {
	var report manifestCapacityReport
	if len(data) == 0 {
		return report, fmt.Errorf("empty manifest_capacity")
	}
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return report, err
	}
	seen := [4]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return report, err
		}
		if field.FieldNum == 0 || field.FieldNum > 3 || seen[field.FieldNum] || field.WireType != frame.WireVarint {
			return report, fmt.Errorf("invalid manifest_capacity field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
		value := frame.GetUint64(field)
		if value == 0 || value > 256 {
			return report, fmt.Errorf("manifest_capacity field %d out of bounds", field.FieldNum)
		}
		switch field.FieldNum {
		case 1:
			report.MaxTemplates = uint32(value)
		case 2:
			report.MaxChannels = uint32(value)
		case 3:
			report.MaxTemplateIDs = uint32(value)
		}
	}
	for _, required := range []uint8{1, 2, 3} {
		if !seen[required] {
			return report, fmt.Errorf("missing manifest_capacity field %d", required)
		}
	}
	return report, nil
}

func decodeCommandEngine(data []byte) (commandEngineReport, error) {
	var report commandEngineReport
	if len(data) == 0 {
		return report, fmt.Errorf("empty command_engine")
	}
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return report, err
	}
	seen := [11]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return report, err
		}
		if field.FieldNum == 0 || field.FieldNum > 10 || seen[field.FieldNum] {
			return report, fmt.Errorf("invalid or duplicate field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
		if field.FieldNum == 2 {
			if field.WireType != frame.WireLengthDelimited {
				return report, fmt.Errorf("boot_id wire type")
			}
			report.BootID = frame.GetString(field)
			if len(report.BootID) == 0 || len(report.BootID) > 32 {
				return report, fmt.Errorf("boot_id length")
			}
			continue
		}
		if field.WireType != frame.WireVarint {
			return report, fmt.Errorf("field %d wire type", field.FieldNum)
		}
		value := frame.GetUint64(field)
		if value > uint64(^uint32(0)) {
			return report, fmt.Errorf("field %d overflow", field.FieldNum)
		}
		boolValue := value == 1
		if field.FieldNum == 3 || field.FieldNum == 4 || field.FieldNum == 5 {
			if value > 1 {
				return report, fmt.Errorf("field %d non-canonical bool", field.FieldNum)
			}
			switch field.FieldNum {
			case 3:
				report.SupportsChannelCmdV2 = boolValue
			case 4:
				report.SupportsBoundedBatch = boolValue
			case 5:
				report.SupportsFinally = boolValue
			}
			continue
		}
		switch field.FieldNum {
		case 1:
			report.Revision = uint32(value)
		case 6:
			report.MaxBatchSteps = uint32(value)
		case 7:
			report.MaxTXBytes = uint32(value)
		case 8:
			report.MaxRXBytes = uint32(value)
		case 9:
			report.MaxStepTimeoutMS = uint32(value)
		case 10:
			report.RAMDedupEntries = uint32(value)
		}
	}
	for _, required := range []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		if !seen[required] {
			return report, fmt.Errorf("missing field %d", required)
		}
	}
	if report.MaxTXBytes == 0 || report.MaxTXBytes > 128 || report.MaxRXBytes == 0 || report.MaxRXBytes > 256 || report.MaxStepTimeoutMS == 0 || report.MaxStepTimeoutMS > 30000 {
		return report, fmt.Errorf("reported bounds invalid")
	}
	if report.SupportsBoundedBatch {
		if report.MaxBatchSteps < 2 || report.MaxBatchSteps > 8 {
			return report, fmt.Errorf("bounded batch max steps invalid")
		}
	} else if report.MaxBatchSteps != 0 {
		return report, fmt.Errorf("max_batch_steps requires bounded batch capability")
	}
	if report.SupportsChannelCmdV2 && !report.SupportsFinally {
		return report, fmt.Errorf("ChannelCmdV2 requires final capability")
	}
	return report, nil
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

func validateKnownFields(data []byte, schema map[uint8]uint8, required []uint8, repeated map[uint8]bool) error {
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return err
	}
	seen := map[uint8]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			for _, number := range required {
				if !seen[number] {
					return fmt.Errorf("missing required field %d", number)
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		expected, ok := schema[field.FieldNum]
		if !ok || field.WireType != expected {
			return fmt.Errorf("field %d has wire type %d", field.FieldNum, field.WireType)
		}
		if seen[field.FieldNum] && !repeated[field.FieldNum] {
			return fmt.Errorf("duplicate field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
	}
}

func validateFieldSequence(data []byte) error {
	return validateKnownFields(data, map[uint8]uint8{1: frame.WireVarint, 2: frame.WireLengthDelimited, 3: frame.WireVarint, 4: frame.WireVarint, 5: frame.WireVarint, 6: frame.WireVarint, 7: frame.WireLengthDelimited, 8: frame.WireVarint}, []uint8{1}, nil)
}

func validateGenericFieldSequence(data []byte) error {
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return err
	}
	for {
		_, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func resourceEntrySchema(kind string, outer uint8) (map[uint8]uint8, []uint8, map[uint8]bool, error) {
	var schema map[uint8]uint8
	required := []uint8{1}
	repeated := map[uint8]bool{}
	if kind == "channels" {
		if outer != 1 {
			return nil, nil, nil, fmt.Errorf("unexpected channel entry field %d", outer)
		}
		schema = map[uint8]uint8{1: frame.WireVarint, 2: frame.WireVarint, 3: frame.WireVarint, 4: frame.WireVarint, 5: frame.WireVarint, 6: frame.WireLengthDelimited, 7: frame.WireVarint, 8: frame.WireVarint}
		repeated[7] = true
		return schema, required, repeated, nil
	}
	switch outer {
	case 1, 2:
		schema = map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireVarint, 3: frame.WireVarint, 4: frame.WireVarint, 5: frame.WireVarint, 6: frame.WireVarint}
	case 3:
		schema = map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireVarint, 3: frame.WireVarint, 4: frame.WireVarint, 5: frame.WireVarint, 6: frame.WireVarint, 7: frame.WireVarint, 8: frame.WireVarint}
	case 4:
		schema = map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireVarint}
		required = []uint8{1, 2}
	case 5:
		schema = map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireVarint, 3: frame.WireVarint, 4: frame.WireVarint, 5: frame.WireVarint}
	case 6:
		schema = map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireVarint, 3: frame.WireVarint, 4: frame.WireVarint}
		required = []uint8{1, 2, 3, 4}
	default:
		return nil, nil, nil, fmt.Errorf("unexpected bus entry field %d", outer)
	}
	return schema, required, repeated, nil
}

func validateNestedResourceBlob(data []byte, kind string) error {
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return err
	}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			return nil
		}
		if err != nil {
			return err
		}
		if field.WireType != frame.WireLengthDelimited {
			return fmt.Errorf("resource entry field %d has wire type %d", field.FieldNum, field.WireType)
		}
		schema, required, repeated, schemaErr := resourceEntrySchema(kind, field.FieldNum)
		if schemaErr != nil {
			return schemaErr
		}
		if err := validateKnownFields(frame.GetBytes(field), schema, required, repeated); err != nil {
			return fmt.Errorf("resource entry field %d: %w", field.FieldNum, err)
		}
	}
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

func decodePWMEntry(data []byte) pwmEntry {
	var e pwmEntry
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
			e.Channel = frame.GetUint64(field)
		case 3:
			e.TimerCount = frame.GetUint64(field)
		case 4:
			e.MaxResolutionBits = frame.GetUint64(field)
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
		case 6: // pwm_entry (repeated)
			buses.PWM = append(buses.PWM, decodePWMEntry(frame.GetBytes(field)))
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

// decodeDmaChannel decodes a single DMA channel sub-message (ResourceReport field 8)
func decodeDmaChannel(data []byte) (models.DmaChannelInfo, bool) {
	var ch models.DmaChannelInfo
	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return ch, false
	}
	valid := true
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1: // dma_id
			v := frame.GetUint64(field)
			if v > uint64(^uint32(0)) {
				valid = false
				break
			}
			ch.DmaID = uint32(v)
		case 2: // name
			ch.Name = frame.GetString(field)
		case 3: // dma_type
			v := frame.GetUint64(field)
			if v > 255 {
				valid = false
				break
			}
			ch.DmaType = uint8(v)
		case 4: // capabilities
			v := frame.GetUint64(field)
			if v > 255 {
				valid = false
				break
			}
			ch.Capabilities = uint8(v)
		case 5: // max_burst
			v := frame.GetUint64(field)
			if v > uint64(^uint32(0)) {
				valid = false
				break
			}
			ch.MaxBurst = uint32(v)
		case 6: // state
			v := frame.GetUint64(field)
			if v > 255 {
				valid = false
				break
			}
			ch.State = uint8(v)
		case 7: // bound_to
			ch.BoundTo = frame.GetString(field)
		case 8: // compatible_bus
			v := frame.GetUint64(field)
			if v > 255 {
				valid = false
				break
			}
			ch.CompatibleBus = uint8(v)
		}
	}
	if !valid {
		return models.DmaChannelInfo{}, false
	}
	return ch, true
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
	var dmaChannels []models.DmaChannelInfo
	var commandEngine commandEngineReport
	var manifestCapacity *manifestCapacityReport
	seenTop := map[uint8]bool{}

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			logger.Warnf("[%s] Rejecting malformed ResourceReport: %v", deviceID, err)
			return
		}
		if seenTop[field.FieldNum] && field.FieldNum != 8 {
			logger.Warnf("[%s] duplicate ResourceReport field %d", deviceID, field.FieldNum)
			return
		}
		seenTop[field.FieldNum] = true
		switch field.FieldNum {
		case 1:
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid platform wire type", deviceID)
				return
			}
			platform = frame.GetString(field)
		case 2:
			if field.WireType != frame.WireVarint {
				logger.Warnf("[%s] invalid resource_count wire type", deviceID)
				return
			}
			resourceCount = frame.GetUint64(field)
		case 3:
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid buses wire type", deviceID)
				return
			}
			busesBlob = frame.GetBytes(field)
		case 4:
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid channels wire type", deviceID)
				return
			}
			channelsBlob = frame.GetBytes(field)
		case 8: // dma_channels (repeated DmaChannel sub-messages)
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid DMA wire type", deviceID)
				return
			}
			dmaData := frame.GetBytes(field)
			if err := validateFieldSequence(dmaData); err != nil {
				logger.Warnf("[%s] Rejecting malformed DMA resource: %v", deviceID, err)
				return
			}
			dma, ok := decodeDmaChannel(dmaData)
			if !ok {
				logger.Warnf("[%s] Rejecting overflowing DMA resource", deviceID)
				return
			}
			dmaChannels = append(dmaChannels, dma)
		case 9: // command_engine capability sub-message
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid command_engine wire type", deviceID)
				return
			}
			decoded, err := decodeCommandEngine(frame.GetBytes(field))
			if err != nil {
				logger.Warnf("[%s] Rejecting malformed command_engine: %v", deviceID, err)
				return
			}
			commandEngine = decoded
		case 10: // ConfigManifest capacity sub-message (optional for old firmware)
			if field.WireType != frame.WireLengthDelimited {
				logger.Warnf("[%s] invalid manifest_capacity wire type", deviceID)
				return
			}
			decoded, err := decodeManifestCapacity(frame.GetBytes(field))
			if err != nil {
				logger.Warnf("[%s] Rejecting malformed manifest_capacity: %v", deviceID, err)
				return
			}
			manifestCapacity = &decoded
		}
	}
	for _, required := range []uint8{1, 2, 3} {
		if !seenTop[required] {
			logger.Warnf("[%s] missing ResourceReport field %d", deviceID, required)
			return
		}
	}

	logger.Infof("[%s] ResourceReport: platform=%s count=%d buses_blob=%d channels_blob=%d",
		deviceID, platform, resourceCount, len(busesBlob), len(channelsBlob))

	// Decode buses sub-message
	var buses busesData
	if len(busesBlob) > 0 {
		if err := validateNestedResourceBlob(busesBlob, "buses"); err != nil {
			logger.Warnf("[%s] Rejecting malformed buses blob: %v", deviceID, err)
			return
		}
		buses = decodeBusesBlob(busesBlob)
	}

	// Decode channels sub-message
	var channels []channelEntry
	if len(channelsBlob) > 0 {
		if err := validateNestedResourceBlob(channelsBlob, "channels"); err != nil {
			logger.Warnf("[%s] Rejecting malformed channels blob: %v", deviceID, err)
			return
		}
		channels = decodeChannelsBlob(channelsBlob)
	}

	// Build JSON for DB storage — wrap with {"buses": ...} for API compatibility
	busesWrapped := map[string]interface{}{"buses": buses}
	busesJSON, _ := json.Marshal(busesWrapped)

	hardwareInfo := map[string]interface{}{
		"platform":       platform,
		"resource_count": resourceCount,
		"buses":          buses,
		// This is the firmware-applied truth used by the control domain.  It
		// must be persisted with the capability report rather than reduced to
		// a count, otherwise a stale DB Channel can be dispatched to a C6 that
		// rejected or has not yet applied its ConfigManifest.
		"channels":       channels,
		"channel_count":  len(channels),
		"command_engine": commandEngine,
	}
	if manifestCapacity != nil {
		hardwareInfo["manifest_capacity"] = manifestCapacity
	}
	hwInfoJSON, _ := json.Marshal(hardwareInfo)

	logger.Infof("[%s] ResourceReport decoded: %d uart, %d i2c, %d spi, %d gpio, %d pwm, %d adc, %d channels, %d dma",
		deviceID, len(buses.UART), len(buses.I2C), len(buses.SPI), len(buses.GPIO), len(buses.PWM), len(buses.ADC), len(channels), len(dmaChannels))

	// Find node
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Warnf("[%s] Node not found for ResourceReport", deviceID)
		return
	}

	// Update node: capabilities (buses JSON), hardware_info (full JSON), platform, dma_channels
	now := time.Now().UTC()
	commandCapabilities, _ := json.Marshal(commandEngine)
	updates := map[string]interface{}{
		"capabilities":                string(busesJSON),
		"hardware_info":               string(hwInfoJSON),
		"platform":                    platform,
		"boot_id":                     commandEngine.BootID,
		"resource_reported_at":        now,
		"command_engine_revision":     commandEngine.Revision,
		"command_engine_capabilities": string(commandCapabilities),
	}
	// An empty reported DMA list is authoritative and clears stale state.
	dmaJSON, err := json.Marshal(dmaChannels)
	if err != nil {
		logger.Errorf("[%s] Failed to encode DMA resources: %v", deviceID, err)
		return
	}
	updates["dma_channels"] = string(dmaJSON)
	if err := m.db.Model(&node).Updates(updates).Error; err != nil {
		logger.Errorf("[%s] Failed to persist ResourceReport: %v", deviceID, err)
		return
	}

	// Build buses map for WebSocket event
	var busesMap map[string]interface{}
	json.Unmarshal(busesJSON, &busesMap)

	// WebSocket push: node_resources_updated
	wsData := map[string]interface{}{
		"node_id":        deviceID,
		"resource_count": resourceCount,
		"platform":       platform,
		"buses":          busesMap,
		"channel_count":  len(channels),
		"command_engine": commandEngine,
	}
	wsData["dma_channels"] = dmaChannels
	m.wsHub.BroadcastEvent(events.NodeResourcesUpdated, wsData)
}
