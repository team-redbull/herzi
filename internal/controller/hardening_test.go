package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const reloadConfig = `hubName: ocp4-test
installURL: https://example.invalid/install
activityQueue: activity
workflowQueue: workflow
segmentQueue: segment
agentNamespace: other
interval: 9m
reconcileTimeout: 30s
pools: {p: {}, q: {}}
`

func TestStartupNamespaceFrozen(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	require.NoError(t, f.r.Config.Update([]byte(reloadConfig)))
	ctx := context.Background()
	want := client.ObjectKeyFromObject(infra("p"))
	require.Equal(t, want, f.r.mapAgent(ctx, agent("a", "p"))[0].NamespacedName)
	require.Equal(t, want, f.r.mapNodePool(ctx, node("n", "p", 2))[0].NamespacedName)
	require.Equal(t, want, f.r.allPools(ctx, nil)[0].NamespacedName)
	result, err := f.r.Reconcile(ctx, ctrl.Request{NamespacedName: want})
	require.NoError(t, err)
	require.Equal(t, 9*time.Minute, result.RequeueAfter)
	require.Len(t, f.install.requests, 1)
}
func TestBlockedExpiryRequeue(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "fresh"}[fresh], func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"))
			ie := infra("p")
			ie.SetAnnotations(map[string]string{kube.AnnotationBlockedUntil: f.now.Add(15 * time.Second).Format(time.RFC3339)})
			if fresh {
				other := setup(t, testConfig(), ie)
				f.r.Reader = other.c
			} else {
				require.NoError(t, f.c.Patch(context.Background(), ie, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"herzi.redbull.io/blocked-until":"2026-10-26T10:00:15Z"}}}`))))
			}
			result, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ie)})
			require.NoError(t, err)
			require.Positive(t, result.RequeueAfter)
			require.LessOrEqual(t, result.RequeueAfter, 15*time.Second)
			require.Empty(t, f.install.requests)
		})
	}
}
func TestNewBlockRequeueAndRounding(t *testing.T) {
	cfg := testConfig()
	cfg.AmbiguousWait = time.Nanosecond
	f := setup(t, cfg, infra("p"))
	f.now = f.now.Add(123 * time.Millisecond)
	f.install.err = pipeline.ErrAmbiguous
	result, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
	require.NoError(t, err)
	ie := infra("p")
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKeyFromObject(ie), ie))
	until, err := time.Parse(time.RFC3339, ie.GetAnnotations()[kube.AnnotationBlockedUntil])
	require.NoError(t, err)
	require.True(t, until.After(f.now))
	require.Positive(t, result.RequeueAfter)
	require.LessOrEqual(t, result.RequeueAfter, until.Sub(f.now))
}

type hookReader struct {
	client.Reader
	hook func()
}

func (r hookReader) Get(ctx context.Context, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	r.hook()
	return r.Reader.Get(ctx, k, o, opts...)
}
func TestOneClockAndFrozenConfig(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	calls := 0
	f.r.Clock = func() time.Time { calls++; return f.now }
	f.r.Reader = hookReader{Reader: f.c, hook: func() { require.NoError(t, f.r.Config.Update([]byte(reloadConfig))) }}
	result, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 2*time.Minute, result.RequeueAfter)
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 2}}, f.install.requests)
}
func TestNotFoundFreshLeavesNoSeries(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	fresh := setup(t, testConfig())
	f.r.Reader = fresh.c
	f.reconcile(t, "p")
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, fam := range families {
		for _, sample := range fam.Metric {
			for _, label := range sample.Label {
				require.NotEqual(t, "pool", label.GetName())
			}
		}
	}
}
func TestPollerErrorBlocksAllPools(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), infra("q"))
	f.queues.result.Err = errors.New("offline")
	f.reconcile(t, "p")
	f.reconcile(t, "q")
	require.Empty(t, f.install.requests)
	require.Equal(t, 1, f.queues.calls)
}

type sweepFunc func(context.Context, pipeline.PoolKey) pipeline.SweepResult

func (f sweepFunc) Sweep(ctx context.Context, k pipeline.PoolKey) pipeline.SweepResult {
	return f(ctx, k)
}

type queueFunc func(context.Context) pipeline.PollerStatus

func (f queueFunc) Pollers(ctx context.Context) pipeline.PollerStatus { return f(ctx) }

type installFunc func(context.Context, pipeline.InstallRequest) (pipeline.InstallResult, error)

func (f installFunc) Install(ctx context.Context, r pipeline.InstallRequest) (pipeline.InstallResult, error) {
	return f(ctx, r)
}

type deadlineClient struct {
	client.Client
	check  func(context.Context, string) error
	prefix string
}

func (c deadlineClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.check(ctx, c.prefix+"get"); err != nil {
		return err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
func (c deadlineClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.check(ctx, c.prefix+list.GetObjectKind().GroupVersionKind().Kind); err != nil {
		return err
	}
	return c.Client.List(ctx, list, opts...)
}
func (c deadlineClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if err := c.check(ctx, "patch"); err != nil {
		return err
	}
	return c.Client.Patch(ctx, obj, p, opts...)
}
func TestExternalDeadlinesAndCancellation(t *testing.T) {
	for _, stage := range []string{"get", "AgentList", "fresh-get", "fresh-AgentList", "sweep", "pollers", "install", "patch"} {
		t.Run(stage, func(t *testing.T) {
			cfg := testConfig()
			cfg.ReconcileTimeout = 20 * time.Millisecond
			f := setup(t, cfg, infra("p"))
			var deadline time.Time
			var calls []string
			check := func(ctx context.Context, call string) error {
				d, ok := ctx.Deadline()
				require.True(t, ok)
				if deadline.IsZero() {
					deadline = d
				}
				require.Equal(t, deadline, d)
				calls = append(calls, call)
				if call == stage {
					<-ctx.Done()
					return ctx.Err()
				}
				return ctx.Err()
			}
			f.r.Client = deadlineClient{Client: f.c, check: check}
			f.r.Reader = deadlineClient{Client: f.c, check: check, prefix: "fresh-"}
			f.r.Sweeper = sweepFunc(func(ctx context.Context, _ pipeline.PoolKey) pipeline.SweepResult {
				return pipeline.SweepResult{Err: check(ctx, "sweep")}
			})
			f.r.Queues = queueFunc(func(ctx context.Context) pipeline.PollerStatus {
				return pipeline.PollerStatus{Activity: 1, Workflow: 1, Segment: 1, Err: check(ctx, "pollers")}
			})
			f.r.Installer = installFunc(func(ctx context.Context, r pipeline.InstallRequest) (pipeline.InstallResult, error) {
				if err := check(ctx, "install"); err != nil {
					return pipeline.InstallResult{Requested: r.Count}, errors.Join(pipeline.ErrAmbiguous, err)
				}
				return pipeline.InstallResult{Requested: r.Count}, pipeline.ErrAmbiguous
			})
			started := time.Now()
			_, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(started), time.Second)
			require.Contains(t, calls, stage)
		})
	}
	// A cooperative cancellation during the sweep prevents further external calls.
	f := setup(t, testConfig(), infra("p"))
	ctx, cancel := context.WithCancel(context.Background())
	f.r.Sweeper = sweepFunc(func(context.Context, pipeline.PoolKey) pipeline.SweepResult { cancel(); return pipeline.SweepResult{} })
	_, err := f.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, f.queues.calls)
	require.Empty(t, f.install.requests)
}

type logEntry struct {
	msg    string
	values []any
	err    error
}
type captureSink struct{ entries *[]logEntry }

func (s captureSink) Init(logr.RuntimeInfo)  {}
func (s captureSink) Enabled(level int) bool { return level == 0 }
func (s captureSink) Info(_ int, msg string, values ...any) {
	*s.entries = append(*s.entries, logEntry{msg: msg, values: values})
}
func (s captureSink) Error(err error, msg string, values ...any) {
	*s.entries = append(*s.entries, logEntry{msg: msg, values: values, err: err})
}
func (s captureSink) WithName(string) logr.LogSink   { return s }
func (s captureSink) WithValues(...any) logr.LogSink { return s }
func TestSafeChangeOnlyLogging(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), agent("one", "p"), agent("two", "p"))
	var entries []logEntry
	ctx := log.IntoContext(context.Background(), logr.New(captureSink{entries: &entries}))
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))}
	for range 3 {
		_, err := f.r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	require.Empty(t, entries)
	f.sweep.result.Err = errors.New("https://user:secret@example.invalid?bearer=secret")
	for range 3 {
		_, err := f.r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	require.Len(t, entries, 1)
	f.sweep.result.Err = nil
	require.NoError(t, f.c.Delete(ctx, agent("one", "p")))
	f.install.err = fmt.Errorf("secret response: %w", pipeline.ErrRejected)
	_, err := f.r.Reconcile(ctx, req)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotEmpty(t, entry.msg)
		require.True(t, unicode.IsUpper([]rune(entry.msg)[0]))
		require.False(t, strings.HasSuffix(entry.msg, "."))
		require.Zero(t, len(entry.values)%2)
		require.NotContains(t, fmt.Sprint(entry), "secret")
	}
	// Return text is also logged by controller-runtime, so retain causes via
	// Unwrap instead of rendering arbitrary external strings in Error().
	f = setup(t, testConfig(), infra("p"))
	f.install.err = errors.New("https://user:secret@example.invalid")
	_, err = f.r.Reconcile(ctx, req)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
	require.ErrorContains(t, errors.Unwrap(err), "secret")
}
func TestClosedReloadChannel(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	reload := make(chan event.GenericEvent, 1)
	reload <- event.GenericEvent{Object: infra("p")}
	close(reload)
	src := source.Channel(reload, handler.EnqueueRequestsFromMapFunc(f.r.allPools))
	ctx := t.Context()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()
	require.NoError(t, src.Start(ctx, queue))
	require.Eventually(t, func() bool { return queue.Len() == 1 }, time.Second, time.Millisecond)
	req, quit := queue.Get()
	require.False(t, quit)
	queue.Done(req)
	require.Equal(t, client.ObjectKeyFromObject(infra("p")), req.NamespacedName)
	// Closing the source cannot generate zero-value events or repeat the reload.
	require.Never(t, func() bool { return queue.Len() > 0 }, 20*time.Millisecond, time.Millisecond)
}

func TestWatchBurstCannotSpendAgainstCachedSweep(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.reconcile(t, "p")
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	require.Equal(t, 2.0, metric(t, f.reg, "herzi_pool_need", nil))
	f.now = f.now.Add(11 * time.Second)
	f.sweep.result.Slots = []pipeline.SlotRun{{Slot: 1, Status: pipeline.StatusRunning, Started: f.now}, {Slot: 2, Status: pipeline.StatusRunning, Started: f.now}}
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	// A later fresh sweep can prove a short order left real unmet demand.
	f.sweep.result.Slots = f.sweep.result.Slots[:1]
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 2)
	require.Equal(t, 1, f.install.requests[1].Count)
}

func TestPartialOutcomeCountsExactlyOnce(t *testing.T) {
	cfg := testConfig()
	cfg.Defaults.MinFree = 3
	f := setup(t, cfg, infra("p"))
	f.install.result = &pipeline.InstallResult{Requested: 3, Started: []int{1}, Busy: []int{2}, Failed: []int{3}}
	f.reconcile(t, "p")
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_machines_ordered_total", nil))
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_orders_total", map[string]string{"result": "short"}))
	for i := 1; i <= 3; i++ {
		f.sweep.result.Slots = append(f.sweep.result.Slots, pipeline.SlotRun{Slot: i, Status: pipeline.StatusRunning, Started: f.now})
	}
	f.reconcile(t, "p")
	require.Len(t, f.install.requests, 1)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_machines_ordered_total", nil))
}
func TestUnmanageForgetsEvenWhenAPIUnavailable(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.reconcile(t, "p")
	require.NoError(t, f.r.Config.Update([]byte(strings.Replace(reloadConfig, "pools: {p: {}, q: {}}", "pools: {q: {}}", 1))))
	f.r.Client = readClient{Client: f.c, reader: failingReader{Reader: f.c, get: true}}
	_, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(infra("p"))})
	require.Error(t, err)
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, fam := range families {
		for _, sample := range fam.Metric {
			for _, l := range sample.Label {
				require.NotEqual(t, "pool", l.GetName())
			}
		}
	}
}
func TestBlockedMetricsAndSingleObservations(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	f.sweep.result.Err = errors.New("offline")
	f.reconcile(t, "p")
	require.Equal(t, 2.0, metric(t, f.reg, "herzi_pool_target", nil))
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_blocked", map[string]string{"reason": "sweep_error"}))
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, fam := range families {
		if fam.GetName() == "herzi_sweep_duration_seconds" || fam.GetName() == "herzi_reconcile_duration_seconds" {
			require.EqualValues(t, 1, fam.Metric[0].Histogram.GetSampleCount())
		}
	}
}
