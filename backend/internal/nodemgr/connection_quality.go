package nodemgr

// Connection quality synthesis thresholds. These are generic WiFi/RTT
// heuristics — not tied to ESP32 firmware — so any WiFi node benefits.
const (
	// RSSI (dBm) scoring window: rssi >= rssiGoodDbm → 100, rssi <= rssiBadDbm → 0.
	rssiGoodDbm = -50
	rssiBadDbm  = -90

	// RTT (ms) scoring window: rtt <= rttGoodMs → 100, rtt >= rttBadMs → 0.
	rttGoodMs = 20
	rttBadMs  = 500

	// Blend weights when both signals are present.
	rssiWeight = 0.6
	rttWeight  = 0.4
)

// clampQuality confines v to [0, 100].
func clampQuality(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// rssiScore maps an RSSI in dBm to 0..100 linearly across [rssiBadDbm, rssiGoodDbm].
func rssiScore(rssiDbm int) int {
	if rssiDbm >= rssiGoodDbm {
		return 100
	}
	if rssiDbm <= rssiBadDbm {
		return 0
	}
	return clampQuality((rssiDbm - rssiBadDbm) * 100 / (rssiGoodDbm - rssiBadDbm))
}

// rttScore maps an RTT in ms to 0..100 linearly across [rttGoodMs, rttBadMs].
// Caller guarantees rttMs > 0.
func rttScore(rttMs int) int {
	if rttMs <= rttGoodMs {
		return 100
	}
	if rttMs >= rttBadMs {
		return 0
	}
	return clampQuality((rttBadMs - rttMs) * 100 / (rttBadMs - rttGoodMs))
}

// ComputeConnectionQuality synthesizes a 0..100 link quality score from WiFi
// RSSI (dBm) and ping RTT (ms). hasRssi reports whether rssiDbm carries real
// data; rttMs <= 0 is treated as "no RTT data" (RTT is measured once after
// Hello and may be stale or absent).
//
// Blending rules:
//   - hasRssi && rttMs > 0 → 0.6*rssiScore + 0.4*rttScore
//   - hasRssi only         → rssiScore
//   - rttMs > 0 only       → rttScore
//   - neither              → 0
//
// Pure function; safe for unit tests and reuse outside nodemgr.
func ComputeConnectionQuality(rssiDbm int, rttMs int, hasRssi bool) int {
	hasRtt := rttMs > 0
	switch {
	case hasRssi && hasRtt:
		q := rssiWeight*float64(rssiScore(rssiDbm)) + rttWeight*float64(rttScore(rttMs))
		return clampQuality(int(q + 0.5)) // round half up
	case hasRssi:
		return clampQuality(rssiScore(rssiDbm))
	case hasRtt:
		return clampQuality(rttScore(rttMs))
	default:
		return 0
	}
}
