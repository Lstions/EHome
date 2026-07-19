package deviceaction

import (
	"encoding/json"
	"fmt"
	"testing"
)

func number(value float64) *float64 { return &value }

func TestCanonicalizeParamsRestrictedSchema(t *testing.T) {
	schema := ParameterSchema{
		Properties: map[string]Parameter{
			"enabled": {Type: "boolean"},
			"level":   {Type: "integer", Minimum: number(1), Maximum: number(3)},
			"mode":    {Type: "string", Enum: []string{"eco", "normal"}},
		},
		Required: []string{"enabled", "level"},
	}
	canonical, err := CanonicalizeParams(schema, json.RawMessage(`{"mode":"eco","level":2,"enabled":true}`))
	if err != nil || string(canonical) != `{"enabled":true,"level":2,"mode":"eco"}` {
		t.Fatalf("canonical=%s err=%v", canonical, err)
	}
	for _, raw := range []string{
		`{"enabled":true,"enabled":false,"level":2}`,
		`{"enabled":true,"level":2,"unknown":1}`,
		`{"enabled":true,"level":2.5}`,
		`{"enabled":true,"level":2,"mode":{"nested":true}}`,
		`{"enabled":true}`,
	} {
		if _, err := CanonicalizeParams(schema, json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid params accepted: %s", raw)
		}
	}
}

func TestParameterSchemaRejectsUnsafeOrIncoherentDefinitions(t *testing.T) {
	for _, schema := range []ParameterSchema{
		{Properties: map[string]Parameter{"bad-name": {Type: "string"}}},
		{Properties: map[string]Parameter{"value": {Type: "object"}}},
		{Properties: map[string]Parameter{"value": {Type: "string"}}, Required: []string{"missing"}},
		{Properties: map[string]Parameter{"value": {Type: "integer", Enum: []string{"1"}}}},
	} {
		if err := schema.Validate(); err == nil {
			t.Fatalf("unsafe schema accepted: %+v", schema)
		}
	}
}

func TestRegistryRejectsParameterizedActionWithoutTrustedCompiler(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Definition{
		ID: "set_level", Version: 1, DeviceType: "test", Semantics: "read", Risk: "low", Transport: ChannelCmdV2Adapter,
		InputSchema: ParameterSchema{Properties: map[string]Parameter{"level": {Type: "integer"}}},
		SingleStep:  SingleStep{TXData: []byte{1}, RXTimeoutMS: 1},
	}); err == nil {
		t.Fatal("parameterized action without compiler was registered")
	}
}

func TestRegistryCompilesParameterizedActionOnlyThroughTrustedCompiler(t *testing.T) {
	registry := NewRegistry()
	definition := Definition{
		ID: "set_level", Version: 1, DeviceType: "test", Semantics: "read", Risk: "low", Transport: ChannelCmdV2Adapter,
		InputSchema: ParameterSchema{Properties: map[string]Parameter{"level": {Type: "integer"}}, Required: []string{"level"}},
		compiler: func(params json.RawMessage) (SingleStep, error) {
			if string(params) != `{"level":2}` {
				return SingleStep{}, fmt.Errorf("unexpected params %s", params)
			}
			return SingleStep{TXData: []byte{1, 6, 0, 1, 0, 2}, RXTimeoutMS: 1000}, nil
		},
	}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	compiled, err := registry.byType["test"]["set_level"].Compile(json.RawMessage(`{"level":2}`))
	if err != nil || len(compiled.TXData) != 6 || compiled.TXData[5] != 2 {
		t.Fatalf("compiled=%+v err=%v", compiled, err)
	}
}
