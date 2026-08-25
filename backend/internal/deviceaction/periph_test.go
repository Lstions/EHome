package deviceaction

import (
	"encoding/json"
	"testing"
)

// TestPeriphActionsRegistered verifies both built-in periph_cmd actions are
// present in the built-in registry with the expected declarative metadata.
func TestPeriphActionsRegistered(t *testing.T) {
	r := NewBuiltInRegistry(nil)
	for _, tc := range []struct {
		deviceType string
		actionID   string
	}{
		{DeviceTypeGPIO, ActionGPIOSet},
		{DeviceTypePWM, ActionPWMSetDuty},
	} {
		def, ok := r.Get(tc.deviceType, tc.actionID)
		if !ok {
			t.Fatalf("action %s/%s not registered", tc.deviceType, tc.actionID)
		}
		if def.Transport != PeriphCmdAdapter {
			t.Errorf("%s: transport=%q, want %q", tc.actionID, def.Transport, PeriphCmdAdapter)
		}
		if def.Semantics != "set" || def.Risk != "medium" || def.ExecutionShape != "single" || def.Verification != "observation" {
			t.Errorf("%s: unexpected metadata semantics=%q risk=%q shape=%q verification=%q",
				tc.actionID, def.Semantics, def.Risk, def.ExecutionShape, def.Verification)
		}
		if !def.Enabled {
			t.Errorf("%s: built-in periph action must default to enabled", tc.actionID)
		}
		if !CurrentEngineAllows(def) {
			t.Errorf("%s: single+set+observation periph action must pass the engine gate", tc.actionID)
		}
	}
}

// TestPeriphActionRegistrationRejectsInvalidTransport verifies the Register
// transport whitelist still rejects unknown adapters while accepting both
// supported ones.
func TestPeriphActionRegistrationRejectsInvalidTransport(t *testing.T) {
	r := NewRegistry()
	def, ok := periphDefinition(ActionGPIOSet)
	if !ok {
		t.Fatal("gpio_set definition missing")
	}
	def.Transport = "raw_mqtt"
	if err := r.Register(def); err == nil {
		t.Fatal("register with unknown transport must fail")
	}
	def.Transport = PeriphCmdAdapter
	if err := r.Register(def); err != nil {
		t.Fatalf("register with periph_cmd transport failed: %v", err)
	}
}

// TestRegisterPeriphActionsIdempotentGuard verifies double registration fails
// closed instead of silently overwriting.
func TestRegisterPeriphActionsIdempotentGuard(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPeriphActions(r); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if err := RegisterPeriphActions(r); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}

// TestGPIOSetParamsCanonicalize exercises the schema: valid params pass,
// out-of-range/unknown/missing params fail closed.
func TestGPIOSetParamsCanonicalize(t *testing.T) {
	def, ok := periphDefinition(ActionGPIOSet)
	if !ok {
		t.Fatal("gpio_set definition missing")
	}
	valid, err := CanonicalizeParams(def.InputSchema, json.RawMessage(`{"pin":5,"level":1}`))
	if err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	if string(valid) != `{"level":1,"pin":5}` {
		t.Fatalf("canonical form = %s", valid)
	}
	for _, raw := range []string{
		`{"pin":256,"level":1}`,     // pin out of range
		`{"pin":-1,"level":1}`,      // pin negative
		`{"pin":5,"level":2}`,       // level out of range
		`{"pin":5}`,                 // level missing
		`{"level":1}`,               // pin missing
		`{"pin":5,"level":1,"x":1}`, // unknown parameter
		`{"pin":"5","level":1}`,     // pin wrong type
	} {
		if _, err := CanonicalizeParams(def.InputSchema, json.RawMessage(raw)); err == nil {
			t.Errorf("params %s must be rejected", raw)
		}
	}
}

// TestPWMSetDutyParamsCanonicalize exercises the pwm schema bounds.
func TestPWMSetDutyParamsCanonicalize(t *testing.T) {
	def, ok := periphDefinition(ActionPWMSetDuty)
	if !ok {
		t.Fatal("pwm_set_duty definition missing")
	}
	if _, err := CanonicalizeParams(def.InputSchema, json.RawMessage(`{"hardware_id":"pwm0","duty":5000}`)); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	if _, err := CanonicalizeParams(def.InputSchema, json.RawMessage(`{"hardware_id":"pwm0","duty":0}`)); err != nil {
		t.Fatalf("duty=0 must be accepted: %v", err)
	}
	for _, raw := range []string{
		`{"hardware_id":"pwm0","duty":10001}`, // duty out of range
		`{"hardware_id":"pwm0","duty":-1}`,    // duty negative
		`{"hardware_id":"","duty":1}`,         // hardware_id empty
		`{"duty":1}`,                          // hardware_id missing
		`{"hardware_id":"pwm0"}`,              // duty missing
		`{"hardware_id":"pwm0","duty":1.5}`,   // duty not integer
	} {
		if _, err := CanonicalizeParams(def.InputSchema, json.RawMessage(raw)); err == nil {
			t.Errorf("params %s must be rejected", raw)
		}
	}
}

// TestIsPeriphAction covers the routing predicate used by commandexec.
func TestIsPeriphAction(t *testing.T) {
	if !IsPeriphAction(ActionGPIOSet) || !IsPeriphAction(ActionPWMSetDuty) {
		t.Fatal("built-in periph actions must be recognized")
	}
	if IsPeriphAction("read_rainfall") || IsPeriphAction("") {
		t.Fatal("non-periph actions must not be recognized")
	}
}
