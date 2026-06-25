package api

import (
	parser "ehome/backend/pkg/parser"
)

// ModbusCRC16 computes the standard Modbus RTU CRC16 over the given data.
// Returns the 2-byte CRC in little-endian order (low byte first).
//
// Deprecated: Use parser.ModbusCRC16 from ehome/backend/pkg/parser instead.
func ModbusCRC16(data []byte) uint16 {
	return parser.ModbusCRC16(data)
}

// OperationConfig represents a single operation definition from DeviceConfig.Operations JSONB.
//
// Deprecated: Use parser.OperationConfig from ehome/backend/pkg/parser instead.
type OperationConfig = parser.OperationConfig

// TemplateVars holds the variables available for template substitution.
//
// Deprecated: Use parser.TemplateVars from ehome/backend/pkg/parser instead.
type TemplateVars = parser.TemplateVars

// RenderCommandTemplate renders a command template into raw bytes.
//
// Deprecated: Use parser.RenderCommandTemplate from ehome/backend/pkg/parser instead.
func RenderCommandTemplate(template string, vars TemplateVars) ([]byte, error) {
	return parser.RenderCommandTemplate(template, vars)
}

// ResponseParser is the interface for parsing device response data.
//
// Deprecated: Use parser.Parser from ehome/backend/pkg/parser instead.
type ResponseParser = parser.ResponseParser

// RegisterParser adds a parser to the global registry.
//
// Deprecated: Use parser.Register() with the unified Parser interface instead.
func RegisterParser(name string, p ResponseParser) {
	parser.RegisterParser(name, p)
}

// ParseResponseByRule parses a response using a JSON-defined ParserRule.
//
// Deprecated: Use parser.ParseResponseByRule from ehome/backend/pkg/parser instead.
func ParseResponseByRule(ruleJSON string, rawData []byte) (float64, string, error) {
	return parser.ParseResponseByRule(ruleJSON, rawData)
}

// ParserRule defines a JSON-driven response parsing rule.
//
// Deprecated: Use parser.ParserRule from ehome/backend/pkg/parser instead.
type ParserRule = parser.ParserRule

// ParseModbusResponse parses a Modbus response according to the response_parser strategy.
//
// Deprecated: Use parser.ParseModbusResponse from ehome/backend/pkg/parser instead.
func ParseModbusResponse(rawData []byte, parserName string) (float64, error) {
	return parser.ParseModbusResponse(rawData, parserName)
}

// modbusExceptionMessage returns a human-readable description for Modbus exception codes.
//
// Deprecated: Use parser.ModbusExceptionMessage from ehome/backend/pkg/parser instead.
func modbusExceptionMessage(code byte) string {
	return parser.ModbusExceptionMessage(code)
}

// toUint64 converts an interface{} to uint64.
//
// Deprecated: Use parser.ToUint64 from ehome/backend/pkg/parser instead.
func toUint64(v interface{}) (uint64, error) {
	return parser.ToUint64(v)
}

// ToUint64 converts an interface{} to uint64 (exported wrapper for cross-package use).
var ToUint64 = parser.ToUint64
