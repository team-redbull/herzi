package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type runsFake struct {
	mu          sync.Mutex
	runs        map[string]pipeline.RemoveRun
	err         error
	describes   int
	invalidated []string
}

func (r *runsFake) Describe(_ context.Context, server string) (pipeline.RemoveRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.describes++
	if r.err != nil {
		return pipeline.RemoveRun{}, r.err
	}
	if run, ok := r.runs[server]; ok {
		return run, nil
	}
	return pipeline.RemoveRun{Status: pipeline.StatusNotFound}, nil
}

func (r *runsFake) Invalidate(server string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, server)
}

func (r *runsFake) set(server string, run pipeline.RemoveRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[server] = run
}

// removerFake starts a Running run on success, as the route does, so the next Describe sees it.
type removerFake struct {
	mu       sync.Mutex
	runs     *runsFake
	requests []pipeline.RemoveRequest
	result   pipeline.RemoveResult
	err      error
	notify   chan pipeline.RemoveRequest
}

func (f *removerFake) Remove(_ context.Context, req pipeline.RemoveRequest) (pipeline.RemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.notify != nil {
		f.notify <- req
	}
	if f.err == nil && !f.result.AlreadyRunning {
		f.runs.set(req.Server, pipeline.RemoveRun{Status: pipeline.StatusRunning})
	}
	return f.result, f.err
}

type retireFixture struct {
	*fixture
	ret     *Retirer
	runs    *runsFake
	remover *removerFake
}

func setupRetire(t *testing.T, cfg config.Config, objects ...client.Object) *retireFixture {
	t.Helper()
	f := setup(t, cfg, objects...)
	f.queues.result.Remove = 1
	runs := &runsFake{runs: make(map[string]pipeline.RemoveRun)}
	remover := &removerFake{runs: runs, result: pipeline.RemoveResult{HTTPCode: 202}}
	ret, err := f.r.NewRetirer(RetireOptions{Runs: runs, Remover: remover})
	require.NoError(t, err)
	return &retireFixture{fixture: f, ret: ret, runs: runs, remover: remover}
}

func (f *retireFixture) retire(t *testing.T, name string) ctrl.Result {
	t.Helper()
	result, err := f.ret.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: name})
	require.NoError(t, err)
	return result
}

func liveRemoval() config.Config {
	cfg := testConfig()
	cfg.Defaults.DryRunRemove = false
	return cfg
}

func usedAgent(name, pool, server string) *unstructured.Unstructured {
	u := agent(name, pool)
	labels := u.GetLabels()
	if pool == "" {
		delete(labels, kube.LabelInfraEnv)
	}
	labels[kube.LabelClaimed] = kube.ClaimedValue
	if server != "" {
		labels[kube.LabelBMH] = server
	}
	u.SetLabels(labels)
	return u
}

func bound(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.Object["spec"].(map[string]any)["clusterDeploymentName"] = map[string]any{"name": "tenant", "namespace": "tenant"}
	return u
}

func guardedNode(name, pool string, replicas int64) *unstructured.Unstructured {
	u := node(name, pool, replicas)
	selector := u.Object["spec"].(map[string]any)["platform"].(map[string]any)["agent"].(map[string]any)["agentLabelSelector"].(map[string]any)
	selector["matchExpressions"] = []any{map[string]any{"key": kube.LabelClaimed, "operator": "DoesNotExist"}}
	return u
}

func TestRetirerLabelsEveryBoundAgent(t *testing.T) {
	ignored := infra("ignored")
	ignored.SetLabels(map[string]string{config.DefaultIgnoreLabel: ""})
	paused := infra("paused")
	paused.SetAnnotations(map[string]string{kube.AnnotationPaused: "true"})
	stray := bound(agent("stray", ""))
	stray.SetLabels(nil)
	f := setupRetire(t, testConfig(), infra("p"), ignored, paused,
		bound(agent("one", "p")), bound(agent("two", "ignored")), bound(agent("three", "paused")), stray, agent("free", "p"))
	for _, name := range []string{"one", "two", "three", "stray", "free"} {
		result := f.retire(t, name)
		require.Zero(t, result.RequeueAfter, name)
	}
	for _, name := range []string{"one", "two", "three", "stray"} {
		u := kube.NewUnstructured(kube.AgentGVK)
		require.NoError(t, f.c.Get(context.Background(), types.NamespacedName{Namespace: "inventory", Name: name}, u))
		require.Equal(t, kube.ClaimedValue, u.GetLabels()[kube.LabelClaimed], name)
	}
	u := kube.NewUnstructured(kube.AgentGVK)
	require.NoError(t, f.c.Get(context.Background(), types.NamespacedName{Namespace: "inventory", Name: "free"}, u))
	require.NotContains(t, u.GetLabels(), kube.LabelClaimed, "a free Agent that was never bound is not labelled")
	require.Equal(t, 4.0, metric(t, f.reg, "herzi_agents_labelled_total", nil))
	require.Empty(t, f.remover.requests)

	f.retire(t, "one")
	require.Equal(t, 4.0, metric(t, f.reg, "herzi_agents_labelled_total", nil), "a labelled bound Agent needs nothing")
}

func TestRetirerRemovesOnlyWhenSafe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     func(*config.Config)
		objects []client.Object
		prepare func(*retireFixture)
		remove  bool
		state   string
	}{
		{name: "dry run by default", cfg: func(c *config.Config) { c.Defaults.DryRunRemove = true }, state: pipeline.RetireDryRun},
		{name: "live", remove: true, state: pipeline.RetireRemoving},
		{name: "pool override goes live", cfg: func(c *config.Config) {
			c.Defaults.DryRunRemove = true
			c.Pools["p"] = config.PoolOverride{DryRunRemove: new(false)}
		}, remove: true, state: pipeline.RetireRemoving},
		{name: "excluded pool still removes", cfg: func(c *config.Config) { c.Pools["p"] = config.PoolOverride{Exclude: new(true)} },
			remove: true, state: pipeline.RetireRemoving},
		{name: "unguarded nodepool", objects: []client.Object{node("legacy", "p", 1)}, state: pipeline.RetireBlocked},
		{name: "no remove worker", prepare: func(f *retireFixture) { f.queues.result.Remove = 0 }, state: pipeline.RetireBlocked},
		{name: "paused by config", cfg: func(c *config.Config) { c.Pools["p"] = config.PoolOverride{Paused: new(true)} }, state: pipeline.RetirePaused},
		{name: "describe failed", prepare: func(f *retireFixture) { f.runs.err = errors.New("temporal down") }, state: pipeline.RetireWaiting},
		{name: "already running elsewhere", prepare: func(f *retireFixture) {
			f.runs.set("srv-1", pipeline.RemoveRun{Status: pipeline.StatusRunning})
		}, state: pipeline.RetireRemoving},
		{name: "recent failure backs off", prepare: func(f *retireFixture) {
			f.runs.set("srv-1", pipeline.RemoveRun{Status: pipeline.StatusFailed, Closed: f.now.Add(-time.Minute)})
		}, state: pipeline.RetireWaiting},
		{name: "old failure retries", prepare: func(f *retireFixture) {
			f.runs.set("srv-1", pipeline.RemoveRun{Status: pipeline.StatusFailed, Closed: f.now.Add(-time.Hour)})
		}, remove: true, state: pipeline.RetireRemoving},
		{name: "cap reached", cfg: func(c *config.Config) { c.MaxOpenRemovals = 1 },
			objects: []client.Object{usedAgent("other", "p", "srv-2")},
			prepare: func(f *retireFixture) { f.runs.set("srv-2", pipeline.RemoveRun{Status: pipeline.StatusRunning}) },
			state:   pipeline.RetireWaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := liveRemoval()
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			objects := append([]client.Object{infra("p"), guardedNode("workers", "p", 2), usedAgent("used", "p", "srv-1")}, tc.objects...)
			f := setupRetire(t, cfg, objects...)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			result := f.retire(t, "used")
			require.Equal(t, cfg.Interval, result.RequeueAfter, "a retiring Agent is polled every interval")
			if tc.remove {
				require.Equal(t, []pipeline.RemoveRequest{{Agent: "used", Server: "srv-1"}}, f.remover.requests)
				require.Contains(t, f.runs.invalidated, "srv-1")
			} else {
				require.Empty(t, f.remover.requests)
			}
			require.Equal(t, 1.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": tc.state}))
		})
	}
}

func TestRetirerAsksOnceWhileTheRunIsOpen(t *testing.T) {
	f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 2), usedAgent("used", "p", "srv-1"))
	f.retire(t, "used")
	f.retire(t, "used")
	f.retire(t, "used")
	require.Len(t, f.remover.requests, 1)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_removals_total", map[string]string{"result": "started"}))
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireRemoving}))

	u := kube.NewUnstructured(kube.AgentGVK)
	u.SetNamespace("inventory")
	u.SetName("used")
	require.NoError(t, f.c.Delete(context.Background(), u))
	result := f.retire(t, "used")
	require.Zero(t, result.RequeueAfter, "a removed Agent is forgotten")
	require.Zero(t, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireRemoving}))
}

func TestRetirerCoversStrayAndIgnoredAgents(t *testing.T) {
	ignored := infra("ignored")
	ignored.SetLabels(map[string]string{config.DefaultIgnoreLabel: "true"})
	f := setupRetire(t, liveRemoval(), ignored, guardedNode("workers", "ignored", 1),
		usedAgent("from-ignored", "ignored", "srv-1"), usedAgent("stray", "", "srv-2"))
	f.retire(t, "from-ignored")
	f.retire(t, "stray")
	require.ElementsMatch(t, []pipeline.RemoveRequest{{Agent: "from-ignored", Server: "srv-1"}, {Agent: "stray", Server: "srv-2"}}, f.remover.requests)
}

func TestRetirerNeverRemovesWithoutAServerName(t *testing.T) {
	f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 1), usedAgent("nameless", "p", ""))
	f.retire(t, "nameless")
	require.Empty(t, f.remover.requests)
	require.Zero(t, f.runs.describes, "no id to describe")
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireBlocked}))
}

func TestRetirerDecidesAgainOnTheUncachedAgent(t *testing.T) {
	cached := usedAgent("used", "p", "srv-1")
	rebound := bound(usedAgent("used", "p", "srv-1"))
	f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 1), cached)
	f.r.Reader = fake.NewClientBuilder().WithObjects(rebound).Build()
	f.retire(t, "used")
	require.Empty(t, f.remover.requests, "the API server says it is bound again")

	held := usedAgent("used", "p", "srv-1")
	held.SetLabels(map[string]string{kube.LabelInfraEnv: "p", kube.LabelClaimed: "true", kube.LabelBMH: "srv-1", kube.LabelAgentMachineRef: "m"})
	f.r.Reader = fake.NewClientBuilder().WithObjects(held).Build()
	f.retire(t, "used")
	require.Empty(t, f.remover.requests, "held by an AgentMachine on the API server")

	f.r.Reader = fake.NewClientBuilder().Build()
	f.retire(t, "used")
	require.Empty(t, f.remover.requests, "gone from the API server")
}

func TestRetirerOutcomes(t *testing.T) {
	t.Run("already running is not an error", func(t *testing.T) {
		f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 1), usedAgent("used", "p", "srv-1"))
		f.remover.result = pipeline.RemoveResult{HTTPCode: 409, AlreadyRunning: true}
		f.retire(t, "used")
		require.Equal(t, 1.0, metric(t, f.reg, "herzi_removals_total", map[string]string{"result": "already_running"}))
	})
	t.Run("ambiguous waits for the next describe", func(t *testing.T) {
		f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 1), usedAgent("used", "p", "srv-1"))
		f.remover.err = &pipeline.InstallError{Kind: pipeline.KindTimeout, Class: pipeline.ErrAmbiguous, Detail: "timeout"}
		f.retire(t, "used")
		require.Equal(t, 1.0, metric(t, f.reg, "herzi_removals_total", map[string]string{"result": "ambiguous"}))
		require.Equal(t, 1.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireRemoving}))
	})
	t.Run("rejected backs off for removeRetryAfter", func(t *testing.T) {
		f := setupRetire(t, liveRemoval(), infra("p"), guardedNode("workers", "p", 1), usedAgent("used", "p", "srv-1"))
		f.remover.err = &pipeline.InstallError{Kind: pipeline.KindRejected, Class: pipeline.ErrRejected, HTTP: 422, Detail: "bad"}
		f.retire(t, "used")
		f.retire(t, "used")
		require.Len(t, f.remover.requests, 1)
		require.Equal(t, 1.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireBlocked}))
		f.now = f.now.Add(liveRemoval().RemoveRetryAfter)
		f.remover.err = nil
		f.retire(t, "used")
		require.Len(t, f.remover.requests, 2)
	})
}

func TestRetirerCapHoldsUnderConcurrency(t *testing.T) {
	cfg := liveRemoval()
	cfg.MaxOpenRemovals = 3
	objects := make([]client.Object, 0, 12)
	objects = append(objects, infra("p"), guardedNode("workers", "p", 1))
	for i := range 10 {
		objects = append(objects, usedAgent(fmt.Sprint("used-", i), "p", fmt.Sprint("srv-", i)))
	}
	f := setupRetire(t, cfg, objects...)
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			_, err := f.ret.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: fmt.Sprint("used-", i)})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, f.remover.requests, 3)
	require.Equal(t, 7.0, metric(t, f.reg, "herzi_retiring_agents", map[string]string{"state": pipeline.RetireWaiting}))
}

func TestNewRetirerNeedsItsPorts(t *testing.T) {
	f := setup(t, testConfig())
	_, err := f.r.NewRetirer(RetireOptions{Remover: &removerFake{}})
	require.ErrorContains(t, err, "Runs")
	_, err = f.r.NewRetirer(RetireOptions{Runs: &runsFake{}})
	require.ErrorContains(t, err, "Remover")
}
