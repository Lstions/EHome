package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
)

// =====================================================================
// F10: sender.go 拆分 — manifest_codec.go 纯函数单测
// These functions were extracted verbatim from sender.go; the tests pin
// their standalone behavior so future edits to the send path cannot
// silently change manifest encoding/validation semantics.
// =====================================================================

func TestNormalizedManifestBusType(t *testing.T) {
	tests := []struct {
		value      string
		wantName   string
		wantKnown  bool
		wantPeriph bool
	}{
		{"UART", "UART", true, false},
		{"uart", "UART", true, false},
		{" 1 ", "UART", true, false},
		{"I2C", "I2C", true, false},
		{"i2c", "I2C", true, false},
		{"2", "I2C", true, false},
		{"SPI", "SPI", true, false},
		{"3", "SPI", true, false},
		{"GPIO", "GPIO", true, true},
		{"4", "GPIO", true, true},
		{"ADC", "ADC", true, false},
		{"5", "ADC", true, false},
		{"PWM", "PWM", true, true},
		{"6", "PWM", true, true},
		{"UNKNOWN", "", false, false},
		{"", "", false, false},
	}
	for _, tc := range tests {
		name, known, periph := normalizedManifestBusType(tc.value)
		if name != tc.wantName || known != tc.wantKnown || periph != tc.wantPeriph {
			t.Errorf("normalizedManifestBusType(%q) = (%q, %v, %v); want (%q, %v, %v)",
				tc.value, name, known, periph, tc.wantName, tc.wantKnown, tc.wantPeriph)
		}
	}
}

func TestDecodeManifestTransportPins(t *testing.T) {
	t.Run("UART two pins", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "UART", BusConfig: "10110000258000"}
		pins, err := decodeManifestTransportPins(ch, "UART")
		if err != nil {
			t.Fatal(err)
		}
		if len(pins) != 2 || pins[0] != 0x10 || pins[1] != 0x11 {
			t.Fatalf("pins = %v, want [16 17]", pins)
		}
	})

	t.Run("I2C two pins", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "I2C", BusConfig: "0207"}
		pins, err := decodeManifestTransportPins(ch, "I2C")
		if err != nil {
			t.Fatal(err)
		}
		if len(pins) != 2 || pins[0] != 0x02 || pins[1] != 0x07 {
			t.Fatalf("pins = %v, want [2 7]", pins)
		}
	})

	t.Run("SPI nine bytes CS+CLK+MISO+MOSI", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "SPI", BusConfig: "050000000000020304"}
		pins, err := decodeManifestTransportPins(ch, "SPI")
		if err != nil {
			t.Fatal(err)
		}
		// data[0]=5 (CS), data[6]=2, data[7]=3, data[8]=4
		if len(pins) != 4 || pins[0] != 5 || pins[1] != 2 || pins[2] != 3 || pins[3] != 4 {
			t.Fatalf("pins = %v, want [5 2 3 4]", pins)
		}
	})

	t.Run("ADC no pins", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "ADC", BusConfig: ""}
		pins, err := decodeManifestTransportPins(ch, "ADC")
		if err != nil || pins != nil {
			t.Fatalf("pins = %v err = %v, want nil/nil", pins, err)
		}
	})

	t.Run("malformed hex rejected", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "I2C", BusConfig: "zz"}
		if _, err := decodeManifestTransportPins(ch, "I2C"); err == nil {
			t.Fatal("expected malformed bus_config error")
		}
	})

	t.Run("too short rejected", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "I2C", BusConfig: "02"}
		if _, err := decodeManifestTransportPins(ch, "I2C"); err == nil {
			t.Fatal("expected short bus_config error")
		}
	})

	t.Run("SPI wrong length rejected", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "SPI", BusConfig: "050000"}
		if _, err := decodeManifestTransportPins(ch, "SPI"); err == nil {
			t.Fatal("expected SPI length error")
		}
	})

	t.Run("unsupported bus type", func(t *testing.T) {
		ch := models.Channel{ID: 1, BusType: "CAN"}
		if _, err := decodeManifestTransportPins(ch, "CAN"); err == nil {
			t.Fatal("expected unsupported bus type error")
		}
	})

	t.Run("backslash-x prefixed hex trimmed", func(t *testing.T) {
		// \x-prefixed hex (PostgreSQL bytea text form) must be trimmed before decode.
		ch := models.Channel{ID: 1, BusType: "I2C", BusConfig: `\x0207`}
		pins, err := decodeManifestTransportPins(ch, "I2C")
		if err != nil {
			t.Fatal(err)
		}
		if len(pins) != 2 || pins[0] != 2 || pins[1] != 7 {
			t.Fatalf("pins = %v, want [2 7]", pins)
		}
	})
}

func TestValidateManifestTemplateCapacity(t *testing.T) {
	templates := make([]models.ConfigTemplate, 16)
	if err := validateManifestTemplateCapacity(templates, 16); err != nil {
		t.Fatalf("exactly at limit should pass: %v", err)
	}
	if err := validateManifestTemplateCapacity(append(templates, models.ConfigTemplate{}), 16); err == nil {
		t.Fatal("over limit should fail")
	}
}

func TestFindTemplateIDForCommand(t *testing.T) {
	templates := []models.ConfigTemplate{
		{ID: 3, WriteData: "010300000001840A"},
		{ID: 7, WriteData: "010300000000"},
	}
	tests := []struct {
		writeData string
		want      uint64
	}{
		{"010300000001840A", 3},
		{" 010300000001840a ", 3}, // case/space normalization
		{"010300000000", 7},
		{"AABBCC", 0}, // unknown command → 0, caller must skip
		{"", 0},
	}
	for _, tc := range tests {
		if got := findTemplateIDForCommand(templates, tc.writeData); got != tc.want {
			t.Errorf("findTemplateIDForCommand(%q) = %d, want %d", tc.writeData, got, tc.want)
		}
	}
}

func TestFindTemplateID(t *testing.T) {
	ch := models.Channel{TemplateIDs: "3,9"}
	edge := models.EdgeDevice{}
	if got := findTemplateID(ch, edge); got != 3 {
		t.Fatalf("first parseable id = %d, want 3", got)
	}
	if got := findTemplateID(models.Channel{}, edge); got != 0 {
		t.Fatalf("empty template_ids = %d, want 0 (F3 no fallback)", got)
	}
	if got := findTemplateID(models.Channel{TemplateIDs: "abc,,"}, edge); got != 0 {
		t.Fatalf("unparseable template_ids = %d, want 0", got)
	}
}

func TestReconcileDriverTemplatesNeedsRegistry(t *testing.T) {
	// reconcileDriverTemplates requires a real CommandTemplateProvider driver;
	// the auto-create path is exercised end-to-end by
	// TestManifestReconcileFailureLeavesNoOrphanTemplates and the F2 snapshot
	// tests via SendConfigManifestWithDecision. Here we only pin the nil
	// registry no-op contract.
	if created, err := reconcileDriverTemplates(nil, nil, "n1", nil, 16); err != nil || created {
		t.Fatalf("nil registry should no-op, got created=%v err=%v", created, err)
	}
}
