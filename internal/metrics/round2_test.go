package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/pipeline"
)

func TestF11StuckAfterGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.SetStuckAfter(30 * time.Minute)
	families, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if f.GetName() == "herzi_stuck_after_seconds" {
			require.Equal(t, 1800.0, f.Metric[0].Gauge.GetValue())
			require.Empty(t, f.Metric[0].Label)
			found = true
		}
	}
	require.True(t, found)
}
func TestF5UnknownFailureMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObservePool(pipeline.PoolFacts{Key: "p", Closed: []pipeline.SlotRun{{Status: pipeline.StatusFailed,
		FailType: pipeline.FailUnknown, Closed: time.Now()}}}, pipeline.Verdict{}, pipeline.Hold{}, 2, 3, 2, time.Now())
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "herzi_pool_failure_class" {
			continue
		}
		for _, sample := range f.Metric {
			class := ""
			for _, label := range sample.Label {
				if label.GetName() == "class" {
					class = label.GetValue()
				}
			}
			want := 0.0
			if class == "unknown" {
				want = 1
			}
			require.Equal(t, want, sample.Gauge.GetValue())
		}
	}
}
