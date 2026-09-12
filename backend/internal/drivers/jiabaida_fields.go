package drivers

import (
	"fmt"
	"sort"
)

// ============================================================================
// F2/F3 parameter-block field tables — single source of truth
// ============================================================================
// Every F2/F3 field is declared exactly once in jiabaidaF2Fields() /
// jiabaidaF3Fields(); the block compilers, read projections (parse0xF2/F3),
// writable schemas (jiabaidaF2Parameters/F3Parameters) and readback
// reconciliation spans (jiabaidaF2FieldSpans/F3FieldSpans) all derive from
// the same table, so a field can no longer drift between the three copies.

// jiabaidaField describes one declared byte range of an F2/F3 parameter
// block.  Width must be 1 (uint8) or 2 (uint16 big-endian).  Scale is the
// parse output multiplier; for Scale<1 the parse divides by the exact
// reciprocal (1/Scale is exactly 10/100 in float64), which is bit-identical
// to the legacy explicit /10 and /100 divisions.  Temp fields override Scale
// with the 0.1K → °C conversion.  Parse=false fields are writable but not
// projected by the read parsers (F2 delay/protection counters).
type jiabaidaField struct {
	Name   string
	Offset int
	Width  int
	Scale  float64
	Unit   string
	Min    float64
	Max    float64
	Temp   bool
	Parse  bool
}

// jiabaidaF2Fields declares all 33 writable fields of the 53-byte F2 block
// (51 field bytes 0..50 + CRC at 51-52).  Order is the schema order (== byte
// order) so compile, parse and schema outputs match the legacy hand-written
// versions exactly.  chg_oc_protect keeps its protocol cap 32676; every
// other u16 is 0..65535, every u8 0..255.
func jiabaidaF2Fields() []jiabaidaField {
	return []jiabaidaField{
		{Name: "cell_ov_protect", Offset: 0, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_ov_release", Offset: 2, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_uv_protect", Offset: 4, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_uv_release", Offset: 6, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_ov_protect", Offset: 8, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_ov_release", Offset: 10, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_uv_protect", Offset: 12, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_uv_release", Offset: 14, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_ov_delay", Offset: 16, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "cell_uv_delay", Offset: 17, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "pack_ov_delay", Offset: 18, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "pack_uv_delay", Offset: 19, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "chg_ot_protect", Offset: 20, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ot_release", Offset: 22, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ut_protect", Offset: 24, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ut_release", Offset: 26, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ot_protect", Offset: 28, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ot_release", Offset: 30, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ut_protect", Offset: 32, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ut_release", Offset: 34, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ot_delay", Offset: 36, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_ut_delay", Offset: 37, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_ot_delay", Offset: 38, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_ut_delay", Offset: 39, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_oc_protect", Offset: 40, Width: 2, Scale: 0.01, Unit: "A", Min: 0, Max: 32676, Parse: true},
		{Name: "chg_oc_delay", Offset: 42, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_oc_release_delay", Offset: 43, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_oc_protect", Offset: 44, Width: 2, Scale: 0.01, Unit: "A", Min: 0, Max: 65535, Parse: true},
		{Name: "dis_oc_delay", Offset: 46, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_oc_release_delay", Offset: 47, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "short_circuit_protect", Offset: 48, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "hardware_oc_protect", Offset: 49, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "short_circuit_release", Offset: 50, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
	}
}

// jiabaidaF3Fields declares all 15 writable fields of the 52-byte F3 block
// (50 field bytes + CRC at 50-51).  Reserved byte ranges (16-19, 32-47) are
// NOT declared and therefore never compiled, parsed or reconciled.
// cell_count_config keeps its protocol range 1..32.
func jiabaidaF3Fields() []jiabaidaField {
	return []jiabaidaField{
		{Name: "function_config", Offset: 0, Width: 2, Scale: 1, Unit: "bitmask", Min: 0, Max: 65535, Parse: true},
		{Name: "ntc_config", Offset: 2, Width: 2, Scale: 1, Unit: "bitmask", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_count_config", Offset: 4, Width: 2, Scale: 1, Unit: "串", Min: 1, Max: 32, Parse: true},
		{Name: "shunt_resistance", Offset: 6, Width: 2, Scale: 0.1, Unit: "mΩ", Min: 0, Max: 65535, Parse: true},
		{Name: "balance_start_voltage", Offset: 8, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "balance_diff", Offset: 10, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "gps_shutdown_voltage", Offset: 12, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "gps_shutdown_delay", Offset: 14, Width: 2, Scale: 1, Unit: "S", Min: 0, Max: 65535, Parse: true},
		{Name: "nominal_capacity_cfg", Offset: 20, Width: 2, Scale: 0.01, Unit: "Ah", Min: 0, Max: 65535, Parse: true},
		{Name: "cycle_capacity_cfg", Offset: 22, Width: 2, Scale: 0.01, Unit: "Ah", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_full_voltage", Offset: 24, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_empty_voltage", Offset: 26, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "self_discharge_rate", Offset: 28, Width: 2, Scale: 0.1, Unit: "%", Min: 0, Max: 65535, Parse: true},
		{Name: "soc100_voltage", Offset: 30, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "soc0_voltage", Offset: 48, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
	}
}

// jiabaidaResistanceParameters declares the 30 per-cell internal resistance
// values as scalar integer parameters.  The deviceaction catalog schema is
// deliberately scalar-only (string/boolean/integer/number), so a fixed-30
// F6 resistance block is expressed as resistance_1..resistance_30 rather
// than an array.  Values are signed 0.1mΩ (协议 §八 F6, int16 范围).
func jiabaidaResistanceParameters() []ControlParameter {
	params := make([]ControlParameter, 0, 30)
	for i := 1; i <= 30; i++ {
		params = append(params, ControlParameter{
			Name:     fmt.Sprintf("resistance_%d", i),
			Type:     "integer",
			Required: true,
			Minimum:  floatPtr(-32768),
			Maximum:  floatPtr(32767),
		})
	}
	return params
}

// jiabaidaFieldParameters renders the writable ControlParameter schema from
// the field table: every field is an integer parameter required=true with the
// table's Min/Max (u8 → 0..255, u16 → 0..65535 except the declared
// overrides cell_count_config 1..32 and chg_oc_protect 0..32676).
func jiabaidaFieldParameters(fields []jiabaidaField) []ControlParameter {
	params := make([]ControlParameter, 0, len(fields))
	for _, f := range fields {
		params = append(params, ControlParameter{
			Name: f.Name, Type: "integer", Required: true,
			Minimum: floatPtr(f.Min), Maximum: floatPtr(f.Max),
		})
	}
	return params
}

// jiabaidaF2Parameters declares the writable protection-parameter fields of
// the 53-byte F2 block.  Names match parse0xF2 so the readback verifier can
// reconcile write→read 1:1.
func jiabaidaF2Parameters() []ControlParameter {
	return jiabaidaFieldParameters(jiabaidaF2Fields())
}

// jiabaidaF3Parameters declares the writable system-parameter fields of the
// 52-byte F3 block.  Reserved byte ranges (16-19, 32-47) are zero-filled by
// the compiler; names match parse0xF3.
func jiabaidaF3Parameters() []ControlParameter {
	return jiabaidaFieldParameters(jiabaidaF3Fields())
}

// jiabaidaF2FieldSpans returns the inclusive [start,end] byte ranges of the
// F2 block that carry DECLARED protocol fields, derived from the field table
// (0..50 continuous — no reserved area).  The CRC-16 bytes (51-52) are
// deliberately excluded: a real BMS may recompute the CRC on readback, and
// the CRC protects the field bytes themselves — if every declared field
// matches, the CRC must be correct.
func jiabaidaF2FieldSpans() [][2]int {
	return jiabaidaFieldSpans(jiabaidaF2Fields())
}

// jiabaidaF3FieldSpans returns the inclusive [start,end] byte ranges of the
// F3 block that carry DECLARED protocol fields ({0-15, 20-31, 48-49}),
// derived from the field table.  Reserved byte ranges (16-19, 32-47) and the
// CRC bytes (50-51) are NOT compared: a real BMS may hold nonzero reserved
// bytes or recompute the CRC.
func jiabaidaF3FieldSpans() [][2]int {
	return jiabaidaFieldSpans(jiabaidaF3Fields())
}

// jiabaidaFieldSpans derives inclusive declared-field byte spans from a field
// table by merging contiguous ranges (fields sorted by offset).
func jiabaidaFieldSpans(fields []jiabaidaField) [][2]int {
	sorted := append([]jiabaidaField(nil), fields...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	var spans [][2]int
	for _, f := range sorted {
		start, end := f.Offset, f.Offset+f.Width-1
		if n := len(spans); n > 0 && start <= spans[n-1][1]+1 {
			if end > spans[n-1][1] {
				spans[n-1][1] = end
			}
			continue
		}
		spans = append(spans, [2]int{start, end})
	}
	return spans
}

// jiabaidaEqualOnSpans reports whether got and want are byte-identical on
// every declared-field span.  Length mismatch (including a truncated block
// that cannot cover a span) is a mismatch.
func jiabaidaEqualOnSpans(got, want []byte, spans [][2]int) bool {
	if len(got) != len(want) {
		return false
	}
	for _, span := range spans {
		start, end := span[0], span[1]
		if start < 0 || end < start || end >= len(got) {
			return false
		}
		for i := start; i <= end; i++ {
			if got[i] != want[i] {
				return false
			}
		}
	}
	return true
}
