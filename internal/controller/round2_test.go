package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

type annotationClient struct {
	client.Client
	patch  func(context.Context, map[string]any) error
	bodies []map[string]any
}

func (c *annotationClient) Patch(ctx context.Context, obj client.Object, p client.Patch,
	opts ...client.PatchOption) error {
	body, err := p.Data(obj)
	if err != nil {
		return err
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	c.bodies = append(c.bodies, parsed)
	if c.patch != nil {
		if err := c.patch(ctx, parsed); err != nil {
			return err
		}
	}
	return c.Client.Patch(ctx, obj, p, opts...)
}
func markerBody(value any) map[string]any {
	return map[string]any{"metadata": map[string]any{"annotations": map[string]any{kube.AnnotationBlockedUntil: value}}}
}

type invalidatingSweep struct {
	pipeline.RunSweeper
	invalidated int
}

func (s *invalidatingSweep) Invalidate(pipeline.PoolKey) { s.invalidated++ }

func TestF1SlowRecheckCannotSpendStaleSweep(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), node("demand", "p", 10))
	for i := range 10 {
		require.NoError(t, f.c.Create(t.Context(), agent(fmt.Sprint(i), "p")))
	}
	base := f.now
	f.sweep.result.ObservedAt = base
	sweep := &invalidatingSweep{RunSweeper: f.sweep}
	f.r.Sweeper = sweep
	f.reconcile(t, "p")
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 2}}, f.install.requests)
	require.Equal(t, 1, sweep.invalidated)
	f.r.markAttempt("p", base.Add(100*time.Millisecond))
	f.now = base.Add(9 * time.Second)
	f.r.Reader = deadlineClient{Client: f.c, check: func(_ context.Context, stage string) error {
		if stage == "get" {
			time.Sleep(2 * time.Second)
		}
		return nil
	}}
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	// A later observation, rather than elapsed time alone, permits spending.
	f.r.Reader = f.c
	f.now = base.Add(12 * time.Second)
	f.sweep.result.ObservedAt = f.now
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 2)
}

func TestF2PendingMarkerPrecedesPOSTAndSurvivesCancellation(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), node("demand", "p", 50))
	c := &annotationClient{Client: f.c}
	f.r.Client = c
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.patch = func(ctx context.Context, _ map[string]any) error {
		require.NoError(t, ctx.Err())
		d, ok := ctx.Deadline()
		require.True(t, ok)
		require.InDelta(t, 10, time.Until(d).Seconds(), .1)
		return nil
	}
	f.r.Installer = installFunc(func(_ context.Context, req pipeline.InstallRequest) (pipeline.InstallResult, error) {
		require.Equal(t, 15, req.Count)
		require.Len(t, c.bodies, 1)
		require.Equal(t, markerBody(f.now.Add(5*time.Minute+time.Second).Format(time.RFC3339)), c.bodies[0])
		cancel()
		return pipeline.InstallResult{}, pipeline.ErrAmbiguous
	})
	_, err := f.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
	require.NoError(t, err)
	require.Len(t, c.bodies, 2)
	ie := infra("p")
	require.NoError(t, f.c.Get(t.Context(), client.ObjectKeyFromObject(ie), ie))
	require.NotEmpty(t, ie.GetAnnotations()[kube.AnnotationBlockedUntil])
}

func TestF2ExactClearAndPrewriteFailure(t *testing.T) {
	for _, outcome := range []string{"success", "short", "ambiguous", "rejected", "prewrite failure", "clear failure"} {
		t.Run(outcome, func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"))
			c := &annotationClient{Client: f.c}
			f.r.Client = c
			switch outcome {
			case "short":
				f.install.result = &pipeline.InstallResult{Requested: 2, Started: []int{1}}
			case "ambiguous":
				f.install.err = pipeline.ErrAmbiguous
			case "rejected":
				f.install.err = pipeline.ErrRejected
			case "prewrite failure":
				c.patch = func(context.Context, map[string]any) error { return errors.New("offline") }
			case "clear failure":
				c.patch = func(context.Context, map[string]any) error {
					if len(c.bodies) == 2 {
						return errors.New("offline")
					}
					return nil
				}
			}
			sweep := &invalidatingSweep{RunSweeper: f.sweep}
			f.r.Sweeper = sweep
			_, err := f.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
			if outcome == "prewrite failure" {
				require.Error(t, err)
				require.Empty(t, f.install.requests)
				require.Zero(t, sweep.invalidated)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, sweep.invalidated)
			require.Len(t, c.bodies, 2)
			if outcome == "success" || outcome == "short" || outcome == "clear failure" {
				require.Equal(t, markerBody(nil), c.bodies[1])
			} else {
				require.Equal(t, c.bodies[0], c.bodies[1])
			}
		})
	}
}

func TestF4StatePersistsAcrossRestartAndProbePrewrite(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.sweep.result.Slots = []pipeline.SlotRun{{Slot: 1, RunID: "capacity", Status: pipeline.StatusFailed,
		FailType: pipeline.FailServerReserved, Closed: f.now.Add(-21 * time.Minute)}}
	c := &annotationClient{Client: f.c}
	f.r.Client = c
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	// State evidence patch, then marker+probe state in a single patch, then clear marker.
	require.Len(t, c.bodies, 3)
	a := c.bodies[1]["metadata"].(map[string]any)["annotations"].(map[string]any)
	require.Len(t, a, 2)
	state := pipeline.ParseState(a[kube.AnnotationState].(string))
	require.True(t, state.ProbeNotBefore.After(f.now.Add(19*time.Minute)))
	opts := f.r.Options
	var err error
	f.r, err = New(opts)
	require.NoError(t, err)
	f.sweep.result.Slots = []pipeline.SlotRun{{Slot: 1, RunID: "probe", Status: pipeline.StatusRunning,
		Started: f.now.Add(-10 * time.Second)}}
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
}

func TestF8MalformedAgentIsolatedToItsPool(t *testing.T) {
	for _, brokenPool := range []string{"unmanaged", "pool-0", ""} {
		t.Run(brokenPool, func(t *testing.T) {
			cfg := testConfig()
			cfg.Pools = map[string]config.PoolOverride{}
			objects := make([]client.Object, 0, 21)
			for i := range 20 {
				name := fmt.Sprintf("pool-%d", i)
				cfg.Pools[name] = config.PoolOverride{}
				objects = append(objects, infra(name))
			}
			bad := agent("malformed", brokenPool)
			require.NoError(t, unstructured.SetNestedField(bad.Object, "invalid", "status", "conditions"))
			objects = append(objects, bad)
			f := setup(t, cfg, objects...)
			for i := range 20 {
				f.reconcile(t, fmt.Sprintf("pool-%d", i))
			}
			want := 20
			if brokenPool == "pool-0" {
				want = 19
			}
			require.Len(t, f.install.requests, want)
			for _, req := range f.install.requests {
				require.NotEqual(t, brokenPool, string(req.Key))
			}
		})
	}
}

func TestF10ConfigErrorBlocksOnlyInvalidPool(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), infra("q"), infra("r"))
	require.NoError(t, f.r.cfg.Update([]byte("hubName: hub\ninstallURL: http://api/install\npools:\n  p: {paused: true}\n  q: {minBand: 0}\n  r: {}\n")))
	for _, pool := range []string{"p", "q", "r"} {
		f.reconcile(t, pool)
	}
	require.Equal(t, []pipeline.InstallRequest{{Key: "r", Count: 2}}, f.install.requests)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_blocked", map[string]string{"pool": "q", "reason": "config_invalid"}))
}

func TestF2AnnotationMarshalError(t *testing.T) {
	f := setup(t, testConfig())
	require.Error(t, f.r.patchAnnotations(t.Context(), types.NamespacedName{}, map[string]any{"bad": make(chan int)}))
}

func TestF4CachedStateCannotEraseDurableFailures(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	current := pipeline.State{CapFailAt: f.now.Add(-time.Minute),
		ProbeNotBefore: f.now.Add(19 * time.Minute), LastOKAt: f.now.Add(-time.Hour),
		OtherFails: []pipeline.Failure{{RunID: "other", ClosedAt: f.now.Add(-10 * time.Second)}}}
	require.NoError(t, f.r.patchAnnotations(t.Context(), client.ObjectKeyFromObject(infra("p")),
		map[string]any{kube.AnnotationState: pipeline.EncodeState(current)}))
	stale := pipeline.State{CapFailAt: f.now.Add(-21 * time.Minute)}
	state, err := f.r.persistState(t.Context(), client.ObjectKeyFromObject(infra("p")), "", stale, f.now, config.Defaults())
	require.NoError(t, err)
	require.Equal(t, current, state)
	// A failed durable read or patch prevents an order from losing observed evidence.
	f.r.Reader = failingReader{Reader: f.c, get: true}
	_, err = f.r.persistState(t.Context(), client.ObjectKeyFromObject(infra("p")), "", stale, f.now, config.Defaults())
	require.Error(t, err)
	f.r.Reader = f.c
	f.r.Client = &patchClient{Client: f.c, err: errors.New("offline")}
	stale.CapFailAt = f.now
	_, err = f.r.persistState(t.Context(), client.ObjectKeyFromObject(infra("p")), "", stale, f.now, config.Defaults())
	require.Error(t, err)
}

type statefulSweepFake struct {
	pipeline.RunSweeper
	called bool
}

func (s *statefulSweepFake) SweepWithState(ctx context.Context, key pipeline.PoolKey,
	_ pipeline.State, _ config.PoolConfig) pipeline.SweepResult {
	s.called = true
	return s.Sweep(ctx, key)
}
func TestF6ReconcilerUsesStatefulSweeper(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	s := &statefulSweepFake{RunSweeper: f.sweep}
	f.r.Sweeper = s
	f.reconcile(t, "p")
	require.True(t, s.called)
}

func TestF2OutcomeTimeExtendsMarkerAndPatchError(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	c := &annotationClient{Client: f.c}
	f.r.Client = c
	outcomeAt := f.now.Add(5 * time.Minute)
	require.NoError(t, f.r.recordOutcome(t.Context(), client.ObjectKeyFromObject(infra("p")), pipeline.Full,
		pipeline.InstallRequest{Key: "p", Count: 15}, pipeline.InstallResult{}, pipeline.ErrAmbiguous, testConfig(), outcomeAt))
	require.Equal(t, markerBody(f.now.Add(10*time.Minute).Format(time.RFC3339)), c.bodies[0])
	c.patch = func(context.Context, map[string]any) error { return errors.New("offline") }
	require.Error(t, f.r.recordOutcome(t.Context(), client.ObjectKeyFromObject(infra("p")), pipeline.Full,
		pipeline.InstallRequest{Key: "p", Count: 15}, pipeline.InstallResult{}, pipeline.ErrAmbiguous, testConfig(), outcomeAt))
}

func TestF11ConfiguredThresholdAvailableBeforeReconcile(t *testing.T) {
	cfg := testConfig()
	cfg.StuckAfter = 30 * time.Minute
	f := setup(t, cfg, infra("p"))
	require.Equal(t, 1800.0, metric(t, f.reg, "herzi_stuck_after_seconds", nil))
	cfg.StuckAfter = time.Hour
	f.r.cfg = config.NewStore(cfg)
	f.reconcile(t, "p")
	require.Equal(t, 1800.0, metric(t, f.reg, "herzi_stuck_after_seconds", nil))
}
