package drivers

import (
	"encoding/binary"
	"fmt"
)

// decodeBatchPlanEnvelope decodes the ChannelCmdV2 bounded-plan step-count
// envelope produced by the firmware (bus_worker.c) for ANY driver's bounded
// plan, not just Jiabaida: raw[0] = N steps (1..8), then per step
// kind(1B) + length_le(2B) + response bytes.
//
// 包级共享，非嘉佰达协议一部分: this helper lives in the drivers package as a
// package-level shared function so every driver that receives bounded-plan
// responses (Jiabaida today, SN-3001 via decodeSN3001BatchRaw later) decodes
// the identical firmware envelope.  It deliberately carries no jiabaida
// prefix.  The SN-3001 sibling (decodeSN3001BatchRaw) additionally requires
// N >= 2 steps; this generic decoder keeps the historical 1..8 range.
func decodeBatchPlanEnvelope(raw []byte) ([][]byte, error) {
	if len(raw) < 1 || raw[0] < 1 || raw[0] > 8 {
		return nil, fmt.Errorf("batch envelope: invalid step count")
	}
	steps := make([][]byte, 0, raw[0])
	pos := 1
	for i := 0; i < int(raw[0]); i++ {
		if pos+3 > len(raw) {
			return nil, fmt.Errorf("batch envelope: truncated step header")
		}
		length := int(binary.LittleEndian.Uint16(raw[pos+1 : pos+3]))
		pos += 3 // kind byte plus little-endian length
		if length == 0 || pos+length > len(raw) {
			return nil, fmt.Errorf("batch envelope: invalid step length")
		}
		steps = append(steps, append([]byte(nil), raw[pos:pos+length]...))
		pos += length
	}
	if pos != len(raw) {
		return nil, fmt.Errorf("batch envelope: trailing bytes")
	}
	return steps, nil
}
