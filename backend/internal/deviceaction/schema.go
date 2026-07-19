package deviceaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
)

const maxParameterJSONBytes = 16 * 1024

var parameterName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ParameterSchema is intentionally not general JSON Schema. It is the small,
// reviewable subset needed by device actions: an object containing declared
// scalar fields. There are no references, objects, arrays, expressions or
// dynamically evaluated validation rules.
type ParameterSchema struct {
	Properties map[string]Parameter `json:"properties"`
	Required   []string             `json:"required,omitempty"`
}

type Parameter struct {
	Type      string   `json:"type"` // string, boolean, integer, number
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	MinLength *uint32  `json:"min_length,omitempty"`
	MaxLength *uint32  `json:"max_length,omitempty"`
	Enum      []string `json:"enum,omitempty"` // only meaningful for string
}

func (s ParameterSchema) Validate() error {
	for name, parameter := range s.Properties {
		if !parameterName.MatchString(name) {
			return fmt.Errorf("invalid parameter name %q", name)
		}
		switch parameter.Type {
		case "string", "boolean", "integer", "number":
		default:
			return fmt.Errorf("unsupported parameter type for %q", name)
		}
		if parameter.Minimum != nil && (!finite(*parameter.Minimum) || parameter.Maximum != nil && *parameter.Minimum > *parameter.Maximum) {
			return fmt.Errorf("invalid numeric range for %q", name)
		}
		if parameter.Maximum != nil && !finite(*parameter.Maximum) {
			return fmt.Errorf("invalid numeric range for %q", name)
		}
		if parameter.MinLength != nil && parameter.MaxLength != nil && *parameter.MinLength > *parameter.MaxLength {
			return fmt.Errorf("invalid string length range for %q", name)
		}
		if len(parameter.Enum) > 0 && parameter.Type != "string" {
			return fmt.Errorf("enum is only supported for string parameter %q", name)
		}
	}
	seen := make(map[string]struct{}, len(s.Required))
	for _, name := range s.Required {
		if _, duplicate := seen[name]; duplicate || s.Properties[name].Type == "" {
			return fmt.Errorf("invalid required parameter %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// CanonicalizeParams rejects duplicate object keys before JSON unmarshalling,
// validates against the restricted schema, then uses json.Marshal's stable key
// ordering as the canonical request-hash input.
func CanonicalizeParams(schema ParameterSchema, raw json.RawMessage) ([]byte, error) {
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > maxParameterJSONBytes {
		return nil, fmt.Errorf("parameter payload exceeds %d bytes", maxParameterJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("parameters must be an object")
	}
	params := make(map[string]interface{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode parameter name: %w", err)
		}
		name, ok := nameToken.(string)
		if !ok || !parameterName.MatchString(name) {
			return nil, fmt.Errorf("invalid parameter name")
		}
		if _, exists := params[name]; exists {
			return nil, fmt.Errorf("duplicate parameter %q", name)
		}
		parameter, exists := schema.Properties[name]
		if !exists {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
		var value interface{}
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode parameter %q: %w", name, err)
		}
		if err := validateParameter(name, parameter, value); err != nil {
			return nil, err
		}
		params[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("close parameter object: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	for _, name := range schema.Required {
		if _, exists := params[name]; !exists {
			return nil, fmt.Errorf("missing required parameter %q", name)
		}
	}
	return json.Marshal(params)
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing parameter data")
	}
	return nil
}

func validateParameter(name string, parameter Parameter, value interface{}) error {
	switch parameter.Type {
	case "string":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("parameter %q must be a string", name)
		}
		length := uint32(len([]rune(stringValue)))
		if parameter.MinLength != nil && length < *parameter.MinLength || parameter.MaxLength != nil && length > *parameter.MaxLength {
			return fmt.Errorf("parameter %q string length is out of range", name)
		}
		if len(parameter.Enum) > 0 {
			found := false
			for _, allowed := range parameter.Enum {
				if stringValue == allowed {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("parameter %q is not an allowed value", name)
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter %q must be a boolean", name)
		}
	case "integer", "number":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("parameter %q must be numeric", name)
		}
		if parameter.Type == "integer" {
			if _, err := number.Int64(); err != nil {
				return fmt.Errorf("parameter %q must be an integer", name)
			}
		}
		floatValue, err := number.Float64()
		if err != nil || !finite(floatValue) || parameter.Minimum != nil && floatValue < *parameter.Minimum || parameter.Maximum != nil && floatValue > *parameter.Maximum {
			return fmt.Errorf("parameter %q is out of range", name)
		}
	}
	return nil
}
