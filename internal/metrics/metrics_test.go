package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/pipeline"
)

func TestCatalogueAndForget(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	now := time.Unix(10000, 0)
	facts := pipeline.PoolFacts{Key: "p", Demand: 5, Assigned: 2, Claimable: 1, Settling: 1, StuckAgents: 3, Open: []pipeline.SlotRun{{Started: now.Add(-time.Hour)}, {Started: now.Add(-time.Minute)}, {}}, Closed: []pipeline.SlotRun{{Status: pipeline.StatusFailed, FailType: pipeline.FailServerReserved, Closed: now}}}
	m.ObservePool(facts, pipeline.Verdict{Blocked: true, Reasons: []string{"paused"}}, pipeline.Grow{Mode: pipeline.Probe}, 7, 1, now)
	m.Order("p", pipeline.Full, "started", 2)
	m.Order("p", pipeline.Probe, "short", 1)
	m.Warning("stray")
	m.SetConfigValid(true)
	m.ObservePollers(pipeline.PollerStatus{Activity: 1, Workflow: 2, Segment: 3})
	m.ReconcileDuration(time.Second)
	m.SweepDuration(time.Second)
	m.Successful("p", now)
	expected := map[string]struct {
		kind   string
		labels []string
	}{
		"herzi_stuck_after_seconds": {"GAUGE", nil},
		"herzi_pool_blocked":        {"GAUGE", []string{"pool", "reason"}}, "herzi_pool_failure_class": {"GAUGE", []string{"class", "pool"}}, "herzi_orders_total": {"COUNTER", []string{"mode", "pool", "result"}}, "herzi_machines_ordered_total": {"COUNTER", []string{"pool"}}, "herzi_warnings_total": {"COUNTER", []string{"kind"}}, "herzi_pollers": {"GAUGE", []string{"queue"}}, "herzi_config_valid": {"GAUGE", nil}, "herzi_reconcile_duration_seconds": {"HISTOGRAM", nil}, "herzi_sweep_duration_seconds": {"HISTOGRAM", nil},
	}
	for _, name := range []string{"demand", "assigned", "claimable", "settling", "stuck_agents", "open_runs", "target", "need", "probe_mode"} {
		expected["herzi_pool_"+name] = struct {
			kind   string
			labels []string
		}{"GAUGE", []string{"pool"}}
	}
	for _, name := range []string{"oldest_open_run_seconds", "last_successful_reconcile_timestamp_seconds"} {
		expected["herzi_"+name] = struct {
			kind   string
			labels []string
		}{"GAUGE", []string{"pool"}}
	}
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, len(expected))
	for _, family := range families {
		want, ok := expected[family.GetName()]
		require.True(t, ok, family.GetName())
		require.Equal(t, want.kind, family.GetType().String())
		for _, sample := range family.Metric {
			labels := make([]string, 0, len(sample.Label))
			for _, l := range sample.Label {
				labels = append(labels, l.GetName())
			}
			require.ElementsMatch(t, want.labels, labels)
		}
		if family.GetName() == "herzi_oldest_open_run_seconds" {
			require.Equal(t, 3600.0, family.Metric[0].Gauge.GetValue())
		}
	}
	m.ObservePool(pipeline.PoolFacts{Key: "p"}, pipeline.Verdict{}, pipeline.Hold{Reason: pipeline.HoldAtOrAboveTarget}, 2, 2, now)
	families, err = reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "herzi_pool_blocked" {
			require.Len(t, family.Metric, 1)
			require.Zero(t, family.Metric[0].Gauge.GetValue())
		}
	}
	m.ForgetPool("p")
	families, err = reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				require.NotEqual(t, "pool", label.GetName())
			}
		}
	}
}
func TestProbeAndLatestFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	now := time.Now()
	for _, intent := range []pipeline.Intent{pipeline.Hold{Reason: pipeline.HoldProbeWait}, pipeline.Hold{Reason: pipeline.HoldProbeRunning}, pipeline.Grow{Mode: pipeline.Full}} {
		m.ObservePool(pipeline.PoolFacts{Key: "p", Closed: []pipeline.SlotRun{{Slot: 1, Status: pipeline.StatusFailed, Closed: now, FailType: pipeline.FailServerReserved}, {Slot: 2, Status: pipeline.StatusTimedOut, Closed: now}}}, pipeline.Verdict{}, intent, 1, 1, now)
	}
	m2 := New(reg)
	m2.SetConfigValid(false)
	m2.Warning("warning")
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "herzi_pool_failure_class" {
			for _, sample := range family.Metric {
				class := ""
				for _, l := range sample.Label {
					if l.GetName() == "class" {
						class = l.GetValue()
					}
				}
				want := 0.0
				if class == "other" {
					want = 1
				}
				require.Equal(t, want, sample.Gauge.GetValue())
			}
		}
	}
}

func TestRegistrationErrors(t *testing.T) {
	require.Error(t, New(nil).RegistrationError())
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounterVec(prometheus.CounterOpts{Name: "herzi_pool_demand", Help: "Pool demand"}, []string{"pool"}))
	require.Error(t, New(reg).RegistrationError())
	reg = prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "herzi_pool_demand", Help: "Incompatible help"}))
	require.Error(t, New(reg).RegistrationError())
}

func TestBoundedReasons(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObservePool(pipeline.PoolFacts{Key: "p"}, pipeline.Verdict{Blocked: true, Reasons: []string{"pool blocked until 2099-01-01T00:00:00Z", "read Agent inventory/bad failed: secret", "np-unique: selector must be only the InfraEnv label"}}, pipeline.Hold{}, 0, 0, time.Now())
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "herzi_pool_blocked" {
			for _, sample := range f.Metric {
				for _, l := range sample.Label {
					if l.GetName() == "reason" {
						require.Contains(t, []string{"blocked_until", "read_error", "selector"}, l.GetValue())
					}
				}
			}
		}
	}
}

func TestReasonVocabulary(t *testing.T) {
	reasons := []string{"pool is unmanaged", "pool is paused", "pool blocked until 2030-01-01T00:00:00Z", "InfraEnv is deleting", "InfraEnv is early-bound", "pool sweep failed: one", "read Agent bad failed: err", "poller check failed: err", "activity queue has no pollers", "workflow queue has no pollers", "segment queue has no pollers", "tenant/n: selector must be only the InfraEnv label", "tenant/n: no replicas (autoscaling?)", "pool \"p\": bad config", "arbitrary text", "arbitrary text"}
	require.Equal(t, []string{"activity_pollers", "blocked_until", "config_invalid", "deleting", "early_bound", "other", "paused", "poller_error", "read_error", "replicas", "segment_pollers", "selector", "sweep_error", "unmanaged", "workflow_pollers"}, BlockReasons(reasons))
}

func TestRemovedMetricsAreNotRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObservePool(pipeline.PoolFacts{Key: "p", Claimable: 100}, pipeline.Verdict{}, pipeline.Hold{Reason: pipeline.HoldAtOrAboveTarget}, 2, 0, time.Now())
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotContains(t, []string{"herzi_would_remove_total", "herzi_pool_ceiling"}, family.GetName())
	}
	require.NoError(t, reg.Register(prometheus.NewGauge(prometheus.GaugeOpts{Name: "herzi_pool_ceiling", Help: "retired"})))
	require.NoError(t, reg.Register(prometheus.NewCounter(prometheus.CounterOpts{Name: "herzi_would_remove_total", Help: "retired"})))
}
