package deviceaction

import (
	"encoding/json"
	"fmt"
	"testing"

	"ehome/backend/internal/drivers"
)

type testActionDriver struct{}

func (testActionDriver) DeviceType() string                             { return "test-action" }
func (testActionDriver) DeviceName() string                             { return "test" }
func (testActionDriver) OEM() string                                    { return "test" }
func (testActionDriver) Category() string                               { return "test" }
func (testActionDriver) HardwareTypes() []string                        { return []string{"uart"} }
func (testActionDriver) GetSensorDefinitions() []drivers.SensorData     { return nil }
func (testActionDriver) ParseData([]byte) ([]drivers.SensorData, error) { return nil, nil }
func (testActionDriver) ControlActions() []drivers.ControlAction {
	return []drivers.ControlAction{{ID: "read", Version: 1, Name: "read", Description: "test", Semantics: "read", Risk: "low", TXData: []byte{1}, RXTimeoutMS: 1}}
}

type verifiedSetDriver struct{ testActionDriver }

func (verifiedSetDriver) DeviceType() string { return "verified-set" }
func (verifiedSetDriver) ControlActions() []drivers.ControlAction {
	return []drivers.ControlAction{{ID: "set_mode", Version: 1, Name: "set", Description: "test", Semantics: "set", Risk: "medium", Enabled: true, TXData: []byte{0xaa}, ReadSize: 1, RXTimeoutMS: 1}}
}
func (verifiedSetDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]drivers.SensorData, error) {
	if actionID != "set_mode" || string(params) != "{}" || len(raw) != 1 || raw[0] != 0x06 {
		return nil, fmt.Errorf("unexpected verifier input")
	}
	return []drivers.SensorData{{Name: "mode_ack", Value: 1, Unit: "ack"}}, nil
}

func TestNewBuiltInRegistryUsesInjectedDriverRegistry(t *testing.T) {
	driverRegistry := drivers.NewRegistry()
	driverRegistry.Register(testActionDriver{})
	registry := NewBuiltInRegistry(driverRegistry)
	if _, ok := registry.Get("test-action", "read"); !ok {
		t.Fatal("injected driver action is missing")
	}
	if items := registry.List("prs3001"); len(items) != 0 {
		t.Fatalf("unregistered driver leaked into catalog: %+v", items)
	}
}

func TestBuiltInReadActionStartsDisabledUntilHardwareGate(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	definition, ok := registry.Get("prs3001", "read_rainfall")
	if !ok {
		t.Fatal("built-in read action is missing")
	}
	if definition.Enabled {
		t.Fatal("unverified built-in read action must not start enabled")
	}
	if err := registry.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	definition, _ = registry.Get("prs3001", "read_rainfall")
	if !definition.Enabled {
		t.Fatal("explicit rollout enable did not take effect")
	}
}

func TestTechfineReadActionsExcludeUnverifiedWrites(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	definitions := registry.List("techfine_inverter")
	if len(definitions) != 11 {
		t.Fatalf("got %d Techfine read actions, want 11: %+v", len(definitions), definitions)
	}
	for _, definition := range definitions {
		if definition.Semantics != "read" || definition.Enabled {
			t.Fatalf("Techfine action must be a disabled read: %+v", definition)
		}
		if definition.ID == "turn_on" || definition.ID == "set_grid_range" {
			t.Fatalf("unverified Techfine write leaked into the catalog: %+v", definition)
		}
	}
}

func TestSetActionRequiresTrustedVerifier(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Definition{ID: "unsafe_set", Version: 1, Name: "unsafe", DeviceType: "test", Semantics: "set", Risk: "medium", Enabled: false, Transport: ChannelCmdV2Adapter, SingleStep: SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}})
	if err == nil {
		t.Fatal("set action without a verifier was registered")
	}
}

func TestJiabaidaReadActionsExcludeBMSWrites(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	definitions := registry.List("jiabaida_bms")
	if len(definitions) < 8 {
		t.Fatalf("got %d Jiabaida actions, want read actions plus guarded capabilities: %+v", len(definitions), definitions)
	}
	var mos, restart bool
	for _, definition := range definitions {
		if definition.ID == "set_mos_policy" {
			mos = true
			if definition.Enabled || definition.ExecutionShape != "bounded_sequence" || !definition.AtMostOnce || definition.Verification != "readback" {
				t.Fatalf("MOS policy must be guarded bounded action: %+v", definition)
			}
		}
		if definition.ID == "bms_restart" {
			restart = true
			if definition.Enabled || definition.Risk != "critical" || definition.Verification != "observation" {
				t.Fatalf("BMS restart must be guarded critical action: %+v", definition)
			}
		}
		if definition.ID != "set_mos_policy" && definition.ID != "bms_restart" && definition.Semantics != "read" {
			t.Fatalf("unexpected Jiabaida action: %+v", definition)
		}
		if definition.ID == "close_discharge_mos" || definition.ID == "close_charge_mos" || definition.ID == "release_mos" {
			t.Fatalf("unverified BMS write leaked into the catalog: %+v", definition)
		}
	}
	if !mos || !restart {
		t.Fatal("guarded BMS capabilities are missing")
	}
}

func TestBoundedPlanDefinitionIsNeverFlattenedIntoSingleStep(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	definition, ok := registry.Get("sn3001_rain", "reset_rainfall")
	if !ok || definition.ExecutionShape != "bounded_sequence" || !definition.AtMostOnce || definition.Verification != "readback" {
		t.Fatalf("rain reset metadata missing: %+v", definition)
	}
	if _, err := definition.Compile(json.RawMessage(`{}`)); err == nil {
		t.Fatal("unavailable bounded reset must not compile as a single physical step")
	}
	if _, err := definition.CompilePlan(json.RawMessage(`{}`)); err == nil {
		t.Fatal("hardware-gated bounded reset must not compile for the current node")
	}
}

func TestBuiltInRainResetPlanCompilerIsRegistered(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	definition, ok := registry.Get("sn3001_rain", "reset_rainfall")
	if !ok {
		t.Fatal("SN-3001 reset action is missing")
	}
	if definition.AvailabilityCode != "hardware_evidence_required" || definition.MaxSteps != 3 {
		t.Fatalf("unexpected reset gate: %+v", definition)
	}
}

func TestBuiltInSetActionUsesTrustedVerifier(t *testing.T) {
	driverRegistry := drivers.NewRegistry()
	driverRegistry.Register(verifiedSetDriver{})
	registry := NewBuiltInRegistry(driverRegistry)
	definition, ok := registry.Get("verified-set", "set_mode")
	if !ok {
		t.Fatal("verified set action is missing")
	}
	if definition.Enabled {
		t.Fatal("current single-step/RAM engine must force setters disabled")
	}
	if err := registry.SetEnabled("verified-set", "set_mode", true); err == nil {
		t.Fatal("current engine accepted setter rollout")
	}
	result, err := definition.Verify(json.RawMessage(`{}`), []byte{0x06})
	if err != nil || len(result) != 1 || result[0].Name != "mode_ack" {
		t.Fatalf("trusted verifier result=%+v err=%v", result, err)
	}
}
