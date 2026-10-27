package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/pipeline"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type sweepFake struct {
	result pipeline.SweepResult
	now    func() time.Time
	calls  int
}

func (s *sweepFake) Sweep(ctx context.Context, _ pipeline.PoolKey) pipeline.SweepResult {
	s.calls++
	if ctx.Err() != nil {
		return pipeline.SweepResult{Err: ctx.Err()}
	}
	result := s.result
	if result.ObservedAt.IsZero() && s.now != nil {
		result.ObservedAt = s.now()
	}
	return result
}

type queuesFake struct {
	mu     sync.Mutex
	result pipeline.PollerStatus
	calls  int
}

func (q *queuesFake) Pollers(context.Context) pipeline.PollerStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	return q.result
}

type installFake struct {
	mu       sync.Mutex
	requests []pipeline.InstallRequest
	err      error
	result   *pipeline.InstallResult
	notify   chan pipeline.InstallRequest
}

func (i *installFake) Install(_ context.Context, r pipeline.InstallRequest) (pipeline.InstallResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.requests = append(i.requests, r)
	if i.notify != nil {
		i.notify <- r
	}
	if i.result != nil {
		return *i.result, i.err
	}
	result := pipeline.InstallResult{Requested: r.Count}
	for slot := 1; slot <= r.Count; slot++ {
		result.Started = append(result.Started, slot)
	}
	return result, i.err
}
func object(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := kube.NewUnstructured(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}
func infra(name string) *unstructured.Unstructured {
	return object(kube.InfraEnvGVK, "inventory", name)
}
func node(name, pool string, replicas int64) *unstructured.Unstructured {
	u := object(kube.NodePoolGVK, "tenant", name)
	u.Object["spec"] = map[string]any{"replicas": replicas, "platform": map[string]any{"type": "Agent", "agent": map[string]any{"agentLabelSelector": map[string]any{"matchLabels": map[string]any{kube.LabelInfraEnv: pool}}}}}
	return u
}
func agent(name, pool string) *unstructured.Unstructured {
	u := object(kube.AgentGVK, "inventory", name)
	u.SetLabels(map[string]string{kube.LabelInfraEnv: pool})
	u.Object["spec"] = map[string]any{"approved": true}
	u.Object["status"] = map[string]any{"debugInfo": map[string]any{"state": kube.StateKnownUnbound}, "conditions": []any{map[string]any{"type": "Connected", "status": "True"}}}
	return u
}
func testConfig() config.Config {
	defaults := config.Defaults()
	defaults.DryRunGrow = false
	return config.Config{HubConfig: config.DefaultHub(), Defaults: defaults, Pools: map[string]config.PoolOverride{"p": {}, "q": {}}}
}

type fixture struct {
	r       *Reconciler
	c       client.Client
	sweep   *sweepFake
	queues  *queuesFake
	install *installFake
	reg     *prometheus.Registry
	now     time.Time
}

func setup(t *testing.T, cfg config.Config, objects ...client.Object) *fixture {
	t.Helper()
	f := &fixture{c: fake.NewClientBuilder().WithObjects(objects...).Build(), sweep: &sweepFake{}, queues: &queuesFake{result: pipeline.PollerStatus{Activity: 1, Workflow: 2, Segment: 3}}, install: &installFake{}, reg: prometheus.NewRegistry(), now: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)}
	f.sweep.now = func() time.Time { return f.now }
	var err error
	f.r, err = New(Options{Client: f.c, Reader: f.c, Config: config.NewStore(cfg), Sweeper: f.sweep, Queues: f.queues, Installer: f.install, Metrics: metrics.New(f.reg), Clock: func() time.Time { return f.now }})
	require.NoError(t, err)
	return f
}
func (f *fixture) reconcile(t *testing.T, pool string) {
	t.Helper()
	result, err := f.r.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: pool})
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	require.LessOrEqual(t, result.RequeueAfter, 2*time.Minute)
}
func metric(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, sample := range family.Metric {
			match := true
			for k, v := range labels {
				found := false
				for _, l := range sample.Label {
					if l.GetName() == k && l.GetValue() == v {
						found = true
					}
				}
				match = match && found
			}
			if match {
				if sample.Gauge != nil {
					return sample.Gauge.GetValue()
				}
				if sample.Counter != nil {
					return sample.Counter.GetValue()
				}
			}
		}
	}
	return 0
}
func TestReconcileDecisions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		demand       int64
		agents, open int
		want         int
		dry          bool
	}{
		{name: "steady", demand: 4, agents: 6}, {name: "grow", demand: 4, agents: 1, want: 5}, {name: "capped", demand: 50, want: 15}, {name: "open headroom", demand: 50, open: 12, want: 3}, {name: "running subtracted", demand: 4, open: 2, want: 4}, {name: "dry", demand: 4, want: 0, dry: true}, {name: "above target", agents: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Defaults.DryRunGrow = tc.dry
			objs := []client.Object{infra("p"), node("workers", "p", tc.demand)}
			for i := 0; i < tc.agents; i++ {
				objs = append(objs, agent(fmt.Sprint(i), "p"))
			}
			f := setup(t, cfg, objs...)
			for i := 0; i < tc.open; i++ {
				f.sweep.result.Slots = append(f.sweep.result.Slots, pipeline.SlotRun{Slot: i + 1, Status: pipeline.StatusRunning, Started: f.now.Add(-time.Hour)})
			}
			f.reconcile(t, "p")
			if tc.want == 0 {
				require.Empty(t, f.install.requests)
			} else {
				require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: tc.want}}, f.install.requests)
			}
			if tc.dry {
				require.Equal(t, 1.0, metric(t, f.reg, "herzi_orders_total", map[string]string{"result": "dry_run"}))
			}
			if tc.name == "above target" {
				require.Equal(t, 2.0, metric(t, f.reg, "herzi_pool_target", nil))
				require.Zero(t, metric(t, f.reg, "herzi_pool_need", nil))
			}
		})
	}
}
func TestBlockedPools(t *testing.T) {
	for _, kind := range []string{"unmanaged", "paused annotation", "paused config", "early bound", "deleting", "future block", "expired block", "zero activity", "zero workflow", "zero segment", "poller error", "sweep error", "bad selector", "invalid pool config", "malformed Agent", "malformed NodePool", "malformed InfraEnv"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig()
			ie := infra("p")
			np := node("workers", "p", 4)
			objs := []client.Object{ie, np}
			switch kind {
			case "unmanaged":
				delete(cfg.Pools, "p")
			case "paused annotation":
				ie.SetAnnotations(map[string]string{kube.AnnotationPaused: "true"})
			case "paused config":
				cfg.Defaults.Paused = true
			case "early bound":
				ie.Object["spec"] = map[string]any{"clusterRef": map[string]any{"name": "bound"}}
			case "deleting":
				ts := metav1.NewTime(time.Now())
				ie.SetDeletionTimestamp(&ts)
				ie.SetFinalizers([]string{"keep"})
			case "future block":
				ie.SetAnnotations(map[string]string{kube.AnnotationBlockedUntil: "2099-01-01T00:00:00Z"})
			case "expired block":
				ie.SetAnnotations(map[string]string{kube.AnnotationBlockedUntil: "2020-01-01T00:00:00Z"})
			case "bad selector":
				require.NoError(t, unstructured.SetNestedStringMap(np.Object, map[string]string{kube.LabelInfraEnv: "p", "extra": "x"}, "spec", "platform", "agent", "agentLabelSelector", "matchLabels"))
			case "invalid pool config":
				cfg.Defaults.HighPct = -1
			case "malformed Agent":
				a := agent("bad", "p")
				a.Object["spec"] = map[string]any{"approved": "oops"}
				objs = append(objs, a)
			case "malformed NodePool":
				require.NoError(t, unstructured.SetNestedField(np.Object, "oops", "spec", "replicas"))
			case "malformed InfraEnv":
				ie.Object["spec"] = map[string]any{"clusterRef": "oops"}
			}
			f := setup(t, cfg, objs...)
			switch kind {
			case "zero activity":
				f.queues.result.Activity = 0
			case "zero workflow":
				f.queues.result.Workflow = 0
			case "zero segment":
				f.queues.result.Segment = 0
			case "poller error":
				f.queues.result.Err = errors.New("offline")
			case "sweep error":
				f.sweep.result.Err = errors.New("offline")
			}
			f.reconcile(t, "p")
			if kind == "expired block" {
				f.now = f.now.Add(11 * time.Second)
			}
			f.reconcile(t, "p")
			if kind == "expired block" {
				require.Len(t, f.install.requests, 2)
			} else {
				require.Empty(t, f.install.requests)
			}
		})
	}
}

type patchClient struct {
	client.Client
	body      []byte
	patchType types.PatchType
	err       error
}

func (c *patchClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	var err error
	c.patchType = p.Type()
	c.body, err = p.Data(obj)
	if err != nil {
		return err
	}
	if c.err != nil {
		return c.err
	}
	return c.Client.Patch(ctx, obj, p, opts...)
}
func TestOutcomes(t *testing.T) {
	for _, kind := range []string{"ambiguous", "rejected", "short", "failed slots", "patch failure", "unexpected error"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"))
			p := &patchClient{Client: f.c}
			f.r.Client = p
			switch kind {
			case "ambiguous", "patch failure":
				f.install.err = fmt.Errorf("transport: %w", pipeline.ErrAmbiguous)
			case "rejected":
				f.install.err = pipeline.ErrRejected
			case "unexpected error":
				f.install.err = errors.New("programmer error")
			case "short":
				f.install.result = &pipeline.InstallResult{Requested: 2, Started: []int{1}}
			case "failed slots":
				f.install.result = &pipeline.InstallResult{Requested: 2, Failed: []int{1, 2}}
			}
			if kind == "patch failure" {
				p.err = errors.New("patch offline")
			}
			if kind == "patch failure" || kind == "unexpected error" {
				_, err := f.r.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: "p"})
				require.Error(t, err)
				return
			}
			f.reconcile(t, "p")
			if kind == "ambiguous" || kind == "rejected" {
				var body map[string]any
				require.NoError(t, json.Unmarshal(p.body, &body))
				require.Equal(t, types.MergePatchType, p.patchType)
				require.Equal(t, map[string]any{"metadata": map[string]any{"annotations": map[string]any{kube.AnnotationBlockedUntil: f.now.Add(5*time.Minute + time.Second).Format(time.RFC3339)}}}, body)
				f.reconcile(t, "p")
				require.Len(t, f.install.requests, 1)
			}
			result := kind
			if kind == "failed slots" {
				result = "short"
			}
			require.Equal(t, 1.0, metric(t, f.reg, "herzi_orders_total", map[string]string{"result": result}))
			if kind == "short" {
				require.Equal(t, 1.0, metric(t, f.reg, "herzi_machines_ordered_total", nil))
			}
		})
	}
}
func TestProbeClock(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.sweep.result.Slots = []pipeline.SlotRun{{Slot: 1, Status: pipeline.StatusFailed, Closed: f.now.Add(-19 * time.Minute), FailType: pipeline.FailServerNotAvailable}}
	f.reconcile(t, "p")
	require.Empty(t, f.install.requests)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_probe_mode", nil))
	f.now = f.now.Add(time.Minute)
	f.reconcile(t, "p")
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 1}}, f.install.requests)
}
func TestPoolIsolationAndForget(t *testing.T) {
	stray := agent("stray", "")
	f := setup(t, testConfig(), infra("p"), infra("q"), stray, node("stray", "", 5))
	f.sweep.result.Err = errors.New("p failed")
	f.reconcile(t, "p")
	f.sweep.result.Err = nil
	f.reconcile(t, "q")
	require.Equal(t, []pipeline.InstallRequest{{Key: "q", Count: 2}}, f.install.requests)
	require.Equal(t, 2.0, metric(t, f.reg, "herzi_warnings_total", map[string]string{"kind": "agent not in any pool"}))
	require.NoError(t, f.c.Delete(context.Background(), infra("q")))
	f.reconcile(t, "q")
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, l := range sample.Label {
				require.False(t, l.GetName() == "pool" && l.GetValue() == "q")
			}
		}
	}
}
func TestRecheck(t *testing.T) {
	for _, kind := range []string{"satisfied", "paused", "less need", "more demand", "missing", "probe"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"), node("workers", "p", 4))
			ie := infra("p")
			objs := []client.Object{ie, node("workers", "p", 4)}
			switch kind {
			case "satisfied":
				for i := range 6 {
					objs = append(objs, agent(fmt.Sprint(i), "p"))
				}
			case "paused":
				ie.SetAnnotations(map[string]string{kube.AnnotationPaused: "true"})
			case "less need":
				objs = append(objs, agent("one", "p"))
			case "more demand":
				objs = []client.Object{ie, node("workers", "p", 20)}
			case "missing":
				objs = nil
			case "probe":
				f.sweep.result.Slots = []pipeline.SlotRun{{Status: pipeline.StatusFailed, Closed: f.now.Add(-time.Hour), FailType: pipeline.FailServerReserved}}
			}
			f.r.Reader = fake.NewClientBuilder().WithObjects(objs...).Build()
			f.reconcile(t, "p")
			switch kind {
			case "less need":
				require.Equal(t, 5, f.install.requests[0].Count)
			case "more demand":
				require.Equal(t, 6, f.install.requests[0].Count)
			case "probe":
				require.Equal(t, 1, f.install.requests[0].Count)
			default:
				require.Empty(t, f.install.requests)
			}
		})
	}
}
func TestPollersTTL(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"), infra("q"))
			ttl := 30 * time.Second
			if failed {
				f.queues.result.Err = errors.New("offline")
				ttl = 5 * time.Second
			}
			f.reconcile(t, "p")
			f.reconcile(t, "q")
			require.Equal(t, 1, f.queues.calls)
			f.now = f.now.Add(ttl - time.Nanosecond)
			f.reconcile(t, "p")
			require.Equal(t, 1, f.queues.calls)
			f.now = f.now.Add(time.Nanosecond)
			f.reconcile(t, "q")
			require.Equal(t, 2, f.queues.calls)
		})
	}
}
func TestConcurrentPollers(t *testing.T) {
	f := setup(t, testConfig())
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { p := f.r.cachedPollers(context.Background(), f.now); require.Equal(t, 1, p.Activity) })
	}
	wg.Wait()
	require.Equal(t, 1, f.queues.calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.r.pollers = &pollerEntry{done: make(chan struct{}), at: f.now}
	require.ErrorIs(t, f.r.cachedPollers(ctx, f.now).Err, context.Canceled)
}
func TestNewAndNamespace(t *testing.T) {
	f := setup(t, testConfig())
	require.True(t, f.r.NeedLeaderElection())
	o := f.r.Options
	o.Clock = nil
	r, err := New(o)
	require.NoError(t, err)
	require.NotNil(t, r.Clock)
	for _, name := range []string{"client", "reader", "config", "sweeper", "queues", "installer", "metrics"} {
		o := f.r.Options
		switch name {
		case "client":
			o.Client = (*patchClient)(nil)
		case "reader":
			o.Reader = nil
		case "config":
			o.Config = nil
		case "sweeper":
			o.Sweeper = nil
		case "queues":
			o.Queues = nil
		case "installer":
			o.Installer = nil
		case "metrics":
			o.Metrics = nil
		}
		_, err := New(o)
		require.Error(t, err)
	}
	_, err = f.r.Reconcile(context.Background(), ctrl.Request{Namespace: "elsewhere", Name: "p"})
	require.NoError(t, err)
	require.Zero(t, f.sweep.calls)
}

func TestBadMetricsRegistration(t *testing.T) {
	f := setup(t, testConfig())
	o := f.r.Options
	o.Metrics = metrics.New(nil)
	_, err := New(o)
	require.ErrorContains(t, err, "metrics registration")
}

func TestBlockSurvivesRestartAndExpires(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.install.err = pipeline.ErrAmbiguous
	f.reconcile(t, "p")
	restarted, err := New(f.r.Options)
	require.NoError(t, err)
	f.r = restarted
	f.install.err = nil
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	f.now = f.now.Add(5*time.Minute + time.Second)
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 2)
}
func TestFailedSlotsResolvedBySweep(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.install.result = &pipeline.InstallResult{Requested: 2, Failed: []int{1, 2}}
	f.reconcile(t, "p")
	f.sweep.result.Slots = []pipeline.SlotRun{{Slot: 1, Status: pipeline.StatusRunning, Started: f.now}, {Slot: 2, Status: pipeline.StatusRunning, Started: f.now}}
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
}

func TestReconcileUsesHighPctTarget(t *testing.T)              { assertHighPctTarget(t, 18, 3) }
func TestAssignedExceedsDemandCanStillNeedBuffer(t *testing.T) { assertHighPctTarget(t, 21, 0) }
func assertHighPctTarget(t *testing.T, assigned, claimable int) {
	t.Helper()
	objects := []client.Object{infra("p"), node("workers", "p", 20)}
	for i := 0; i < assigned+claimable; i++ {
		a := agent(fmt.Sprint(i), "p")
		if i < assigned {
			require.NoError(t, unstructured.SetNestedMap(a.Object, map[string]any{"name": "cluster"}, "spec", "clusterDeploymentName"))
		}
		objects = append(objects, a)
	}
	// Use 30% because the default 10% leaves demand 20 under the minFree floor.
	cfg := testConfig()
	cfg.Defaults.HighPct = 30
	f := setup(t, cfg, objects...)
	f.reconcile(t, "p")
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 5}}, f.install.requests)
	require.Equal(t, 26.0, metric(t, f.reg, "herzi_pool_target", nil))
	require.Equal(t, 5.0, metric(t, f.reg, "herzi_pool_need", nil))
	require.Equal(t, float64(assigned), metric(t, f.reg, "herzi_pool_assigned", nil))
	require.Equal(t, float64(claimable), metric(t, f.reg, "herzi_pool_claimable", nil))
}

func TestDefaultBufferIsTheFloorUntilDemand25(t *testing.T) {
	for _, tc := range []struct {
		demand, assigned, claimable int
		wantTarget, wantNeed        float64
		wantOrder                   int
	}{
		{demand: 20, assigned: 18, claimable: 3, wantTarget: 22, wantNeed: 1, wantOrder: 1},
		{demand: 24, assigned: 24, claimable: 2, wantTarget: 26, wantNeed: 0, wantOrder: 0},
		{demand: 25, assigned: 25, claimable: 2, wantTarget: 28, wantNeed: 1, wantOrder: 1},
		{demand: 50, assigned: 50, claimable: 0, wantTarget: 55, wantNeed: 5, wantOrder: 5},
	} {
		t.Run(fmt.Sprintf("demand %d", tc.demand), func(t *testing.T) {
			objects := []client.Object{infra("p"), node("workers", "p", int64(tc.demand))}
			for i := 0; i < tc.assigned+tc.claimable; i++ {
				a := agent(fmt.Sprint(i), "p")
				if i < tc.assigned {
					require.NoError(t, unstructured.SetNestedMap(a.Object, map[string]any{"name": "cluster"}, "spec", "clusterDeploymentName"))
				}
				objects = append(objects, a)
			}
			f := setup(t, testConfig(), objects...)
			f.reconcile(t, "p")
			require.Equal(t, tc.wantTarget, metric(t, f.reg, "herzi_pool_target", nil))
			require.Equal(t, tc.wantNeed, metric(t, f.reg, "herzi_pool_need", nil))
			if tc.wantOrder == 0 {
				require.Empty(t, f.install.requests)
				return
			}
			require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: tc.wantOrder}}, f.install.requests)
		})
	}
}
