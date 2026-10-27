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
	}{{"Client",
		o.Client},
		{"Reader",
			o.Reader},
		{"Config",
			o.Config},
		{"Sweeper",
			o.Sweeper},
		{"Queues",
			o.Queues},
		{"Installer",
			o.Installer},
		{"Metrics",
			o.Metrics}} {
		if isNil(p.value) {
			return nil, fmt.Errorf("port %s is required", p.name)
		}
	}
	if err := o.Metrics.RegistrationError(); err != nil {
		return nil, fmt.Errorf("metrics registration: %w", err)
	}
	o.Metrics.SetStuckAfter(o.Config.Snapshot().StuckAfter)
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Reconciler{Options: o,
			cfg:      o.Config,
			startup:  o.Config.Snapshot().HubConfig,
			reasons:  make(map[string]string),
			attempts: make(map[string]time.Time)},
		nil
}

// NeedLeaderElection documents the controller's default leader-only execution.
func (r *Reconciler) NeedLeaderElection() bool { return true }

// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=infraenvs;agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=infraenvs,verbs=get;patch
// +kubebuilder:rbac:groups=hypershift.openshift.io,resources=nodepools,verbs=get;list;watch

// Reconcile implements design Part 2: "one frozen config for the whole pass".
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
	sweep := r.sweep(ctx, key, pipeline.ParseState(ie.GetAnnotations()[kube.AnnotationState]), pc)
	raw.Sweep = map[pipeline.PoolKey]pipeline.SweepResult{key: sweep}
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
	if facts.Managed {
		facts.State, err = r.persistState(ctx, req.NamespacedName,
			ie.GetAnnotations()[kube.AnnotationState], facts.State, now, pc)
		if err != nil {
			return result, err
		}
	}
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
	case pipeline.Grow:
		result, wakeUntil, exists, retErr = r.grow(ctx, req, cfg, now, started, raw, i)
		return result, retErr
	}
	return result, nil
}

func validate(f pipeline.PoolFacts, raw pipeline.RawSnapshot, cfg config.Config) pipeline.Verdict {
	if err := cfg.PoolErrors()[string(f.Key)]; err != nil {
		f.Problems = append(f.Problems, pipeline.Problem{Pool: f.Key, Msg: err.Error()})
	}
	return pipeline.Validate(f, raw, cfg.ForPool(string(f.Key)))
}
func (r *Reconciler) observe(f pipeline.PoolFacts,
	v pipeline.Verdict,
	i pipeline.Intent,
	p config.PoolConfig,
	now time.Time) {
	_, target, _ := pipeline.Target(f.Demand, p)
	r.Metrics.ObservePool(f, v, i, target, max(0, target-f.Assigned-f.Claimable-f.Settling-len(f.Open)), now)
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

func (r *Reconciler) recordOutcome(ctx context.Context,
	key types.NamespacedName,
	mode pipeline.Mode,
	request pipeline.InstallRequest,
	res pipeline.InstallResult,
	err error,
	cfg config.Config,
	now time.Time) error {
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
	annotations := map[string]any{kube.AnnotationBlockedUntil: nil}
	if err != nil {
		annotations[kube.AnnotationBlockedUntil] = blockedUntil(now, cfg.AmbiguousWait).Format(time.RFC3339)
	}
	if patchErr := r.patchAnnotations(ctx, key, annotations); patchErr != nil {
		log.FromContext(ctx).Error(errors.New("annotation patch was rejected by the API server"),
			"Failed to update InfraEnv marker", "pool", key.Name)
		if err != nil {
			return patchErr
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

func (e *reconcileError) Error() string { return "reconcile failed" }
func (e *reconcileError) Unwrap() error { return e.cause }

// sweepWindow schedules a retry for ports that return stale observations.
// Eligibility itself depends solely on ObservedAt, never on elapsed wall time.
const sweepWindow = 10 * time.Second

func (r *Reconciler) freshSweep(pool string, observed time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.attempts[pool]
	return !ok || observed.After(last)
}
func (r *Reconciler) markAttempt(pool string, at time.Time) {
	r.mu.Lock()
	r.attempts[pool] = at
	r.mu.Unlock()
}

func (r *Reconciler) patchAnnotations(ctx context.Context, key types.NamespacedName, a map[string]any) error {
	patchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": a}})
	if err != nil {
		return fmt.Errorf("encode annotation patch: %w", err)
	}
	ie := kube.NewUnstructured(kube.InfraEnvGVK)
	ie.SetNamespace(key.Namespace)
	ie.SetName(key.Name)
	if err := r.Client.Patch(patchCtx, ie, client.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patch InfraEnv annotations: %w", err)
	}
	return nil
}
func (r *Reconciler) persistState(ctx context.Context, key types.NamespacedName,
	previous string, state pipeline.State, now time.Time, p config.PoolConfig) (pipeline.State, error) {
	if reflect.DeepEqual(pipeline.ParseState(previous), state) {
		return state, nil
	}
	// Never overwrite newer durable evidence with an informer-cache snapshot.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ie := kube.NewUnstructured(kube.InfraEnvGVK)
	if err := r.Reader.Get(readCtx, key, ie); err != nil {
		return state, fmt.Errorf("read durable pool state: %w", err)
	}
	current := pipeline.ParseState(ie.GetAnnotations()[kube.AnnotationState])
	state = unionState(state, current, now, p)
	if reflect.DeepEqual(current, state) {
		return state, nil
	}
	err := r.patchAnnotations(ctx, key, map[string]any{kube.AnnotationState: pipeline.EncodeState(state)})
	return state, err
}

func unionState(a, b pipeline.State, now time.Time, p config.PoolConfig) pipeline.State {
	if b.CapFailAt.After(a.CapFailAt) {
		a.CapFailAt = b.CapFailAt
	}
	if b.LastOKAt.After(a.LastOKAt) {
		a.LastOKAt = b.LastOKAt
	}
	if b.ProbeNotBefore.After(a.ProbeNotBefore) {
		a.ProbeNotBefore = b.ProbeNotBefore
	}
	a.OtherFails = append(append([]pipeline.Failure(nil), a.OtherFails...), b.OtherFails...)
	return pipeline.MergeState(a, nil, now, p)
}

func (r *Reconciler) sweep(ctx context.Context, key pipeline.PoolKey, state pipeline.State,
	p config.PoolConfig) pipeline.SweepResult {
	if sweeper, ok := r.Sweeper.(pipeline.StatefulSweeper); ok {
		return sweeper.SweepWithState(ctx, key, state, p)
	}
	return r.Sweeper.Sweep(ctx, key)
}

func (r *Reconciler) grow(ctx context.Context, req ctrl.Request, cfg config.Config,
	now, started time.Time, raw pipeline.RawSnapshot, i pipeline.Grow) (ctrl.Result, time.Time, bool, error) {
	result := ctrl.Result{RequeueAfter: cfg.Interval}
	var wakeUntil time.Time
	exists := true
	key := pipeline.PoolKey(req.Name)
	pc := cfg.ForPool(req.Name)
	fresh, err := r.gather(ctx, r.Reader, cfg, req.Name, now, true)
	if apierrors.IsNotFound(err) {
		exists = false
		r.forget(req.Name)
		return result, wakeUntil, exists, nil
	}
	if err != nil {
		return result, wakeUntil, exists, err
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
		return result, wakeUntil, exists, nil
	}
	if !ok {
		return result, wakeUntil, exists, nil
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
			return result, wakeUntil, exists, err
		}
		if pc.DryRunGrow {
			log.FromContext(ctx).Info("Would install", "pool", req.Name, "request", request, "count", request.Count)
			r.Metrics.Order(req.Name, g.Mode, "dry_run", 0)
			continue
		}
		if !r.freshSweep(req.Name, raw.Sweep[key].ObservedAt) {
			result.RequeueAfter = min(cfg.Interval, sweepWindow)
			return result, wakeUntil, exists, nil
		}
		attemptAt := now.Add(time.Since(started))
		annotations := map[string]any{
			kube.AnnotationBlockedUntil: blockedUntil(attemptAt, cfg.AmbiguousWait).Format(time.RFC3339),
		}
		if g.Mode == pipeline.Probe {
			f.State.ProbeNotBefore = attemptAt.Add(pc.ProbeInterval)
			annotations[kube.AnnotationState] = pipeline.EncodeState(f.State)
		}
		if err := r.patchAnnotations(ctx, req.NamespacedName, annotations); err != nil {
			return result, wakeUntil, exists, err
		}
		r.markAttempt(req.Name, attemptAt)
		res, installErr := r.Installer.Install(ctx, request)
		if invalidator, ok := r.Sweeper.(pipeline.SweepInvalidator); ok {
			invalidator.Invalidate(key)
		}
		outcomeAt := now.Add(time.Since(started))
		if installErr != nil {
			wakeUntil = blockedUntil(outcomeAt, cfg.AmbiguousWait)
		}
		if err := r.recordOutcome(ctx, req.NamespacedName, g.Mode, request, res, installErr, cfg,
			outcomeAt); err != nil {
			return result, wakeUntil, exists, err
		}

	}
	return result, wakeUntil, exists, nil
}
