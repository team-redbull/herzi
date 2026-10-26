// Package controller implements design Part 2, "Reconcile" and "Growth protocol":
// "last check before spending" and "do not retry the same request".
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/pipeline"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Options supplies all I/O ports. Reader must bypass the informer cache.
type Options struct {
	Client    client.Client
	Reader    client.Reader
	Config    *config.Store
	Sweeper   pipeline.RunSweeper
	Queues    pipeline.QueueChecker
	Installer pipeline.Installer
	Metrics   *metrics.Metrics
	Clock     func() time.Time
	Reload    <-chan event.GenericEvent
}

type pollerEntry struct {
	done   chan struct{}
	at     time.Time
	status pipeline.PollerStatus
}

// Reconciler keeps only disposable log-deduplication and hub poller cache state.
// Safety across restarts comes from Temporal and the InfraEnv block annotation.
type Reconciler struct {
	Options
	cfg      *config.Store
	startup  config.HubConfig
	mu       sync.Mutex
	reasons  map[string]string
	attempts map[string]time.Time
	pollers  *pollerEntry
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

// New validates all required ports, including typed nil interface values.
func New(o Options) (*Reconciler, error) {
	for _, p := range []struct {
		name  string
		value any
	}{{"Client", o.Client}, {"Reader", o.Reader}, {"Config", o.Config}, {"Sweeper", o.Sweeper}, {"Queues", o.Queues}, {"Installer", o.Installer}, {"Metrics", o.Metrics}} {
		if isNil(p.value) {
			return nil, fmt.Errorf("%s is required", p.name)
		}
	}
	if err := o.Metrics.RegistrationError(); err != nil {
		return nil, fmt.Errorf("metrics registration: %w", err)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Reconciler{Options: o, cfg: o.Config, startup: o.Config.Snapshot().HubConfig, reasons: make(map[string]string), attempts: make(map[string]time.Time)}, nil
}

// NeedLeaderElection documents the controller's default leader-only execution.
func (r *Reconciler) NeedLeaderElection() bool { return true }

// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=infraenvs;agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=infraenvs,verbs=get;patch
// +kubebuilder:rbac:groups=hypershift.openshift.io,resources=nodepools,verbs=get;list;watch

// Reconcile implements design Part 2: "one frozen config for the whole pass".
//
//nolint:gocyclo // one flat pass mirroring the design: gather, attribute, validate, decide, recheck, order
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	started := time.Now()
	defer func() {
		r.Metrics.ReconcileDuration(time.Since(started))
		if retErr != nil {
			retErr = &reconcileError{cause: retErr}
		}
	}()
	cfg := r.snapshot()
	pc := cfg.ForPool(req.Name)
	ctx, cancel := context.WithTimeout(ctx, cfg.ReconcileTimeout)
	defer cancel()
	now := r.Clock()
	var wakeUntil time.Time
	defer func() {
		if retErr == nil && wakeUntil.After(now) {
			// Account for API latency without reading the decision clock again.
			// The queue delay starts at return, not at the beginning of the pass.
			remaining := wakeUntil.Sub(now) - time.Since(started)
			result.RequeueAfter = min(cfg.Interval, max(time.Nanosecond, remaining))
		}
	}()
	result = ctrl.Result{RequeueAfter: cfg.Interval}
	if !cfg.Manages(req.Name) {
		r.forget(req.Name)
	}
	if req.Namespace != cfg.AgentNamespace {
		return result, nil
	}
	ie := kube.NewUnstructured(kube.InfraEnvGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, ie); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.Name)
			return result, nil
		}
		return result, fmt.Errorf("get InfraEnv: %w", err)
	}
	raw, err := r.gather(ctx, r.Client, cfg, req.Name, now, false)
	if err != nil {
		return result, err
	}
	// The explicit Get is authoritative even if the list raced with creation/deletion.
	replaceInfraEnv(&raw, ie)
	key := pipeline.PoolKey(req.Name)
	sweepStarted := time.Now()
	raw.Sweep = map[pipeline.PoolKey]pipeline.SweepResult{key: r.Sweeper.Sweep(ctx, key)}
	r.Metrics.SweepDuration(time.Since(sweepStarted))
	if err := ctx.Err(); err != nil {
		return result, err
	}
	raw.Pollers = r.cachedPollers(ctx, now)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r.Metrics.ObservePollers(raw.Pollers)
	facts := pipeline.Attribute(raw, cfg)[key]
	r.warnings(ctx, facts.Warnings)
	verdict := validate(facts, raw, cfg)
	intent := pipeline.Decide(facts, pc, now)
	r.observe(facts, verdict, intent, pc, now)
	exists := true
	defer func() {
		if !exists || !cfg.Manages(req.Name) {
			r.forget(req.Name)
		} else if retErr == nil {
			r.Metrics.Successful(req.Name, now)
		}
	}()
	if verdict.Blocked {
		wakeUntil = facts.BlockedUntil
		r.blocked(ctx, req.Name, verdict.Reasons)
		return result, nil
	}
	r.blocked(ctx, req.Name, nil)
	switch i := intent.(type) {
	case pipeline.Hold:
		log.FromContext(ctx).V(1).Info("Holding pool", "pool", req.Name, "reason", i.Reason)
	case pipeline.WouldRemove:
		log.FromContext(ctx).Info("Would remove machines", "pool", req.Name, "surplus", i.Surplus, "total", i.Total, "target", i.Target, "ceiling", i.Ceiling)
		r.Metrics.WouldRemove(req.Name)
	case pipeline.Grow:
		fresh, err := r.gather(ctx, r.Reader, cfg, req.Name, now, true)
		if apierrors.IsNotFound(err) {
			exists = false
			r.forget(req.Name)
			return result, nil
		}
		if err != nil {
			return result, err
		}
		fresh.Sweep, fresh.Pollers = raw.Sweep, raw.Pollers
		f := pipeline.Attribute(fresh, cfg)[key]
		v := validate(f, fresh, cfg)
		second := pipeline.Decide(f, pc, now)
		r.observe(f, v, second, pc, now)
		g, ok := second.(pipeline.Grow)
		if v.Blocked {
			wakeUntil = f.BlockedUntil
			r.blocked(ctx, req.Name, v.Reasons)
			return result, nil
		}
		if !ok {
			return result, nil
		}
		g.Need = min(g.Need, i.Need)
		if i.Mode == pipeline.Probe {
			g.Mode = pipeline.Probe
		}
		if g.Mode == pipeline.Probe {
			g.Need = min(g.Need, 1)
		}
		for _, request := range pipeline.Plan(g, pc, f) {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if pc.DryRunGrow {
				log.FromContext(ctx).Info("Would install", "pool", req.Name, "request", request, "count", request.Count)
				r.Metrics.Order(req.Name, g.Mode, "dry_run", 0)
				continue
			}
			// Temporal sweeps are cached for 10s in the production port. A
			// watch event must not spend against the same pre-order sweep.
			// Monotonic elapsed time only schedules the throttle; pipeline
			// decisions and annotation timestamps retain the frozen now.
			if wait := r.reserveAttempt(req.Name, now.Add(time.Since(started))); wait > 0 {
				result.RequeueAfter = min(cfg.Interval, wait)
				return result, nil
			}
			res, err := r.Installer.Install(ctx, request)
			if errors.Is(err, pipeline.ErrAmbiguous) || errors.Is(err, pipeline.ErrRejected) {
				wakeUntil = blockedUntil(now, cfg.AmbiguousWait)
			}
			if err := r.recordOutcome(ctx, req.NamespacedName, g.Mode, request, res, err, cfg, now); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func validate(f pipeline.PoolFacts, raw pipeline.RawSnapshot, cfg config.Config) pipeline.Verdict {
	v := pipeline.Validate(f, raw, cfg.ForPool(string(f.Key)))
	if err := cfg.PoolErrors()[string(f.Key)]; err != nil {
		v.Blocked = true
		v.Reasons = append(v.Reasons, err.Error())
	}
	return v
}
func (r *Reconciler) observe(f pipeline.PoolFacts, v pipeline.Verdict, i pipeline.Intent, p config.PoolConfig, now time.Time) {
	low := max(p.MinFree, (p.LowPct*f.Demand+50)/100)
	high := max(low+p.MinBand, (p.HighPct*f.Demand+50)/100)
	target, ceiling := f.Demand+low, f.Demand+high
	r.Metrics.ObservePool(f, v, i, target, ceiling, max(0, target-f.Assigned-f.Claimable-f.Settling-len(f.Open)), now)
}
func (r *Reconciler) forget(pool string) {
	r.Metrics.ForgetPool(pool)
	r.mu.Lock()
	delete(r.reasons, pool)
	delete(r.attempts, pool)
	r.mu.Unlock()
}
func (r *Reconciler) blocked(ctx context.Context, pool string, reasons []string) {
	reason := strings.Join(metrics.BlockReasons(reasons), "; ")
	r.mu.Lock()
	old := r.reasons[pool]
	if reason == "" {
		delete(r.reasons, pool)
	} else {
		r.reasons[pool] = reason
	}
	r.mu.Unlock()
	if reason != "" && reason != old {
		log.FromContext(ctx).Info("Pool blocked", "pool", pool, "reasons", reason)
	}
}
func (r *Reconciler) warnings(ctx context.Context, warnings []pipeline.Warning) {
	seen := make(map[pipeline.Warning]bool)
	for _, w := range warnings {
		if seen[w] {
			continue
		}
		seen[w] = true
		r.Metrics.Warning(w.Msg)
		log.FromContext(ctx).V(1).Info("Inventory warning", "object", w.Object, "reason", w.Msg)
	}
}
func (r *Reconciler) cachedPollers(ctx context.Context, now time.Time) pipeline.PollerStatus {
	r.mu.Lock()
	e := r.pollers
	if e != nil {
		select {
		case <-e.done:
			ttl := 30 * time.Second
			if e.status.Err != nil {
				ttl = 5 * time.Second
			}
			if now.Sub(e.at) >= ttl {
				e = nil
			}
		default:
		}
	}
	if e != nil {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return pipeline.PollerStatus{Err: ctx.Err()}
		case <-e.done:
			return e.status
		}
	}
	e = &pollerEntry{done: make(chan struct{}), at: now}
	r.pollers = e
	r.mu.Unlock()
	status := r.Queues.Pollers(ctx)
	r.mu.Lock()
	e.status = status
	close(e.done)
	r.mu.Unlock()
	return status
}

func (r *Reconciler) recordOutcome(ctx context.Context, key types.NamespacedName, mode pipeline.Mode, request pipeline.InstallRequest, res pipeline.InstallResult, err error, cfg config.Config, now time.Time) error {
	result := "started"
	switch {
	case errors.Is(err, pipeline.ErrAmbiguous):
		result = "ambiguous"
	case errors.Is(err, pipeline.ErrRejected):
		result = "rejected"
		log.FromContext(ctx).Error(pipeline.ErrRejected, "Install request rejected", "pool", key.Name, "request", request)
	case err != nil:
		return fmt.Errorf("install pool %s: %w", key.Name, err)
	case len(res.Started) < res.Requested:
		result = "short"
	}
	r.Metrics.Order(key.Name, mode, result, len(res.Started))
	if result == "ambiguous" || result == "rejected" {
		body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{kube.AnnotationBlockedUntil: blockedUntil(now, cfg.AmbiguousWait).Format(time.RFC3339)}}})
		ie := kube.NewUnstructured(kube.InfraEnvGVK)
		ie.SetNamespace(key.Namespace)
		ie.SetName(key.Name)
		if patchErr := r.Client.Patch(ctx, ie, client.RawPatch(types.MergePatchType, body)); patchErr != nil {
			log.FromContext(ctx).Error(errors.New("annotation patch was rejected by the API server"), "Failed to block InfraEnv", "pool", key.Name)
			return fmt.Errorf("patch InfraEnv block annotation: %w", patchErr)
		}
	}
	return nil
}

// snapshot freezes startup-bound settings as well as the live configuration for
// one pass. Reloading identity cannot redirect watches or an existing installer.
func (r *Reconciler) snapshot() config.Config {
	cfg := r.cfg.Snapshot()
	live := cfg.HubConfig
	cfg.HubConfig = r.startup
	cfg.Interval, cfg.ReconcileTimeout = live.Interval, live.ReconcileTimeout
	cfg.AmbiguousWait, cfg.StuckAfter = live.AmbiguousWait, live.StuckAfter
	return cfg
}

// RFC3339 has second precision; round up so tiny valid waits never vanish.
func blockedUntil(now time.Time, wait time.Duration) time.Time {
	until := now.Add(wait).UTC()
	if until.Nanosecond() != 0 {
		until = until.Truncate(time.Second).Add(time.Second)
	}
	return until
}

// Error text is safe for controller-runtime to log, while callers can still use
// errors.Is/As to inspect the wrapped cause. External error strings may contain
// credentials or arbitrary HTTP response bodies.
type reconcileError struct{ cause error }

func (e *reconcileError) Error() string { return "Reconcile failed" }
func (e *reconcileError) Unwrap() error { return e.cause }

// sweepWindow matches the production sweeper CacheTTL (design Part 2, Growth
// protocol). Main sets that TTL explicitly. This disposable throttle is not a
// replacement for the persistent block annotation or the uncached recheck.
const sweepWindow = 10 * time.Second

func (r *Reconciler) reserveAttempt(pool string, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.attempts[pool]; ok && now.Sub(last) < sweepWindow {
		return sweepWindow - now.Sub(last)
	}
	r.attempts[pool] = now
	return 0
}
