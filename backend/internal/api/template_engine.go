package api

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ModbusCRC16 computes the standard Modbus RTU CRC16 over the given data.
// Returns the 2-byte CRC in little-endian order (low byte first).
func ModbusCRC16(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc >>= 1
				crc ^= 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// OperationConfig represents a single operation definition from DeviceConfig.Operations JSONB.
// Example JSON:
//
//	{
//	  "name": "查询雨量",
//	  "type": "read",
//	  "command_template": "{addr:02X}03 00 00 00 02 {crc}",
//	  "read_size": 9,
//	  "timeout_ms": 3000,
//	  "response_parser": "modbus_uint16",
//	  "unit": "mm",
//	  "post_action": ""
//	}
type OperationConfig struct {
	Name            string `json:"name"`
	Type            string `json:"type"`              // "read" or "write"
	CommandTemplate string `json:"command_template"`  // hex template with placeholders
	ReadSize        uint32 `json:"read_size"`         // expected response bytes (for read ops)
	TimeoutMs       int    `json:"timeout_ms"`        // timeout in ms (for read ops, default 3000)
	ResponseParser  string `json:"response_parser"`   // "modbus_uint16", "modbus_uint16_div10", etc.
	Unit            string `json:"unit"`              // unit string for the response value
	ResponseUnit    string `json:"response_unit"`     // alias: some configs use response_unit instead of unit
	PostAction      string `json:"post_action"`       // "update_connection_address", "update_connection_baud", etc.
	Label           string `json:"label"`             // human-readable operation label
	VerifyOperation string `json:"verify_operation"`  // P2-4: Name of a read operation to verify write success
}

// TemplateVars holds the variables available for template substitution.
type TemplateVars struct {
	Addr    uint64 // current device address (from EdgeDevice.HardwareID or Connection.address)
	Params  map[string]interface{} // user-supplied params from the execute request
}

// placeholderRe matches {var} or {var:format} patterns like {addr:02X}, {new_addr:04X}, {crc}
var placeholderRe = regexp.MustCompile(`\{(\w+)(?::([^}]+))?\}`)

// RenderCommandTemplate renders a command template into raw bytes.
//
// Template syntax:
//   - {addr:02X}  → format the 'addr' variable as 02X
//   - {new_addr:04X} → format the 'new_addr' param as 04X
//   - {baud_code:04X} → format the 'baud_code' param as 04X
//   - {crc}       → compute Modbus CRC16 over all preceding bytes, append as 2-byte little-endian
//
// The template is a hex string with optional spaces (which are stripped).
// Placeholders produce hex digits that become part of the hex string.
// After all substitutions (except {crc}), the hex string is decoded to bytes,
// CRC is computed if {crc} is present, and the CRC bytes are appended.
func RenderCommandTemplate(template string, vars TemplateVars) ([]byte, error) {
	// Normalize: strip spaces
	tmpl := strings.ReplaceAll(template, " ", "")

	// Check if {crc} is present
	hasCRC := strings.Contains(tmpl, "{crc}")

	// First pass: replace all placeholders except {crc}
	// We can't use ReplaceAllStringFunc because it doesn't support error propagation,
	// so we do a manual replacement loop, skipping {crc} placeholders.
	result := tmpl
	searchOffset := 0
	for searchOffset < len(result) {
		match := placeholderRe.FindStringSubmatchIndex(result[searchOffset:])
		if match == nil {
			break
		}
		// Adjust match indices to be relative to full string
		for i := range match {
			if match[i] >= 0 {
				match[i] += searchOffset
			}
		}
		varName := result[match[2]:match[3]]

		if varName == "crc" {
			// Skip {crc} — advance searchOffset past this match
			searchOffset = match[1]
			continue
		}

		fmtSpec := ""
		if match[4] >= 0 {
			fmtSpec = result[match[4]:match[5]]
		}

		// Resolve the variable value
		val, err := resolveVar(varName, vars)
		if err != nil {
			return nil, fmt.Errorf("unresolved template variable: %s: %w", varName, err)
		}

		// Format the value
		var replacement string
		if fmtSpec != "" {
			replacement, err = formatValue(val, fmtSpec)
			if err != nil {
				return nil, fmt.Errorf("invalid format for variable %s: %w", varName, err)
			}
		} else {
			replacement = fmt.Sprintf("%d", val)
		}

		result = result[:match[0]] + replacement + result[match[1]:]
		// Advance searchOffset past the replacement (avoids re-matching within replacement text)
		searchOffset = match[0] + len(replacement)
	}

	// If no CRC, just decode the hex string
	if !hasCRC {
		decoded, err := hex.DecodeString(result)
		if err != nil {
			return nil, fmt.Errorf("invalid hex after template substitution: %q: %w", result, err)
		}
		return decoded, nil
	}

	// Second pass: handle {crc}
	// Split on {crc} — everything before is the data to CRC
	crcSplit := strings.SplitN(result, "{crc}", 2)
	beforeCRC := crcSplit[0]
	afterCRC := ""
	if len(crcSplit) > 1 {
		afterCRC = crcSplit[1]
	}

	// Decode the bytes before CRC
	dataBeforeCRC, err := hex.DecodeString(beforeCRC)
	if err != nil {
		return nil, fmt.Errorf("invalid hex before {crc}: %q: %w", beforeCRC, err)
	}

	// Compute CRC16
	crc := ModbusCRC16(dataBeforeCRC)

	// Append CRC as little-endian (2 hex bytes)
	// Modbus CRC is little-endian: low byte first
	crcLE := []byte{byte(crc & 0xFF), byte(crc >> 8)}
	crcLEHex := hex.EncodeToString(crcLE)

	// Reassemble: before + CRC LE + after
	finalHex := beforeCRC + crcLEHex + afterCRC

	decoded, err := hex.DecodeString(finalHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex after CRC insertion: %q: %w", finalHex, err)
	}

	return decoded, nil
}

// resolveVar looks up a variable name in the template vars.
func resolveVar(name string, vars TemplateVars) (uint64, error) {
	switch name {
	case "addr":
		return vars.Addr, nil
	default:
		// Look in params
		if vars.Params != nil {
			if v, ok := vars.Params[name]; ok {
				return toUint64(v)
			}
		}
		return 0, fmt.Errorf("unknown template variable: %s", name)
	}
}

// toUint64 converts an interface{} to uint64 (from JSON numbers, strings, etc.)
func toUint64(v interface{}) (uint64, error) {
	switch val := v.(type) {
	case float64:
		return uint64(val), nil
	case int:
		return uint64(val), nil
	case int64:
		return uint64(val), nil
	case uint64:
		return val, nil
	case string:
		// Try hex first
		s := strings.TrimSpace(val)
		if strings.HasPrefix(strings.ToLower(s), "0x") {
			return strconv.ParseUint(s[2:], 16, 64)
		}
		return strconv.ParseUint(s, 10, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", v)
	}
}

// fmtSpecRe validates format specifiers for formatValue to prevent format injection.
// Only allows numeric width/precision with integer format verbs (d, X, x, o, b).
var fmtSpecRe = regexp.MustCompile(`^[0-9]*[dXxob]$`)

// formatValue formats a uint64 value using a printf-style format spec.
// Supports common Go fmt verbs: d, x, X, o, b, and width/precision like 02X, 04X.
// Returns an error if the format spec is not in the allowed whitelist.
func formatValue(val uint64, spec string) (string, error) {
	if !fmtSpecRe.MatchString(spec) {
		return "", fmt.Errorf("invalid format spec: %q", spec)
	}
	return fmt.Sprintf("%"+spec, val), nil
}

// ResponseParser is the interface for parsing device response data
type ResponseParser interface {
	Parse(rawData []byte) (value float64, unit string, err error)
}

// parserRegistry maps parser names to implementations
var parserRegistry = map[string]ResponseParser{}

// RegisterParser adds a parser to the global registry
func RegisterParser(name string, p ResponseParser) {
	parserRegistry[name] = p
}

// --- Built-in parsers ---

type modbusUint16Parser struct{}

func (p *modbusUint16Parser) Parse(rawData []byte) (float64, string, error) {
	if len(rawData) < 5 {
		return 0, "", fmt.Errorf("response too short for modbus_uint16: got %d bytes, need at least 5", len(rawData))
	}
	if rawData[1]&0x80 != 0 {
		return 0, "", fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)", rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
	}
	val := binary.BigEndian.Uint16(rawData[3:5])
	return float64(val), "", nil
}

type modbusUint16Div10Parser struct{}

func (p *modbusUint16Div10Parser) Parse(rawData []byte) (float64, string, error) {
	if len(rawData) < 5 {
		return 0, "", fmt.Errorf("response too short for modbus_uint16_div10: got %d bytes, need at least 5", len(rawData))
	}
	if rawData[1]&0x80 != 0 {
		return 0, "", fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)", rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
	}
	val := binary.BigEndian.Uint16(rawData[3:5])
	return float64(val) / 10.0, "", nil
}

type modbusInt16Parser struct{}

func (p *modbusInt16Parser) Parse(rawData []byte) (float64, string, error) {
	if len(rawData) < 5 {
		return 0, "", fmt.Errorf("response too short for modbus_int16: got %d bytes, need at least 5", len(rawData))
	}
	if rawData[1]&0x80 != 0 {
		return 0, "", fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)", rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
	}
	val := int16(binary.BigEndian.Uint16(rawData[3:5]))
	return float64(val), "", nil
}

type modbusUint32Parser struct{}

func (p *modbusUint32Parser) Parse(rawData []byte) (float64, string, error) {
	if len(rawData) < 7 {
		return 0, "", fmt.Errorf("response too short for modbus_uint32: got %d bytes, need at least 7", len(rawData))
	}
	if rawData[1]&0x80 != 0 {
		return 0, "", fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)", rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
	}
	val := binary.BigEndian.Uint32(rawData[3:7])
	return float64(val), "", nil
}

type modbusFloat32Parser struct{}

func (p *modbusFloat32Parser) Parse(rawData []byte) (float64, string, error) {
	if len(rawData) < 7 {
		return 0, "", fmt.Errorf("response too short for modbus_float32: got %d bytes, need at least 7", len(rawData))
	}
	if rawData[1]&0x80 != 0 {
		return 0, "", fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)", rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
	}
	bits := binary.BigEndian.Uint32(rawData[3:7])
	return float64(math.Float32frombits(bits)), "", nil
}

// modbusExceptionMessage returns a human-readable description for Modbus exception codes
func modbusExceptionMessage(code byte) string {
	switch code {
	case 0x01:
		return "illegal function"
	case 0x02:
		return "illegal data address"
	case 0x03:
		return "illegal data value"
	case 0x04:
		return "slave device failure"
	case 0x05:
		return "acknowledge"
	case 0x06:
		return "slave device busy"
	case 0x07:
		return "negative acknowledge"
	case 0x08:
		return "memory parity error"
	case 0x0A:
		return "gateway path unavailable"
	case 0x0B:
		return "gateway target device failed to respond"
	default:
		return fmt.Sprintf("unknown exception code 0x%02X", code)
	}
}

func init() {
	RegisterParser("modbus_uint16", &modbusUint16Parser{})
	RegisterParser("modbus_uint16_div10", &modbusUint16Div10Parser{})
	RegisterParser("modbus_int16", &modbusInt16Parser{})
	RegisterParser("modbus_uint32", &modbusUint32Parser{})
	RegisterParser("modbus_float32", &modbusFloat32Parser{})
}

// P3-3: JSON-driven response parsing rule
type ParserRule struct {
	Type       string  `json:"type"`         // "modbus_register", "raw_bytes", "ascii"
	ByteOffset int     `json:"byte_offset"`  // 数据起始偏移 (0-based)
	ByteLength int     `json:"byte_length"`  // 数据长度 (bytes)
	DataType   string  `json:"data_type"`    // "uint16", "int16", "uint32", "int32", "float32", "ascii"
	Endian     string  `json:"endian"`       // "big" (default), "little"
	Scale      float64 `json:"scale"`        // 缩放因子 (1.0 = no scaling)
	Offset     float64 `json:"offset"`       // 偏移量 (0.0 = no offset)
	Unit       string  `json:"unit"`         // 单位
}

// P3-3: Parse response using a JSON-defined rule
func ParseResponseByRule(ruleJSON string, rawData []byte) (float64, string, error) {
	var rule ParserRule
	if err := json.Unmarshal([]byte(ruleJSON), &rule); err != nil {
		return 0, "", fmt.Errorf("invalid parser rule: %w", err)
	}

	// Default endian
	if rule.Endian == "" {
		rule.Endian = "big"
	}

	// Default scale (0 would zero out the value, so default to 1.0)
	if rule.Scale == 0 {
		rule.Scale = 1.0
	}

	// Boundary check
	if rule.ByteOffset < 0 || rule.ByteLength <= 0 {
		return 0, "", fmt.Errorf("invalid byte_offset=%d or byte_length=%d", rule.ByteOffset, rule.ByteLength)
	}
	if rule.ByteOffset+rule.ByteLength > len(rawData) {
		return 0, "", fmt.Errorf("data too short: need offset %d + length %d = %d bytes, got %d",
			rule.ByteOffset, rule.ByteLength, rule.ByteOffset+rule.ByteLength, len(rawData))
	}

	// Extract bytes
	data := rawData[rule.ByteOffset : rule.ByteOffset+rule.ByteLength]

	// Parse by data type
	var value float64
	switch rule.DataType {
	case "uint16":
		if len(data) < 2 {
			return 0, "", fmt.Errorf("uint16 needs 2 bytes, got %d", len(data))
		}
		if rule.Endian == "little" {
			value = float64(uint16(data[1])<<8 | uint16(data[0]))
		} else {
			value = float64(uint16(data[0])<<8 | uint16(data[1]))
		}
	case "int16":
		if len(data) < 2 {
			return 0, "", fmt.Errorf("int16 needs 2 bytes, got %d", len(data))
		}
		var v int16
		if rule.Endian == "little" {
			v = int16(uint16(data[1])<<8 | uint16(data[0]))
		} else {
			v = int16(uint16(data[0])<<8 | uint16(data[1]))
		}
		value = float64(v)
	case "uint32":
		if len(data) < 4 {
			return 0, "", fmt.Errorf("uint32 needs 4 bytes, got %d", len(data))
		}
		if rule.Endian == "little" {
			value = float64(uint32(data[3])<<24 | uint32(data[2])<<16 | uint32(data[1])<<8 | uint32(data[0]))
		} else {
			value = float64(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
		}
	case "int32":
		if len(data) < 4 {
			return 0, "", fmt.Errorf("int32 needs 4 bytes, got %d", len(data))
		}
		var v int32
		if rule.Endian == "little" {
			v = int32(uint32(data[3])<<24 | uint32(data[2])<<16 | uint32(data[1])<<8 | uint32(data[0]))
		} else {
			v = int32(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
		}
		value = float64(v)
	case "float32":
		if len(data) < 4 {
			return 0, "", fmt.Errorf("float32 needs 4 bytes, got %d", len(data))
		}
		var bits uint32
		if rule.Endian == "little" {
			bits = binary.LittleEndian.Uint32(data)
		} else {
			bits = binary.BigEndian.Uint32(data)
		}
		value = float64(math.Float32frombits(bits))
	case "ascii":
		return 0, string(data), nil
	default:
		return 0, "", fmt.Errorf("unsupported data_type: %s", rule.DataType)
	}

	// Apply scale and offset
	value = value*rule.Scale + rule.Offset

	return value, rule.Unit, nil
}

// ParseModbusResponse parses a Modbus response according to the response_parser strategy.
// rawData is the full response bytes from the device.
//
// Supported parsers (via registry):
//   - "modbus_uint16": Modbus FC03 response, extracts uint16 from data[3:5]
//   - "modbus_uint16_div10": Same as above, then divides by 10.0
//   - "modbus_int16": Modbus FC03 response, extracts int16 from data[3:5]
//   - "modbus_uint32": Modbus FC03 response, extracts uint32 from data[3:7]
//   - "modbus_float32": Modbus FC03 response, extracts IEEE 754 float32 from data[3:7]
//   - JSON rule: If parser starts with '{', parsed as a ParserRule JSON definition (P3-3)
func ParseModbusResponse(rawData []byte, parser string) (float64, error) {
	// P3-3: Try JSON rule parsing first if parser looks like JSON
	if strings.HasPrefix(strings.TrimSpace(parser), "{") {
		value, _, err := ParseResponseByRule(parser, rawData)
		if err != nil {
			return 0, err
		}
		return value, nil
	}

	// Try registry for named parsers
	if p, ok := parserRegistry[parser]; ok {
		val, _, err := p.Parse(rawData)
		return val, err
	}
	// Fallback for unknown parsers
	return 0, fmt.Errorf("unknown response_parser: %s", parser)
}
