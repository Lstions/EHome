package parser

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// ParserRule defines a JSON-driven response parsing rule for operation-level single-value parsing.
// This is used when operations specify a JSON rule as their response_parser (P3-3 feature).
//
// Example JSON:
//
//	{"byte_offset":3, "byte_length":2, "data_type":"uint16", "scale":0.1, "unit":"°C"}
type ParserRule struct {
	// Type is the parsing strategy: "modbus_register", "raw_bytes", or "ascii".
	Type string `json:"type"`
	// ByteOffset is the 0-based starting byte offset in the raw data.
	ByteOffset int `json:"byte_offset"`
	// ByteLength is the number of bytes to extract.
	ByteLength int `json:"byte_length"`
	// DataType is the data type: "uint16", "int16", "uint32", "int32", "float32", "ascii".
	DataType string `json:"data_type"`
	// Endian is the byte order: "big" (default) or "little".
	Endian string `json:"endian"`
	// Scale is the scaling factor (1.0 = no scaling). Defaults to 1.0 if 0.
	Scale float64 `json:"scale"`
	// Offset is the additive offset after scaling (0.0 = no offset).
	Offset float64 `json:"offset"`
	// Unit is the measurement unit string.
	Unit string `json:"unit"`
}

// ParseResponseByRule parses a response using a JSON-defined ParserRule.
// ruleJSON is the JSON string defining the rule, rawData is the full response bytes.
// Returns the parsed value, unit, and any error.
//
// This is fully backward compatible with the P3-3 implementation.
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
