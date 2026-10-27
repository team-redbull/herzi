// Package metrics implements docs/PLAN.md's Metrics catalogue and design Part 2:
// "gauges every pass, even when blocked".
package metrics

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/team-redbull/herzi/internal/pipeline"
)

// Metrics owns collectors registered with the caller's registry. Methods are safe
// for concurrent pools; no collector uses the process-global registry.
type Metrics struct {
	registrationErr  error
	mu               sync.Mutex
	gauges           map[string]*prometheus.GaugeVec
	blocked, failure *prometheus.GaugeVec
	orders           *prometheus.CounterVec
	machines         *prometheus.CounterVec
	warnings         *prometheus.CounterVec
	pollers          *prometheus.GaugeVec
	reconcile, sweep prometheus.Histogram
	valid            prometheus.Gauge
	stuckAfter       prometheus.Gauge
}

// New registers every metric in the catalogue. The caller supplies a fresh or
// application-owned Registerer; duplicate registrations reuse the same collector.
// labelPool is the one label every per-pool series carries.
const labelPool = "pool"
const classOther = "other"

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{gauges: make(map[string]*prometheus.GaugeVec)}
	register := func(c prometheus.Collector) prometheus.Collector {
		if reg == nil {
			m.registrationErr = errors.New("metrics registerer is required")
			return c
		}
		if err := reg.Register(c); err != nil {
			if e, ok := err.(prometheus.AlreadyRegisteredError); ok && reflect.TypeOf(e.ExistingCollector) == reflect.TypeOf(c) {
				return e.ExistingCollector
			}
			// The fixed New API has no error return. Controller.New checks this
			// error before accepting Metrics, so conflicts cannot go unnoticed.
			m.registrationErr = errors.Join(m.registrationErr, fmt.Errorf("register metric: %w", err))
		}
		return c
	}
	for _, name := range []string{"demand",
		"assigned",
		"claimable",
		"settling",
		"stuck_agents",
		"open_runs",
		"target",
		"need",
		"probe_mode"} {
		m.gauges[name] = register(prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "herzi_pool_" + name,
			Help: "Pool " + name},
			[]string{labelPool})).(*prometheus.GaugeVec)
	}
	for _, name := range []string{"oldest_open_run_seconds", "last_successful_reconcile_timestamp_seconds"} {
		m.gauges[name] = register(prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "herzi_" + name,
			Help: name},
			[]string{labelPool})).(*prometheus.GaugeVec)
	}
	m.blocked = register(prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "herzi_pool_blocked",
		Help: "Pool blocked this pass"},
		[]string{labelPool,
			"reason"})).(*prometheus.GaugeVec)
	m.failure = register(prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "herzi_pool_failure_class",
		Help: "Latest failure class"},
		[]string{labelPool,
			"class"})).(*prometheus.GaugeVec)
	m.orders = register(prometheus.NewCounterVec(prometheus.CounterOpts{Name: "herzi_orders_total",
		Help: "Install requests"},
		[]string{labelPool,
			"mode",
			"result"})).(*prometheus.CounterVec)
	m.machines = register(prometheus.NewCounterVec(prometheus.CounterOpts{Name: "herzi_machines_ordered_total",
		Help: "Machines started"},
		[]string{labelPool})).(*prometheus.CounterVec)
	m.warnings = register(prometheus.NewCounterVec(prometheus.CounterOpts{Name: "herzi_warnings_total",
		Help: "Hub findings"},
		[]string{"kind"})).(*prometheus.CounterVec)
	m.pollers = register(prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "herzi_pollers",
		Help: "Queue pollers"},
		[]string{"queue"})).(*prometheus.GaugeVec)
	m.reconcile = register(prometheus.NewHistogram(prometheus.HistogramOpts{Name: "herzi_reconcile_duration_seconds",
		Help: "Reconcile duration"})).(prometheus.Histogram)
	m.sweep = register(prometheus.NewHistogram(prometheus.HistogramOpts{Name: "herzi_sweep_duration_seconds",
		Help: "Pool sweep duration"})).(prometheus.Histogram)
	m.valid = register(prometheus.NewGauge(prometheus.GaugeOpts{Name: "herzi_config_valid",
		Help: "Last configuration accepted"})).(prometheus.Gauge)
	m.stuckAfter = register(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "herzi_stuck_after_seconds", Help: "Configured open run alert threshold"})).(prometheus.Gauge)
	return m
}
func flag(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ObservePool publishes counted terms and formula outputs, resetting stale reasons
// and classes. now is the reconcile's single frozen clock value.
func (m *Metrics) ObservePool(f pipeline.PoolFacts,
	v pipeline.Verdict,
	intent pipeline.Intent,
	target,
	need int,
	now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := string(f.Key)
	values := map[string]float64{"demand": float64(f.Demand),
		"assigned":     float64(f.Assigned),
		"claimable":    float64(f.Claimable),
		"settling":     float64(f.Settling),
		"stuck_agents": float64(f.StuckAgents),
		"open_runs":    float64(len(f.Open)),
		"target":       float64(target),
		"need":         float64(need)}
	probe := false
	switch i := intent.(type) {
	case pipeline.Grow:
		probe = i.Mode == pipeline.Probe
	case pipeline.Hold:
		probe = i.Reason == pipeline.HoldProbeWait || i.Reason == pipeline.HoldProbeRunning
	}
	values["probe_mode"] = flag(probe)
	oldest := 0.0
	for _, r := range f.Open {
		if !r.Started.IsZero() {
			oldest = max(oldest, now.Sub(r.Started).Seconds())
		}
	}
	values["oldest_open_run_seconds"] = oldest
	for name, value := range values {
		m.gauges[name].WithLabelValues(pool).Set(value)
	}
	m.blocked.DeletePartialMatch(prometheus.Labels{labelPool: pool})
	if !v.Blocked {
		m.blocked.WithLabelValues(pool, "").Set(0)
	}
	for _, reason := range BlockReasons(v.Reasons) {
		m.blocked.WithLabelValues(pool, reason).Set(1)
	}
	var latest *pipeline.SlotRun
	for i := range f.Closed {
		r := &f.Closed[i]
		if (r.Status == pipeline.StatusFailed ||
			r.Status == pipeline.StatusTimedOut) &&
			(latest == nil ||
				r.Closed.After(latest.Closed) ||
				(r.Closed.Equal(latest.Closed) &&
					r.Slot > latest.Slot)) {
			latest = r
		}
	}
	for _, class := range []string{"capacity", classOther, "unknown"} {
		actual := classOther
		if latest != nil {
			if pipeline.IsCapacityFailure(latest.FailType) {
				actual = "capacity"
			}
			if latest.FailType == pipeline.FailUnknown {
				actual = "unknown"
			}
		}
		active := latest != nil && class == actual
		m.failure.WithLabelValues(pool, class).Set(flag(active))
	}
}
func (m *Metrics) ObservePollers(p pipeline.PollerStatus) {
	m.pollers.WithLabelValues("activity").Set(float64(p.Activity))
	m.pollers.WithLabelValues("workflow").Set(float64(p.Workflow))
	m.pollers.WithLabelValues("segment").Set(float64(p.Segment))
}
func (m *Metrics) Order(pool string, mode pipeline.Mode, result string, started int) {
	label := "full"
	if mode == pipeline.Probe {
		label = "probe"
	}
	m.orders.WithLabelValues(pool, label, result).Inc()
	m.machines.WithLabelValues(pool).Add(float64(started))
}
func (m *Metrics) Warning(kind string)               { m.warnings.WithLabelValues(kind).Inc() }
func (m *Metrics) SetConfigValid(valid bool)         { m.valid.Set(flag(valid)) }
func (m *Metrics) ReconcileDuration(d time.Duration) { m.reconcile.Observe(d.Seconds()) }
func (m *Metrics) SweepDuration(d time.Duration)     { m.sweep.Observe(d.Seconds()) }
func (m *Metrics) Successful(pool string, now time.Time) {
	m.gauges["last_successful_reconcile_timestamp_seconds"].WithLabelValues(pool).Set(float64(now.UnixNano()) / 1e9)
}

// ForgetPool bounds cardinality after deletion or removal from managed config.
func (m *Metrics) ForgetPool(pool string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.gauges {
		g.DeleteLabelValues(pool)
	}
	labels := prometheus.Labels{labelPool: pool}
	m.blocked.DeletePartialMatch(labels)
	m.failure.DeletePartialMatch(labels)
	m.orders.DeletePartialMatch(labels)
	m.machines.DeleteLabelValues(pool)
}

// RegistrationError reports registry conflicts without panicking. Controller.New
// rejects Metrics with a registration error before reconciling any pool.
func (m *Metrics) RegistrationError() error { return m.registrationErr }

// BlockReasons maps the pipeline's diagnostic strings to a finite label
// vocabulary. Neither timestamps, object names nor external error text belongs
// in a Prometheus label (or in the change-only Info log).
func BlockReasons(reasons []string) []string {
	seen := make(map[string]bool)
	for _, reason := range reasons {
		kind := classOther
		switch {
		case reason == "pool is unmanaged":
			kind = "unmanaged"
		case reason == "pool is paused":
			kind = "paused"
		case strings.HasPrefix(reason, "pool blocked until "):
			kind = "blocked_until"
		case reason == "InfraEnv is deleting":
			kind = "deleting"
		case reason == "InfraEnv is early-bound":
			kind = "early_bound"
		case strings.HasPrefix(reason, "pool sweep failed:"):
			kind = "sweep_error"
		case strings.HasPrefix(reason, "read "):
			kind = "read_error"
		case strings.HasPrefix(reason, "poller check failed:"):
			kind = "poller_error"
		case reason == "activity queue has no pollers":
			kind = "activity_pollers"
		case reason == "workflow queue has no pollers":
			kind = "workflow_pollers"
		case reason == "segment queue has no pollers":
			kind = "segment_pollers"
		case strings.Contains(reason, ": selector must be only the InfraEnv label"):
			kind = "selector"
		case strings.Contains(reason, ": no replicas (autoscaling?)"):
			kind = "replicas"
		case strings.HasPrefix(reason, "pool "):
			kind = "config_invalid"
		}
		seen[kind] = true
	}
	result := make([]string, 0, len(seen))
	for kind := range seen {
		result = append(result, kind)
	}
	slices.Sort(result)
	return result
}

// SetStuckAfter exposes the effective configured alert threshold.
func (m *Metrics) SetStuckAfter(d time.Duration) { m.stuckAfter.Set(d.Seconds()) }
