package sun

import (
	"testing"
	"time"
)

func TestProbeOffsets(t *testing.T) {
	tzs := []string{"UTC", "Asia/Shanghai", "Pacific/Kiritimati", "Pacific/Midway", "America/New_York", "Arctic/Longyearbyen", "Australia/Sydney"}
	lats := []float64{-85, -66.5, -45, -20, 0, 20, 45, 60, 66, 70, 78.22, 85}
	lons := []float64{-179.9, -120, -74, 0, 87.62, 116.4, 151.2, 179.9}
	used := map[int]int{}
	total := 0
	for _, tzn := range tzs {
		tz, err := time.LoadLocation(tzn)
		if err != nil {
			t.Fatal(err)
		}
		for _, lat := range lats {
			for _, lon := range lons {
				for d := 0; d < 365; d += 7 {
					date := time.Date(2026, 1, 1, 12, 0, 0, 0, tz).AddDate(0, 0, d)
					for _, kind := range []Kind{Sunrise, Sunset} {
						y, m, dd := date.In(tz).Date()
						jdBase := julianDay(y, m, dd)
						for _, off := range []int{0, -1, 1, -2, 2} {
							at, ok := sunEventInstant(lat, lon, jdBase+float64(off), kind)
							if !ok {
								continue
							}
							ey, em, ed := at.In(tz).Date()
							if dayDelta(y, m, dd, ey, em, ed) == 0 {
								used[off]++
								total++
								break
							}
						}
					}
				}
			}
		}
	}
	t.Logf("total in-window solutions: %d; offset usage: %v", total, used)
}
