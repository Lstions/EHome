package parser

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

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
	// Name is the operation identifier.
	Name string `json:"name"`
	// Type is "read" or "write".
	Type string `json:"type"`
	// CommandTemplate is the hex template with placeholders like {addr:02X} and {crc}.
	CommandTemplate string `json:"command_template"`
	// ReadSize is the expected response bytes for read operations.
	ReadSize uint32 `json:"read_size"`
	// TimeoutMs is the timeout in milliseconds for read operations (default 3000).
	TimeoutMs int `json:"timeout_ms"`
	// ResponseParser is the parser name or JSON rule for parsing the response
	// (e.g., "modbus_uint16", "modbus_uint16_div10", or a JSON ParserRule).
	ResponseParser string `json:"response_parser"`
	// Unit is the unit string for the response value.
	Unit string `json:"unit"`
	// ResponseUnit is an alias: some configs use response_unit instead of unit.
	ResponseUnit string `json:"response_unit"`
	// PostAction is an optional action after successful execution
	// (e.g., "update_connection_address", "update_connection_baud").
	PostAction string `json:"post_action"`
	// Label is the human-readable operation label.
	Label string `json:"label"`
	// VerifyOperation is the name of a read operation to verify write success (P2-4).
	VerifyOperation string `json:"verify_operation"`
}

// TemplateVars holds the variables available for template substitution.
type TemplateVars struct {
	// Addr is the current device address (from EdgeDevice.HardwareID or Connection.address).
	Addr uint64
	// Params are user-supplied parameters from the execute request.
	Params map[string]interface{}
}

// placeholderRe matches {var} or {var:format} patterns like {addr:02X}, {new_addr:04X}, {crc}.
var placeholderRe = regexp.MustCompile(`\{(\w+)(?::([^}]+))?\}`)

// fmtSpecRe validates format specifiers for formatValue to prevent format injection.
// Only allows numeric width/precision with integer format verbs (d, X, x, o, b).
var fmtSpecRe = regexp.MustCompile(`^[0-9]*[dXxob]$`)

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

// toUint64 converts an interface{} to uint64 (from JSON numbers, strings, etc.).
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

// ToUint64 converts an interface{} to uint64 (exported wrapper for cross-package use).
// See toUint64 for the implementation.
func ToUint64(v interface{}) (uint64, error) {
	return toUint64(v)
}

// formatValue formats a uint64 value using a printf-style format spec.
// Supports common Go fmt verbs: d, x, X, o, b, and width/precision like 02X, 04X.
// Returns an error if the format spec is not in the allowed whitelist.
func formatValue(val uint64, spec string) (string, error) {
	if !fmtSpecRe.MatchString(spec) {
		return "", fmt.Errorf("invalid format spec: %q", spec)
	}
	return fmt.Sprintf("%"+spec, val), nil
}
