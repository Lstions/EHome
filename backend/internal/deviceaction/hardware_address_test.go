package deviceaction

import (
	"sort"
	"strings"
	"testing"
)

// TestParseHardwareAddressSemantics pins the ONE口径 for "what is a legal
// EdgeDevice address". This is the truth source the frontend contract parity
// gate (frontend-shared/src/utils/__tests__/DeviceAddressContractParity.spec.ts)
// and the create/update gate in handler_edge_device.go both defer to.
//
// Why it exists: the 2026-09-20 production incident was caused by the create
// wizard copying Channel.hardware_id ("UART1", a BUS NAME) into
// EdgeDevice.hardware_id (a DEVICE ADDRESS). Every dispatch was then rejected
// here and the operation silently sat in QUEUED for ~120s before failing.
//
// The classifier self-check requirement (本仓禁止"永远绿灯"的门禁) is met by
// driving legal and illegal samples through the SAME function: a regex typo
// cannot make the whole table pass, and both outcomes are asserted.
func TestParseHardwareAddressSemantics(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantOK  bool
		want    uint8
		comment string
	}{
		{"empty_is_legacy_default", "", true, 1, "empty means legacy default address 1"},
		{"zero_is_legacy_default", "0", true, 1, "explicit 0 means default 1"},
		{"whitespace_only_is_default", "   ", true, 1, "trims to empty => default 1"},
		{"single_one", "1", true, 1, ""},
		{"upper_bound", "254", true, 254, "0xFE is the highest legal unit id"},
		{"above_upper_bound", "255", false, 0, "255 is reserved / out of range"},
		{"hex_lower_bound", "0x01", true, 1, ""},
		{"hex_upper_bound", "0xFE", true, 254, ""},
		{"hex_above_upper_bound", "0xFF", false, 0, ""},
		{"hex_zero", "0x00", false, 0, "parsed 0 is below the legal minimum"},
		{"hex_short_form", "0x1", true, 1, "one hex digit is accepted"},
		{"hex_upper_prefix", "0Xfe", true, 254, "0X prefix is accepted"},
		{"hex_empty_digits", "0x", false, 0, ""},
		{"bus_name_uart1", "UART1", false, 0, "THE 2026-09-20 production value: a bus name, not an address"},
		{"bus_name_i2c0", "I2C0", false, 0, "I2C bus names are not addresses either"},
		{"leading_zeros", "007", true, 7, "decimal parse drops leading zeros"},
		{"all_zero_digits", "00", false, 0, "parses to 0, below the minimum"},
		{"padded_by_spaces", " 1 ", true, 1, "value is trimmed before parsing"},
		{"trailing_space", "1 ", true, 1, "value is trimmed before parsing"},
		{"alphabetical", "abc", false, 0, ""},
		{"negative", "-1", false, 0, "a sign prefix is not permitted"},
		{"plus_signed", "+1", false, 0, "a sign prefix is not permitted"},
		{"above_upper_bound_decimal", "256", false, 0, ""},
		{"float", "1.5", false, 0, ""},
		{"scientific", "1e2", false, 0, ""},
		{"binary_prefix", "0b1", false, 0, "only decimal and 0x forms are accepted"},
		{"digit_separator", "1_0", false, 0, "underscores are only legal with ParseUint base 0"},
		{"full_width_digits", "１２", false, 0, ""},
	}
	legal, illegal := 0, 0
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHardwareAddress(tc.value)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("ParseHardwareAddress(%q) = error %v, want address %d (%s)", tc.value, err, tc.want, tc.comment)
				}
				if got != tc.want {
					t.Fatalf("ParseHardwareAddress(%q) = %d, want %d", tc.value, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseHardwareAddress(%q) = %d, want error (%s)", tc.value, got, tc.comment)
			}
			if got != 0 {
				t.Fatalf("ParseHardwareAddress(%q) returned address %d alongside error %v", tc.value, got, err)
			}
			// The error text is surfaced verbatim by the HTTP create/update gate,
			// so it must stay human-readable and carry the legal domain.
			if !strings.Contains(err.Error(), "1 to 254") {
				t.Fatalf("error %q must carry the legal range so the API can surface it", err.Error())
			}
		})
	}
	for _, tc := range tests {
		if tc.wantOK {
			legal++
		} else {
			illegal++
		}
	}
	// Classifier self-check floor: a table that only fed legal (or only illegal)
	// samples would pass while proving nothing.
	if legal < 8 || illegal < 8 {
		t.Fatalf("table is one-sided (legal=%d illegal=%d); this gate would be vacuous", legal, illegal)
	}
}

// TestRequiresTargetAddressTruthSource pins WHICH drivers actually need an
// address on the wire. The create/update gate (handler_edge_device.go) and the
// wizard's mandatory "device address" field both key off this predicate, and it
// holds only where dispatch would call ParseHardwareAddress.
func TestRequiresTargetAddressTruthSource(t *testing.T) {
	registry := NewBuiltInRegistry(nil)

	addressedByType := map[string][]string{}
	totalActions := 0
	for _, deviceType := range registry.DeviceTypes() {
		for _, def := range registry.List(deviceType) {
			totalActions++
			if def.RequiresTargetAddress() {
				addressedByType[deviceType] = append(addressedByType[deviceType], def.ID)
			}
		}
	}

	// Sanity floor: a zero-action scan would make every assertion below vacuous.
	if totalActions < 10 {
		t.Fatalf("action catalog only exposed %d actions; the scan is broken", totalActions)
	}
	if len(addressedByType) == 0 {
		t.Fatal("no action requires a target address; the scan is broken")
	}
	types := make([]string, 0, len(addressedByType))
	for deviceType := range addressedByType {
		types = append(types, deviceType)
	}
	sort.Strings(types)
	if len(types) != 1 || types[0] != "sn3001_rain" {
		t.Fatalf("address-requiring driver types = %v, want exactly [sn3001_rain]; "+
			"when this changes, re-review the create/update gate and the frontend parity gate", types)
	}

	for _, actionID := range []string{
		"read_rainfall", "read_device_address", "read_baud_rate", "read_rain_sensitivity",
		"clear_rainfall_write", "reset_rainfall", "set_baud_rate", "set_device_address", "set_rain_sensitivity",
	} {
		def, ok := registry.Get("sn3001_rain", actionID)
		if !ok {
			t.Fatalf("sn3001_rain/%s is missing from the catalog", actionID)
		}
		if !def.RequiresTargetAddress() {
			t.Fatalf("sn3001_rain/%s must require a target address (its frames embed the Modbus unit id)", actionID)
		}
	}

	// Non-addressed drivers must NOT demand an address, otherwise the gate would
	// block legitimate address-less devices (pure I2C/SPI collectors, BMS, ...).
	for _, deviceType := range []string{"bmp280", "jiabaida_bms", "techfine_inverter", "generic_modbus"} {
		for _, def := range registry.List(deviceType) {
			if def.RequiresTargetAddress() {
				t.Fatalf("%s/%s must not require a target address", deviceType, def.ID)
			}
		}
	}
}
