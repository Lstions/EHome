package parser

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// Field represents a single parsed sensor field.
type Field struct {
	Name  string
	Value float64
	Unit  string
}

// Parser is the unified interface for all data parsing.
// Implementations extract named sensor fields from raw byte data.
type Parser interface {
	// Parse extracts sensor fields from raw bytes.
	Parse(raw []byte) ([]Field, error)
}

// ConfigParser parses raw data using DeviceConfig.Parser JSONB definition.
// This is the PRIMARY parser — replaces both Driver.ParseData and ResponseParser.
//
// The parser JSONB format is:
//
//	{"data_format":"modbus","fields":[{"name":"temperature","type":"int16","scale":0.01,...}]}
type ConfigParser struct {
	// DataFormat specifies the framing: "modbus" (strip addr+func+count header, validate CRC),
	// "binary" (raw bytes from offset 0), or "ascii".
	DataFormat string
	// Fields defines the extraction rules for each sensor value.
	Fields []FieldRule
}

// FieldRule defines how to extract one sensor field from raw bytes.
type FieldRule struct {
	// Name is the sensor field name (e.g., "temperature", "rainfall").
	Name string `json:"name"`
	// Type is the data type: "uint16", "int16", "uint32", "int32", "float32".
	Type string `json:"type"`
	// Unit is the measurement unit (e.g., "°C", "mm", "Lux").
	Unit string `json:"unit"`
	// Scale is the scaling factor applied after extraction (multiply).
	// A value of 0 defaults to 1.0 at parse time.
	Scale float64 `json:"scale"`
	// Offset is the byte offset in the data payload where this field starts.
	// For data_format="modbus", this is the offset after the 3-byte header (addr+func+byte_count).
	Offset float64 `json:"offset"`
	// Length is the byte length of this field (e.g., 2 for uint16, 4 for uint32/float32).
	Length int `json:"length"`
	// Endian is the byte order: "big" (default) or "little".
	Endian string `json:"endian"`
}

// configParserJSON is the intermediate struct for unmarshaling DeviceConfig.Parser JSONB.
type configParserJSON struct {
	DataFormat string      `json:"data_format"`
	Fields     []FieldRule `json:"fields"`
}

// NewConfigParser creates a ConfigParser from DeviceConfig.Parser JSONB.
// The JSONB format is: {"data_format":"modbus","fields":[...]}.
func NewConfigParser(parserJSON json.RawMessage) (*ConfigParser, error) {
	if len(parserJSON) == 0 || string(parserJSON) == "null" || string(parserJSON) == "{}" {
		return nil, fmt.Errorf("empty parser config")
	}
	var cpj configParserJSON
	if err := json.Unmarshal(parserJSON, &cpj); err != nil {
		return nil, fmt.Errorf("invalid parser JSON: %w", err)
	}
	if len(cpj.Fields) == 0 {
		return nil, fmt.Errorf("parser config has no fields")
	}
	if cpj.DataFormat == "" {
		cpj.DataFormat = "binary"
	}
	return &ConfigParser{
		DataFormat: cpj.DataFormat,
		Fields:     cpj.Fields,
	}, nil
}

// Parse implements the Parser interface — the main parsing logic.
//
// For data_format="modbus": strips the Modbus header (addr+func+byte_count = 3 bytes),
// validates CRC, then extracts fields from the data portion.
// For data_format="binary": extracts fields directly from the raw bytes.
func (cp *ConfigParser) Parse(raw []byte) ([]Field, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty raw data")
	}

	var data []byte
	var err error

	switch cp.DataFormat {
	case "modbus":
		data, err = StripModbusHeader(raw)
		if err != nil {
			return nil, fmt.Errorf("modbus strip header: %w", err)
		}
	case "binary":
		data = raw
	default:
		data = raw
	}

	var fields []Field
	for i, rule := range cp.Fields {
		field, fieldErr := cp.parseField(data, rule)
		if fieldErr != nil {
			return nil, fmt.Errorf("field %d (%q): %w", i, rule.Name, fieldErr)
		}
		fields = append(fields, field)
	}

	return fields, nil
}

// parseField extracts a single field from data according to the FieldRule.
func (cp *ConfigParser) parseField(data []byte, rule FieldRule) (Field, error) {
	// Default endian
	endian := rule.Endian
	if endian == "" {
		endian = "big"
	}

	// Default scale (0 would zero out the value, so default to 1.0)
	scale := rule.Scale
	if scale == 0 {
		scale = 1.0
	}

	// Determine byte offset and length
	byteOffset := int(rule.Offset)
	byteLength := rule.Length

	// Auto-determine length from type if not specified
	if byteLength == 0 {
		byteLength = typeLength(rule.Type)
	}

	if byteOffset < 0 || byteLength <= 0 {
		return Field{}, fmt.Errorf("invalid offset=%d or length=%d", byteOffset, byteLength)
	}
	if byteOffset+byteLength > len(data) {
		return Field{}, fmt.Errorf("data too short: need offset %d + length %d = %d bytes, got %d",
			byteOffset, byteLength, byteOffset+byteLength, len(data))
	}

	fieldData := data[byteOffset : byteOffset+byteLength]

	// Parse by data type
	var value float64
	switch rule.Type {
	case "uint16":
		if len(fieldData) < 2 {
			return Field{}, fmt.Errorf("uint16 needs 2 bytes, got %d", len(fieldData))
		}
		if endian == "little" {
			value = float64(uint16(fieldData[1])<<8 | uint16(fieldData[0]))
		} else {
			value = float64(uint16(fieldData[0])<<8 | uint16(fieldData[1]))
		}
	case "int16":
		if len(fieldData) < 2 {
			return Field{}, fmt.Errorf("int16 needs 2 bytes, got %d", len(fieldData))
		}
		var v int16
		if endian == "little" {
			v = int16(uint16(fieldData[1])<<8 | uint16(fieldData[0]))
		} else {
			v = int16(uint16(fieldData[0])<<8 | uint16(fieldData[1]))
		}
		value = float64(v)
	case "uint32":
		if len(fieldData) < 4 {
			return Field{}, fmt.Errorf("uint32 needs 4 bytes, got %d", len(fieldData))
		}
		if endian == "little" {
			value = float64(uint32(fieldData[3])<<24 | uint32(fieldData[2])<<16 | uint32(fieldData[1])<<8 | uint32(fieldData[0]))
		} else {
			value = float64(uint32(fieldData[0])<<24 | uint32(fieldData[1])<<16 | uint32(fieldData[2])<<8 | uint32(fieldData[3]))
		}
	case "int32":
		if len(fieldData) < 4 {
			return Field{}, fmt.Errorf("int32 needs 4 bytes, got %d", len(fieldData))
		}
		var v int32
		if endian == "little" {
			v = int32(uint32(fieldData[3])<<24 | uint32(fieldData[2])<<16 | uint32(fieldData[1])<<8 | uint32(fieldData[0]))
		} else {
			v = int32(uint32(fieldData[0])<<24 | uint32(fieldData[1])<<16 | uint32(fieldData[2])<<8 | uint32(fieldData[3]))
		}
		value = float64(v)
	case "float32":
		if len(fieldData) < 4 {
			return Field{}, fmt.Errorf("float32 needs 4 bytes, got %d", len(fieldData))
		}
		var bits uint32
		if endian == "little" {
			bits = binary.LittleEndian.Uint32(fieldData)
		} else {
			bits = binary.BigEndian.Uint32(fieldData)
		}
		value = float64(math.Float32frombits(bits))
	default:
		return Field{}, fmt.Errorf("unsupported data type: %s", rule.Type)
	}

	// Apply scale
	value *= scale

	return Field{
		Name:  rule.Name,
		Value: value,
		Unit:  rule.Unit,
	}, nil
}

// ParseSingle parses raw data and returns a single value (for operation responses).
// This replaces the old ResponseParser.Parse(raw) → (float64, string, error).
func (cp *ConfigParser) ParseSingle(raw []byte) (float64, string, error) {
	fields, err := cp.Parse(raw)
	if err != nil {
		return 0, "", err
	}
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("no fields parsed")
	}
	return fields[0].Value, fields[0].Unit, nil
}

// typeLength returns the byte length for a given data type name.
func typeLength(dataType string) int {
	switch dataType {
	case "uint16", "int16":
		return 2
	case "uint32", "int32", "float32":
		return 4
	default:
		return 0
	}
}

// Registry holds named parsers for operation response_parser names like "modbus_uint16".
type Registry struct {
	parsers map[string]Parser
}

// globalRegistry is the default global parser registry.
var globalRegistry = NewRegistry()

// NewRegistry creates a new empty parser registry.
func NewRegistry() *Registry {
	return &Registry{parsers: make(map[string]Parser)}
}

// Register adds a named parser to the registry.
func (r *Registry) Register(name string, p Parser) {
	r.parsers[name] = p
}

// Get retrieves a parser by name from the registry.
// Returns an error if the parser is not found.
func (r *Registry) Get(name string) (Parser, error) {
	p, ok := r.parsers[name]
	if !ok {
		return nil, fmt.Errorf("parser not found: %s", name)
	}
	return p, nil
}

// Register adds a named parser to the global registry.
func Register(name string, p Parser) {
	globalRegistry.Register(name, p)
}

// Get retrieves a parser by name from the global registry.
func Get(name string) (Parser, error) {
	return globalRegistry.Get(name)
}
