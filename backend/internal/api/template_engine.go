package api

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
	Name           string `json:"name"`
	Type           string `json:"type"`             // "read" or "write"
	CommandTemplate string `json:"command_template"` // hex template with placeholders
	ReadSize       uint32 `json:"read_size"`        // expected response bytes (for read ops)
	TimeoutMs      int    `json:"timeout_ms"`       // timeout in ms (for read ops, default 3000)
	ResponseParser string `json:"response_parser"`  // "modbus_uint16", "modbus_uint16_div10", etc.
	Unit           string `json:"unit"`             // unit string for the response value
	ResponseUnit   string `json:"response_unit"`    // alias: some configs use response_unit instead of unit
	PostAction     string `json:"post_action"`      // "update_connection_address", "update_connection_baud", etc.
	Label          string `json:"label"`            // human-readable operation label
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
	result := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		parts := placeholderRe.FindStringSubmatch(match)
		varName := parts[1]
		fmtSpec := parts[2]

		if varName == "crc" {
			return match // leave {crc} for second pass
		}

		// Resolve the variable value
		val, err := resolveVar(varName, vars)
		if err != nil {
			return match // leave unresolved
		}

		// Format the value
		if fmtSpec != "" {
			return formatValue(val, fmtSpec)
		}
		// Default: format as decimal
		return fmt.Sprintf("%d", val)
	})

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

// formatValue formats a uint64 value using a printf-style format spec.
// Supports common Go fmt verbs: d, x, X, o, b, and width/precision like 02X, 04X.
func formatValue(val uint64, spec string) string {
	// Handle common format specs like "02X", "04X", "d", "02d", etc.
	// We use fmt.Sprintf with the % prefix
	return fmt.Sprintf("%"+spec, val)
}

// ParseModbusResponse parses a Modbus response according to the response_parser strategy.
// rawData is the full response bytes from the device.
//
// Supported parsers:
//   - "modbus_uint16": Modbus FC03 response, extracts uint16 from data[3:5]
//   - "modbus_uint16_div10": Same as above, then divides by 10.0
func ParseModbusResponse(rawData []byte, parser string) (float64, error) {
	switch parser {
	case "modbus_uint16":
		if len(rawData) < 5 {
			return 0, fmt.Errorf("response too short for modbus_uint16: got %d bytes, need at least 5", len(rawData))
		}
		val := binary.BigEndian.Uint16(rawData[3:5])
		return float64(val), nil

	case "modbus_uint16_div10":
		if len(rawData) < 5 {
			return 0, fmt.Errorf("response too short for modbus_uint16_div10: got %d bytes, need at least 5", len(rawData))
		}
		val := binary.BigEndian.Uint16(rawData[3:5])
		return float64(val) / 10.0, nil

	default:
		return 0, fmt.Errorf("unknown response_parser: %s", parser)
	}
}
