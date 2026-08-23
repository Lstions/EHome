package nodemgr

import "testing"

func TestComputeConnectionQuality(t *testing.T) {
	cases := []struct {
		name    string
		rssiDbm int
		rttMs   int
		hasRssi bool
		want    int
	}{
		// RSSI-only boundaries (no RTT data: rttMs <= 0)
		{name: "rssi only, excellent (-50)", rssiDbm: -50, rttMs: 0, hasRssi: true, want: 100},
		{name: "rssi only, above good (-40)", rssiDbm: -40, rttMs: 0, hasRssi: true, want: 100},
		{name: "rssi only, bad floor (-90)", rssiDbm: -90, rttMs: 0, hasRssi: true, want: 0},
		{name: "rssi only, below floor (-100)", rssiDbm: -100, rttMs: 0, hasRssi: true, want: 0},
		{name: "rssi only, mid (-70)", rssiDbm: -70, rttMs: 0, hasRssi: true, want: 50},
		{name: "rssi only, quarter (-60)", rssiDbm: -60, rttMs: 0, hasRssi: true, want: 75},

		// RTT-only boundaries (no RSSI)
		{name: "rtt only, fast (20ms)", rssiDbm: 0, rttMs: 20, hasRssi: false, want: 100},
		{name: "rtt only, faster (1ms)", rssiDbm: 0, rttMs: 1, hasRssi: false, want: 100},
		{name: "rtt only, slow (500ms)", rssiDbm: 0, rttMs: 500, hasRssi: false, want: 0},
		{name: "rtt only, slower (1000ms)", rssiDbm: 0, rttMs: 1000, hasRssi: false, want: 0},
		{name: "rtt only, mid (260ms)", rssiDbm: 0, rttMs: 260, hasRssi: false, want: 50},

		// Blended: 0.6*rssi + 0.4*rtt
		{name: "blend both perfect", rssiDbm: -50, rttMs: 20, hasRssi: true, want: 100},
		{name: "blend both worst", rssiDbm: -90, rttMs: 500, hasRssi: true, want: 0},
		{name: "blend mid/mid", rssiDbm: -70, rttMs: 260, hasRssi: true, want: 50},
		{name: "blend good rssi bad rtt", rssiDbm: -50, rttMs: 500, hasRssi: true, want: 60},
		{name: "blend bad rssi good rtt", rssiDbm: -90, rttMs: 20, hasRssi: true, want: 40},
		{name: "blend rssi -70 rtt 20", rssiDbm: -70, rttMs: 20, hasRssi: true, want: 70},
		{name: "blend rssi -50 rtt 260", rssiDbm: -50, rttMs: 260, hasRssi: true, want: 80},

		// hasRssi=true ignores RTT <= 0 (treated as no RTT)
		{name: "has rssi, rtt zero", rssiDbm: -70, rttMs: 0, hasRssi: true, want: 50},
		{name: "has rssi, rtt negative", rssiDbm: -70, rttMs: -5, hasRssi: true, want: 50},

		// No data at all
		{name: "no rssi, no rtt", rssiDbm: 0, rttMs: 0, hasRssi: false, want: 0},
		{name: "no rssi, negative rtt", rssiDbm: 0, rttMs: -1, hasRssi: false, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeConnectionQuality(tc.rssiDbm, tc.rttMs, tc.hasRssi)
			if got != tc.want {
				t.Fatalf("ComputeConnectionQuality(%d, %d, %v)=%d want %d",
					tc.rssiDbm, tc.rttMs, tc.hasRssi, got, tc.want)
			}
			if got < 0 || got > 100 {
				t.Fatalf("score %d out of [0,100]", got)
			}
		})
	}
}
