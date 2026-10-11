package downlink

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a counter's current value.
//
// Written the same way nodemgr's tests do it (Collect + client_model dto)
// rather than with prometheus/testutil: client_model is already a dependency
// here while testutil is not, and two ways of reading the same counter in one
// repo is one way too many (P4).
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var out dto.Metric
	if err := (<-ch).Write(&out); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return out.GetCounter().GetValue()
}
