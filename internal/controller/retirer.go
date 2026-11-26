package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// RetireOptions are the retirer's own ports; it shares the pool reconciler's client, reader, config, poller cache,
// metrics and clock.
type RetireOptions struct {
	Runs    pipeline.RemoveRuns
	Remover pipeline.Remover
}

type retireEntry struct {
	state, reason, object string
	since, logged         time.Time
	rejectedUntil         time.Time
}

// Retirer labels every Agent bound to a tenant cluster and asks the workflows API to remove it from the MCE once
// released. It covers every Agent in the agent namespace, ignored and excluded pools included.
type Retirer struct {
	RetireOptions
	pool     *Reconciler
	removing sync.Mutex // one cap check and removal request at a time across workers
	mu       sync.Mutex
	entries  map[string]retireEntry
}

func (r *Reconciler) NewRetirer(o RetireOptions) (*Retirer, error) {
	if isNil(o.Runs) {
		return nil, errors.New("port Runs is required")
	}
	if isNil(o.Remover) {
		return nil, errors.New("port Remover is required")
	}
	return &Retirer{RetireOptions: o, pool: r, entries: make(map[string]retireEntry)}, nil
}

// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=agents,verbs=patch

func (t *Retirer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cfg := t.pool.snapshot()
	ctx, cancel := context.WithTimeout(ctx, cfg.ReconcileTimeout)
	defer cancel()
	now := t.pool.Clock()
	if req.Namespace != cfg.AgentNamespace {
		return ctrl.Result{}, nil
	}
	u := kube.NewUnstructured(kube.AgentGVK)
	if err := t.pool.Client.Get(ctx, req.NamespacedName, u); err != nil {
		if apierrors.IsNotFound(err) {
			t.forget(req.Name, now)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, &reconcileError{cause: fmt.Errorf("get Agent: %w", err)}
	}
	a, err := kube.ParseAgent(u)
	if err != nil {
		// The pool reconciler reports malformed Agents; never act on one.
		t.forget(req.Name, now)
		return ctrl.Result{RequeueAfter: cfg.Interval}, nil
	}
	switch pipeline.Retirement(a) {
	case pipeline.RetireLabel:
		t.forget(req.Name, now)
		return ctrl.Result{}, t.label(ctx, req.NamespacedName, a)
	case pipeline.RetireNothing:
		t.forget(req.Name, now)
		return ctrl.Result{}, nil
	}
	in := t.observe(ctx, cfg, a, now)
	d := pipeline.DecideRemove(in)
	if d.Server != "" {
		d = t.remove(ctx, cfg, req.NamespacedName, in, now)
	}
	if d.State == "" {
		t.forget(req.Name, now)
		return ctrl.Result{}, nil
	}
	t.record(ctx, a, d, now)
	return ctrl.Result{RequeueAfter: cfg.Interval}, nil
}

func (t *Retirer) label(ctx context.Context, key types.NamespacedName, a kube.Agent) error {
	patch := map[string]any{"metadata": map[string]any{"labels": map[string]any{kube.LabelClaimed: kube.ClaimedValue}}}
	err := t.pool.mergePatch(ctx, kube.AgentGVK, key, patch)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.pool.Metrics.LabelError()
		return &reconcileError{cause: fmt.Errorf("label Agent %s: %w", key.Name, err)}
	}
	t.pool.Metrics.Labelled()
	log.FromContext(ctx).Info("Labelled an Agent bound to a tenant cluster; it is removed from the MCE once released",
		"agent", key.Name, keyPool, a.InfraEnvName(), "server", a.ServerName(), "label", kube.LabelClaimed)
	return nil
}

func (t *Retirer) observe(ctx context.Context, cfg config.Config, a kube.Agent, now time.Time) pipeline.RemoveInput {
	in := pipeline.RemoveInput{Agent: a, MaxOpen: cfg.MaxOpenRemovals, RetryAfter: cfg.RemoveRetryAfter, Now: now}
	pool := a.InfraEnvName()
	in.Paused = cfg.ForPool(pool).Paused
	if pool != "" {
		ie := kube.NewUnstructured(kube.InfraEnvGVK)
		err := t.pool.Client.Get(ctx, types.NamespacedName{Namespace: cfg.AgentNamespace, Name: pool}, ie)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			in.PoolErr = err
		default:
			in.Paused = in.Paused || ie.GetAnnotations()[kube.AnnotationPaused] == "true"
		}
	}
	in.NodePools, in.NodePoolsErr = t.nodePools(ctx, now)
	in.Pollers = t.pool.cachedPollers(ctx, now)
	if server := a.ServerName(); server != "" {
		in.Run, in.RunErr = t.Runs.Describe(ctx, server)
		in.Uninstall, in.UninstallErr = t.Runs.DescribeUninstall(ctx, server)
	}
	return in
}

// nodePools fails on any unreadable Agent-platform NodePool: one that cannot be read could claim anything.
func (t *Retirer) nodePools(ctx context.Context, now time.Time) ([]kube.NodePool, error) {
	list, err := listObjects(ctx, t.pool.Client, kube.NodePoolGVK)
	if err != nil {
		return nil, err
	}
	var nodePools []kube.NodePool
	var problems []error
	for i := range list.Items {
		u := &list.Items[i]
		np, err := kube.ParseNodePool(u, now)
		if err != nil {
			if platform, found, _ := unstructured.NestedString(u.Object, "spec", "platform", "type"); found && platform != "Agent" {
				continue
			}
			problems = append(problems, fmt.Errorf("NodePool %s/%s: %w", u.GetNamespace(), u.GetName(), err))
			continue
		}
		nodePools = append(nodePools, np)
	}
	return nodePools, errors.Join(problems...)
}

// remove re-decides under the hub-wide lock with the open-removal count and an uncached read of the Agent.
func (t *Retirer) remove(ctx context.Context, cfg config.Config, key types.NamespacedName,
	in pipeline.RemoveInput, now time.Time) pipeline.RemoveDecision {
	t.removing.Lock()
	defer t.removing.Unlock()
	if now.Before(t.rejectedUntil(key.Name)) {
		return pipeline.RemoveDecision{State: pipeline.RetireBlocked, Reason: pipeline.WaitRejected}
	}
	in.OpenRemovals, in.OpenErr = t.openRemovals(ctx, cfg)
	d := pipeline.DecideRemove(in)
	if d.Server == "" {
		return d
	}
	u := kube.NewUnstructured(kube.AgentGVK)
	err := t.pool.Reader.Get(ctx, key, u)
	if apierrors.IsNotFound(err) {
		return pipeline.RemoveDecision{Reason: pipeline.WaitNotRetiring}
	}
	recheck := pipeline.RemoveDecision{State: pipeline.RetireWaiting, Reason: pipeline.WaitRecheck}
	if err != nil {
		return recheck
	}
	fresh, err := kube.ParseAgent(u)
	if err != nil {
		return recheck
	}
	in.Agent = fresh
	if again := pipeline.DecideRemove(in); again.Server != d.Server {
		if again.Server == "" {
			return again
		}
		return recheck
	}
	if cfg.ForPool(fresh.InfraEnvName()).DryRunRemove {
		t.pool.Metrics.Removal("dry_run")
		return pipeline.RemoveDecision{State: pipeline.RetireDryRun, Reason: pipeline.WaitDryRun, Server: d.Server}
	}
	res, err := t.Remover.Remove(ctx, pipeline.RemoveRequest{Agent: key.Name, Server: d.Server})
	t.Runs.Invalidate(d.Server)
	result := t.logRemoval(ctx, fresh, d.Server, res, err)
	t.pool.Metrics.Removal(result)
	if result == outcomeReject {
		t.setRejectedUntil(key.Name, now.Add(cfg.RemoveRetryAfter))
		return pipeline.RemoveDecision{State: pipeline.RetireBlocked, Reason: pipeline.WaitRejected, Server: d.Server}
	}
	return pipeline.RemoveDecision{State: pipeline.RetireRemoving, Reason: pipeline.WaitRunning, Server: d.Server}
}

// openRemovals counts Running remove runs among released claimed Agents; runs whose Agent is already gone
// are not counted.
func (t *Retirer) openRemovals(ctx context.Context, cfg config.Config) (int, error) {
	list, err := listObjects(ctx, t.pool.Client, kube.AgentGVK, client.InNamespace(cfg.AgentNamespace))
	if err != nil {
		return 0, err
	}
	open := 0
	for i := range list.Items {
		a, err := kube.ParseAgent(&list.Items[i])
		if err != nil || pipeline.Retirement(a) != pipeline.RetireCandidate || a.ServerName() == "" {
			continue
		}
		run, err := t.Runs.Describe(ctx, a.ServerName())
		if err != nil {
			return 0, err
		}
		if run.Status == pipeline.StatusRunning {
			open++
		}
	}
	return open, nil
}

func (t *Retirer) logRemoval(ctx context.Context, a kube.Agent, server string, res pipeline.RemoveResult, err error) string {
	logger := log.FromContext(ctx)
	fields := []any{"agent", a.Name, keyPool, a.InfraEnvName(), "server", server, "httpStatus", res.HTTPCode,
		keyTook, res.Duration.Round(time.Millisecond).String()}
	if failure, ok := errors.AsType[*pipeline.InstallError](err); ok {
		fields = append(fields, "kind", failure.Kind, "url", failure.URL, "detail", failure.Detail)
		if failure.Body != "" {
			fields = append(fields, "responseBody", failure.Body)
		}
	}
	switch {
	case err == nil && res.AlreadyRunning:
		logger.Info("A remove-server-from-mce run is already in flight for this server", fields...)
		return "already_running"
	case err == nil:
		logger.Info("Asked the workflows API to remove a used server from the MCE",
			append(fields, "workflowID", res.WorkflowID, "runID", res.RunID)...)
		return outcomeStarted
	case errors.Is(err, pipeline.ErrRejected):
		logger.Error(errors.New(summarize(err.Error())), "Remove request rejected by the API; nothing started", fields...)
		return outcomeReject
	default:
		logger.Error(errors.New(summarize(err.Error())), "Remove request outcome is unknown; the next Describe decides", fields...)
		return "ambiguous"
	}
}

var retireMessages = map[string]string{
	pipeline.WaitHeld:        "A used server is still held by an AgentMachine; removal waits until CAPI releases it",
	pipeline.WaitNoServer:    "A used server has no agent-install.openshift.io/bmh label, so its server name is unknown; remove it by hand",
	pipeline.WaitPoolUnknown: "Cannot read the used server's InfraEnv; removal waits",
	pipeline.WaitNodePools:   "Cannot read every NodePool, so herzi cannot prove none can claim this used server; removal waits",
	pipeline.WaitUnguarded:   "A NodePool can still claim this used server: its selector lacks redbull.io/claimed DoesNotExist; removal waits",
	pipeline.WaitNoWorkers:   "No worker polls the remove workflow, activity or segment queue; removal waits",
	pipeline.WaitRejected:    "The remove request was rejected by the API; retrying after removeRetryAfter",
}

// record logs a retiring Agent when its state or reason changes, and every statusInterval otherwise.
func (t *Retirer) record(ctx context.Context, a kube.Agent, d pipeline.RemoveDecision, now time.Time) {
	t.mu.Lock()
	e, seen := t.entries[a.Name]
	if !seen || (progressing(d.State) && !progressing(e.state)) {
		e.since = now
	}
	due := !seen || e.state != d.State || e.reason != d.Reason || e.object != d.Object || now.Sub(e.logged) >= statusInterval
	e.state, e.reason, e.object = d.State, d.Reason, d.Object
	if due {
		e.logged = now
	}
	t.entries[a.Name] = e
	t.publishLocked(now)
	t.mu.Unlock()
	if !due {
		return
	}
	fields := []any{"agent", a.Name, keyPool, a.InfraEnvName(), "server", a.ServerName(), "state", d.State, "reason", d.Reason}
	if d.Object != "" {
		fields = append(fields, "nodePool", d.Object)
	}
	logger := log.FromContext(ctx)
	switch d.State {
	case pipeline.RetireBlocked:
		msg, ok := retireMessages[d.Reason]
		if !ok {
			msg = "A used server cannot be removed from the MCE"
		}
		logger.Error(errors.New(d.Reason), msg, fields...)
	case pipeline.RetireDryRun:
		logger.Info("Would remove a used server from the MCE", fields...)
	default:
		logger.Info("Retiring a used server", fields...)
	}
}

func progressing(state string) bool {
	return state == pipeline.RetireWaiting || state == pipeline.RetireRemoving
}

func (t *Retirer) forget(agent string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.entries[agent]; !ok {
		return
	}
	delete(t.entries, agent)
	t.publishLocked(now)
}

func (t *Retirer) publishLocked(now time.Time) {
	counts := make(map[string]int)
	var oldest time.Duration
	for _, e := range t.entries {
		counts[e.state]++
		if progressing(e.state) {
			oldest = max(oldest, now.Sub(e.since))
		}
	}
	t.pool.Metrics.RetiringAgents(counts, oldest)
}

func (t *Retirer) rejectedUntil(agent string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.entries[agent].rejectedUntil
}

func (t *Retirer) setRejectedUntil(agent string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[agent]
	e.rejectedUntil = until
	t.entries[agent] = e
}
