package parser

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// modbusUint16Parser implements the Parser interface for Modbus uint16 responses.
type modbusUint16Parser struct{}

// Parse extracts a single uint16 value from a Modbus RTU response.
// Data is read from bytes 3-5 (after addr+func+byte_count header).
func (p *modbusUint16Parser) Parse(raw []byte) ([]Field, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("response too short for modbus_uint16: got %d bytes, need at least 5", len(raw))
	}
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}
	val := binary.BigEndian.Uint16(raw[3:5])
	return []Field{{Name: "value", Value: float64(val), Unit: ""}}, nil
}

// modbusUint16Div10Parser implements the Parser interface for Modbus uint16 ÷ 10 responses.
type modbusUint16Div10Parser struct{}

// Parse extracts a uint16 value from a Modbus RTU response and divides by 10.
func (p *modbusUint16Div10Parser) Parse(raw []byte) ([]Field, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("response too short for modbus_uint16_div10: got %d bytes, need at least 5", len(raw))
	}
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}
	val := binary.BigEndian.Uint16(raw[3:5])
	return []Field{{Name: "value", Value: float64(val) / 10.0, Unit: ""}}, nil
}

// modbusInt16Parser implements the Parser interface for Modbus int16 responses.
type modbusInt16Parser struct{}

// Parse extracts a signed int16 value from a Modbus RTU response.
func (p *modbusInt16Parser) Parse(raw []byte) ([]Field, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("response too short for modbus_int16: got %d bytes, need at least 5", len(raw))
	}
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}
	val := int16(binary.BigEndian.Uint16(raw[3:5]))
	return []Field{{Name: "value", Value: float64(val), Unit: ""}}, nil
}

// modbusUint32Parser implements the Parser interface for Modbus uint32 responses.
type modbusUint32Parser struct{}

// Parse extracts a uint32 value from a Modbus RTU response.
func (p *modbusUint32Parser) Parse(raw []byte) ([]Field, error) {
	if len(raw) < 7 {
		return nil, fmt.Errorf("response too short for modbus_uint32: got %d bytes, need at least 7", len(raw))
	}
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}
	val := binary.BigEndian.Uint32(raw[3:7])
	return []Field{{Name: "value", Value: float64(val), Unit: ""}}, nil
}

// modbusFloat32Parser implements the Parser interface for Modbus IEEE 754 float32 responses.
type modbusFloat32Parser struct{}

// Parse extracts an IEEE 754 float32 value from a Modbus RTU response.
func (p *modbusFloat32Parser) Parse(raw []byte) ([]Field, error) {
	if len(raw) < 7 {
		return nil, fmt.Errorf("response too short for modbus_float32: got %d bytes, need at least 7", len(raw))
	}
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}
	bits := binary.BigEndian.Uint32(raw[3:7])
	return []Field{{Name: "value", Value: float64(math.Float32frombits(bits)), Unit: ""}}, nil
}

// ResponseParser is the legacy interface for parsing device response data.
// Kept for backward compatibility with existing callers.
type ResponseParser interface {
	Parse(rawData []byte) (value float64, unit string, err error)
}

// legacyAdapter wraps a Parser to implement the legacy ResponseParser interface.
type legacyAdapter struct {
	Parser
}

// Parse implements ResponseParser by delegating to the unified Parser and
// returning only the first field value.
func (la *legacyAdapter) Parse(rawData []byte) (float64, string, error) {
	fields, err := la.Parser.Parse(rawData)
	if err != nil {
		return 0, "", err
	}
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("no fields parsed")
	}
	return fields[0].Value, fields[0].Unit, nil
}

// NewResponseParser creates a legacy ResponseParser from a unified Parser.
func NewResponseParser(p Parser) ResponseParser {
	return &legacyAdapter{Parser: p}
}

// parserRegistry is the legacy named parser registry (for backward compatibility).
var parserRegistry = map[string]ResponseParser{}

// RegisterParser adds a parser to the global legacy registry.
// Deprecated: Use parser.Register() with the unified Parser interface instead.
func RegisterParser(name string, p ResponseParser) {
	parserRegistry[name] = p
}

func init() {
	Register("modbus_uint16", &modbusUint16Parser{})
	Register("modbus_uint16_div10", &modbusUint16Div10Parser{})
	Register("modbus_int16", &modbusInt16Parser{})
	Register("modbus_uint32", &modbusUint32Parser{})
	Register("modbus_float32", &modbusFloat32Parser{})

	// Also register in the legacy registry for backward compatibility
	RegisterParser("modbus_uint16", NewResponseParser(&modbusUint16Parser{}))
	RegisterParser("modbus_uint16_div10", NewResponseParser(&modbusUint16Div10Parser{}))
	RegisterParser("modbus_int16", NewResponseParser(&modbusInt16Parser{}))
	RegisterParser("modbus_uint32", NewResponseParser(&modbusUint32Parser{}))
	RegisterParser("modbus_float32", NewResponseParser(&modbusFloat32Parser{}))
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
//
// This function is fully backward compatible with the existing implementation
// in api/template_engine.go.
func ParseModbusResponse(rawData []byte, parser string) (float64, error) {
	// P3-3: Try JSON rule parsing first if parser looks like JSON
	if strings.HasPrefix(strings.TrimSpace(parser), "{") {
		value, _, err := ParseResponseByRule(parser, rawData)
		if err != nil {
			return 0, err
		}
		return value, nil
	}

	// Try new unified registry first
	if p, err := Get(parser); err == nil {
		fields, parseErr := p.Parse(rawData)
		if parseErr != nil {
			return 0, parseErr
		}
		if len(fields) == 0 {
			return 0, fmt.Errorf("no fields parsed")
		}
		return fields[0].Value, nil
	}

	// Fallback to legacy registry for any parsers registered via RegisterParser
	if p, ok := parserRegistry[parser]; ok {
		val, _, err := p.Parse(rawData)
		return val, err
	}

	// Fallback for unknown parsers
	return 0, fmt.Errorf("unknown response_parser: %s", parser)
}
