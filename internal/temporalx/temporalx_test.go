package temporalx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/ids"
	"github.com/team-redbull/herzi/internal/pipeline"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeClient struct {
	describe func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	history  func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator
	queue    func(context.Context, string, enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error)
}

func (f *fakeClient) DescribeWorkflowExecution(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if f.describe == nil {
		return nil, serviceerror.NewNotFound("free")
	}
	return f.describe(ctx, id, run)
}
func (f *fakeClient) GetWorkflowHistory(ctx context.Context, id, run string, long bool, filter enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	if f.history == nil {
		return &fakeHistory{}
	}
	return f.history(ctx, id, run, long, filter)
}
func (f *fakeClient) DescribeTaskQueue(ctx context.Context, name string, kind enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	return f.queue(ctx, name, kind)
}

type fakeHistory struct {
	events []*historypb.HistoryEvent
	err    error
	next   func() (*historypb.HistoryEvent, error)
}

func (h *fakeHistory) HasNext() bool { return h.next != nil || h.err != nil || len(h.events) > 0 }
func (h *fakeHistory) Next() (*historypb.HistoryEvent, error) {
	if h.next != nil {
		return h.next()
	}
	if h.err != nil {
		err := h.err
		h.err = nil
		return nil, err
	}
	e := h.events[0]
	h.events = h.events[1:]
	return e, nil
}

func execution(status enumspb.WorkflowExecutionStatus, run string) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
		Status: status, Execution: &commonpb.WorkflowExecution{RunId: run},
	}}
}
func failedEvent(f *failurepb.Failure) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{
		WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{Failure: f},
	}}
}
func application(kind string) *failurepb.Failure {
	return &failurepb.Failure{FailureInfo: &failurepb.Failure_ApplicationFailureInfo{
		ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{Type: kind},
	}}
}

func TestStatusMappingAndIDs(t *testing.T) {
	statuses := []struct {
		sdk  enumspb.WorkflowExecutionStatus
		want string
	}{
		{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, pipeline.StatusRunning},
		{enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, pipeline.StatusCompleted},
		{enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, pipeline.StatusFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, pipeline.StatusTimedOut},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, pipeline.StatusTerminated},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, pipeline.StatusCanceled},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, pipeline.StatusRunning},
	}
	started := time.Date(2026, 10, 26, 1, 2, 3, 0, time.UTC)
	closed := started.Add(time.Hour)
	var calls atomic.Int64
	f := &fakeClient{describe: func(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		assert.Empty(t, run, "Describe must use latest run")
		slot, err := strconv.Atoi(id[strings.LastIndex(id, "_")+1:])
		assert.NoError(t, err)
		assert.Equal(t, "install-server-ocp4-hub_pool_"+strconv.Itoa(slot), id)
		r := execution(statuses[slot-1].sdk, "run")
		r.WorkflowExecutionInfo.StartTime = timestamppb.New(started)
		r.WorkflowExecutionInfo.CloseTime = timestamppb.New(closed)
		return r, nil
	}}
	s := NewSweeper(Options{Client: f, MCE: "ocp4-hub", MaxSlots: len(statuses)})
	r := s.Sweep(context.Background(), "pool")
	require.NoError(t, r.Err)
	require.Len(t, r.Slots, len(statuses))
	assert.EqualValues(t, len(statuses), calls.Load())
	for i, tc := range statuses {
		assert.Equal(t, i+1, r.Slots[i].Slot)
		assert.Equal(t, tc.want, r.Slots[i].Status)
		assert.Equal(t, started, r.Slots[i].Started)
		if tc.want == pipeline.StatusRunning {
			assert.True(t, r.Slots[i].Closed.IsZero())
		} else {
			assert.Equal(t, closed, r.Slots[i].Closed)
		}
	}
}

func TestNotFoundAndPartialErrors(t *testing.T) {
	errOne := errors.New("unavailable")
	errTwo := errors.New("permission denied")
	f := &fakeClient{describe: func(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		switch id {
		case ids.PoolSlot("mce", "pool", 1):
			return nil, fmt.Errorf("wrapped: %w", serviceerror.NewNotFound("free"))
		case ids.PoolSlot("mce", "pool", 2):
			return nil, errOne
		case ids.PoolSlot("mce", "pool", 4):
			return nil, errTwo
		default:
			return execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "r"), nil
		}
	}}
	r := NewSweeper(Options{Client: f, MCE: "mce", MaxSlots: 5}).Sweep(context.Background(), "pool")
	require.ErrorIs(t, r.Err, errOne)
	require.ErrorIs(t, r.Err, errTwo)
	assert.Contains(t, r.Err.Error(), ids.PoolSlot("mce", "pool", 2))
	assert.Contains(t, r.Err.Error(), ids.PoolSlot("mce", "pool", 4))
	require.Len(t, r.Slots, 2)
	assert.Equal(t, 3, r.Slots[0].Slot)
	assert.Equal(t, 5, r.Slots[1].Slot)
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errOne))
	r = NewSweeper(Options{Client: &fakeClient{}}).Sweep(context.Background(), "pool")
	assert.NoError(t, r.Err)
	assert.Empty(t, r.Slots)
}

func TestParallelismBoundAndDefaultSlots(t *testing.T) {
	var active, peak, calls atomic.Int64
	entered := make(chan struct{}, 100)
	release := make(chan struct{})
	f := &fakeClient{describe: func(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return nil, serviceerror.NewNotFound("free")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	s := NewSweeper(Options{Client: f, Parallelism: 3})
	done := make(chan pipeline.SweepResult, 1)
	go func() { done <- s.Sweep(context.Background(), "pool") }()
	for range 3 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	assert.EqualValues(t, 3, active.Load())
	close(release)
	select {
	case r := <-done:
		assert.NoError(t, r.Err)
	case <-time.After(time.Second):
		t.Fatal("sweep did not finish")
	}
	assert.EqualValues(t, 3, peak.Load())
	assert.EqualValues(t, 100, calls.Load())
	assert.Zero(t, active.Load())
}

func TestPerCallTimeoutAndParentDeadline(t *testing.T) {
	for _, parentShorter := range []bool{false, true} {
		t.Run(strconv.FormatBool(parentShorter), func(t *testing.T) {
			ctx := context.Background()
			timeout := 20 * time.Millisecond
			if parentShorter {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
				timeout = time.Second
			}
			f := &fakeClient{describe: func(callCtx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
				deadline, ok := callCtx.Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), timeout)
				<-callCtx.Done()
				return nil, callCtx.Err()
			}}
			started := time.Now()
			r := NewSweeper(Options{Client: f, MaxSlots: 1, PerCallTimeout: timeout}).Sweep(ctx, "pool")
			assert.ErrorIs(t, r.Err, context.DeadlineExceeded)
			assert.Less(t, time.Since(started), time.Second)
		})
	}
}

func TestCancellationJoinsWorkers(t *testing.T) {
	var active, calls atomic.Int64
	entered := make(chan struct{}, 4)
	f := &fakeClient{describe: func(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s := NewSweeper(Options{Client: f, Parallelism: 4})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan pipeline.SweepResult, 1)
	go func() { done <- s.Sweep(ctx, "pool") }()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case r := <-done:
		assert.ErrorIs(t, r.Err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked sweep workers")
	}
	assert.Zero(t, active.Load(), "all RPC workers must have exited before Sweep returns")
	assert.EqualValues(t, 4, calls.Load(), "unstarted slots must not be dispatched")
	assert.ErrorIs(t, s.Sweep(ctx, "pool").Err, context.Canceled)
	// A canceled sweep is not reused on a subsequent reconcile.
	f.describe = nil
	assert.NoError(t, s.Sweep(context.Background(), "pool").Err)
}

func TestFailureTypes(t *testing.T) {
	activity := &failurepb.Failure{FailureInfo: &failurepb.Failure_ActivityFailureInfo{ActivityFailureInfo: &failurepb.ActivityFailureInfo{}}, Cause: application(pipeline.FailServerNotAvailable)}
	for _, tc := range []struct {
		name   string
		events []*historypb.HistoryEvent
		err    error
		want   string
	}{
		{"activity cause", []*historypb.HistoryEvent{failedEvent(activity)}, nil, pipeline.FailServerNotAvailable},
		{"two wrappers", []*historypb.HistoryEvent{failedEvent(&failurepb.Failure{Cause: activity})}, nil, pipeline.FailServerNotAvailable},
		{"direct application", []*historypb.HistoryEvent{failedEvent(application(pipeline.FailServerReserved))}, nil, pipeline.FailServerReserved},
		{"no application info", []*historypb.HistoryEvent{failedEvent(&failurepb.Failure{Cause: &failurepb.Failure{}})}, nil, pipeline.FailUnknown},
		{"nil failure", []*historypb.HistoryEvent{failedEvent(nil)}, nil, pipeline.FailUnknown},
		{"empty application type", []*historypb.HistoryEvent{failedEvent(application(""))}, nil, pipeline.FailUnknown},
		{"history error", nil, errors.New("read failed"), pipeline.FailUnknown},
		{"empty history", nil, nil, pipeline.FailUnknown},
		{"unrelated event", []*historypb.HistoryEvent{{}}, nil, pipeline.FailUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeClient{
				describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
					return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "pinned-run"), nil
				},
				history: func(ctx context.Context, id, run string, long bool, filter enumspb.HistoryEventFilterType) client.HistoryEventIterator {
					assert.Equal(t, ids.PoolSlot("mce", "pool", 1), id)
					assert.Equal(t, "pinned-run", run)
					assert.False(t, long)
					assert.Equal(t, enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT, filter)
					_, ok := ctx.Deadline()
					assert.True(t, ok)
					return &fakeHistory{events: tc.events, err: tc.err}
				},
			}
			r := NewSweeper(Options{Client: f, MCE: "mce", MaxSlots: 1}).Sweep(context.Background(), "pool")
			require.NoError(t, r.Err)
			require.Len(t, r.Slots, 1)
			assert.Equal(t, tc.want, r.Slots[0].FailType)
		})
	}
}

func TestHistoryTimeoutAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelParent), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var active atomic.Int64
			f := &fakeClient{
				describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
					return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "r"), nil
				},
				history: func(callCtx context.Context, _ string, _ string, _ bool, _ enumspb.HistoryEventFilterType) client.HistoryEventIterator {
					return &fakeHistory{next: func() (*historypb.HistoryEvent, error) {
						active.Add(1)
						defer active.Add(-1)
						if cancelParent {
							cancel()
						}
						<-callCtx.Done()
						return nil, callCtx.Err()
					}}
				},
			}
			started := time.Now()
			r := NewSweeper(Options{Client: f, MaxSlots: 1, PerCallTimeout: 20 * time.Millisecond}).Sweep(ctx, "pool")
			if cancelParent {
				assert.ErrorIs(t, r.Err, context.Canceled)
			} else {
				assert.NoError(t, r.Err)
			}
			require.Len(t, r.Slots, 1)
			assert.Equal(t, pipeline.FailUnknown, r.Slots[0].FailType)
			assert.Zero(t, active.Load())
			assert.Less(t, time.Since(started), time.Second)
		})
	}
}

func TestFailureLRUAndRunID(t *testing.T) {
	now := time.Time{}
	runID := "r1"
	historyCalls := 0
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, runID), nil
		},
		history: func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			historyCalls++
			return &fakeHistory{events: []*historypb.HistoryEvent{failedEvent(application("type-" + runID))}}
		},
	}
	s := NewSweeper(Options{Client: f, MaxSlots: 1, FailureCacheSize: 2, Now: func() time.Time { return now }})
	for i, tc := range []struct {
		run   string
		calls int
	}{
		{"r1", 1}, {"r1", 1}, {"r2", 2}, {"r1", 2}, {"r3", 3}, {"r2", 4},
	} {
		runID = tc.run
		now = now.Add(10 * time.Second)
		r := s.Sweep(context.Background(), "pool")
		require.NoError(t, r.Err)
		require.Len(t, r.Slots, 1)
		assert.Equal(t, "type-"+tc.run, r.Slots[0].FailType)
		assert.Equal(t, tc.calls, historyCalls, "step %d", i)
	}
	assert.Len(t, s.failures.entries, 2)
	// Exercise replacing an existing LRU entry without increasing its size.
	s.failures.put(runKey{ids.PoolSlot("", "pool", 1), "r2"}, "updated")
	value, ok := s.failures.get(runKey{ids.PoolSlot("", "pool", 1), "r2"})
	assert.True(t, ok)
	assert.Equal(t, "updated", value)
}

func TestHistoryErrorsAreRetried(t *testing.T) {
	now := time.Now()
	calls := 0
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "r"), nil
		},
		history: func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			calls++
			if calls == 1 {
				return &fakeHistory{err: errors.New("temporary")}
			}
			return &fakeHistory{events: []*historypb.HistoryEvent{failedEvent(application("recovered"))}}
		},
	}
	s := NewSweeper(Options{Client: f, MaxSlots: 1, Now: func() time.Time { return now }})
	r := s.Sweep(context.Background(), "pool")
	require.NoError(t, r.Err)
	assert.Equal(t, pipeline.FailUnknown, r.Slots[0].FailType)
	now = now.Add(10 * time.Second)
	r = s.Sweep(context.Background(), "pool")
	require.NoError(t, r.Err)
	assert.Equal(t, "recovered", r.Slots[0].FailType)
	assert.Equal(t, 2, calls)
}

func TestFailureCacheSeparatesWorkflowIDsAndCachesUnknownTypes(t *testing.T) {
	now := time.Now()
	var calls atomic.Int64
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "same-run-id"), nil
		},
		history: func(_ context.Context, id, _ string, _ bool, _ enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			calls.Add(1)
			if id == ids.PoolSlot("mce", "unknown", 1) {
				return &fakeHistory{events: []*historypb.HistoryEvent{failedEvent(&failurepb.Failure{})}}
			}
			return &fakeHistory{events: []*historypb.HistoryEvent{failedEvent(application("known-type"))}}
		},
	}
	s := NewSweeper(Options{Client: f, MCE: "mce", MaxSlots: 1, Now: func() time.Time { return now }})
	for range 2 {
		unknown := s.Sweep(context.Background(), "unknown")
		known := s.Sweep(context.Background(), "known")
		require.NoError(t, unknown.Err)
		require.NoError(t, known.Err)
		assert.Equal(t, pipeline.FailUnknown, unknown.Slots[0].FailType)
		assert.Equal(t, "known-type", known.Slots[0].FailType)
		now = now.Add(10 * time.Second)
	}
	assert.EqualValues(t, 2, calls.Load(), "unknown types from a successful read are cached too")
}

func TestNilHistoryIteratorIsBestEffort(t *testing.T) {
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			return execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "r"), nil
		},
		history: func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			return nil
		},
	}
	r := NewSweeper(Options{Client: f, MaxSlots: 1}).Sweep(context.Background(), "pool")
	require.NoError(t, r.Err)
	require.Len(t, r.Slots, 1)
	assert.Equal(t, pipeline.FailUnknown, r.Slots[0].FailType)
}

func TestCachedDescribeErrorRemainsUnknown(t *testing.T) {
	broken := errors.New("describe unavailable")
	var calls atomic.Int64
	f := &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		return nil, broken
	}}
	s := NewSweeper(Options{Client: f, MaxSlots: 1})
	for range 2 {
		r := s.Sweep(context.Background(), "pool")
		assert.ErrorIs(t, r.Err, broken)
		assert.Empty(t, r.Slots)
	}
	assert.EqualValues(t, 1, calls.Load())
}

func TestSweepCacheTTLAndCopies(t *testing.T) {
	now := time.Time{}
	var calls atomic.Int64
	observations := 0
	f := &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		return execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "r"), nil
	}}
	s := NewSweeper(Options{Client: f, MaxSlots: 1, Now: func() time.Time { return now }, Observe: func(pool string, d time.Duration) {
		assert.Contains(t, []string{"pool", "other"}, pool)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		observations++
	}})
	r := s.Sweep(context.Background(), "pool")
	require.NoError(t, r.Err)
	r.Slots[0].Status = "corrupted"
	now = now.Add(10*time.Second - time.Nanosecond)
	r = s.Sweep(context.Background(), "pool")
	assert.Equal(t, pipeline.StatusRunning, r.Slots[0].Status)
	assert.EqualValues(t, 1, calls.Load())
	now = now.Add(time.Nanosecond)
	r = s.Sweep(context.Background(), "pool")
	assert.NoError(t, r.Err)
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, 2, observations)
	// A different pool has an independent cache.
	s.Sweep(context.Background(), "other")
	assert.EqualValues(t, 3, calls.Load())
}

func TestConcurrentCacheAndCanceledWaiter(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	f := &fakeClient{describe: func(ctx context.Context, _ string, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		close(entered)
		<-release
		return execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "r"), nil
	}}
	s := NewSweeper(Options{Client: f, MaxSlots: 1})
	var wg sync.WaitGroup
	wg.Go(func() { assert.NoError(t, s.Sweep(context.Background(), "pool").Err) })
	<-entered
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := s.Sweep(waitCtx, "pool")
	assert.ErrorIs(t, r.Err, context.DeadlineExceeded)
	for range 10 {
		wg.Go(func() {
			r := s.Sweep(context.Background(), "pool")
			assert.NoError(t, r.Err)
			assert.Len(t, r.Slots, 1)
		})
	}
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())
}

func TestMalformedDescriptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *workflowservice.DescribeWorkflowExecutionResponse
		wantErr  bool
	}{
		{"nil", nil, true}, {"missing info", &workflowservice.DescribeWorkflowExecutionResponse{}, true},
		{"unspecified", execution(enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, "r"), true},
		{"future status", execution(enumspb.WorkflowExecutionStatus(99), "r"), true},
		{"failed without run", execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
				return tc.response, nil
			}, history: func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
				t.Error("must not read unpinned history")
				return nil
			}}
			r := NewSweeper(Options{Client: f, MaxSlots: 1}).Sweep(context.Background(), "pool")
			assert.Equal(t, tc.wantErr, r.Err != nil)
		})
	}
}

func TestQueues(t *testing.T) {
	broken := errors.New("queue unavailable")
	for _, tc := range []struct {
		name   string
		counts [3]int
		errs   [3]error
	}{
		{"success", [3]int{2, 3, 4}, [3]error{}},
		{"zero activity", [3]int{2, 0, 4}, [3]error{}},
		{"zero workflow", [3]int{0, 3, 4}, [3]error{}},
		{"zero segment", [3]int{2, 3, 0}, [3]error{}},
		{"activity error", [3]int{2, 0, 4}, [3]error{nil, broken, nil}},
		{"workflow error", [3]int{0, 3, 4}, [3]error{broken, nil, nil}},
		{"segment error", [3]int{2, 3, 0}, [3]error{nil, nil, broken}},
		{"all errors", [3]int{}, [3]error{broken, broken, broken}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := []string{"install-server-workflow", "server-lifecycle-activity-ocp4-hub", "segment-lifecycle-activity"}
			kinds := []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY, enumspb.TASK_QUEUE_TYPE_ACTIVITY}
			calls := 0
			observed := []string{}
			f := &fakeClient{queue: func(ctx context.Context, name string, kind enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
				i := calls
				calls++
				assert.Equal(t, names[i], name)
				assert.Equal(t, kinds[i], kind)
				deadline, ok := ctx.Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), 3*time.Second)
				if tc.errs[i] != nil {
					return nil, tc.errs[i]
				}
				return &workflowservice.DescribeTaskQueueResponse{Pollers: make([]*taskqueuepb.PollerInfo, tc.counts[i])}, nil
			}}
			q := NewQueues(Options{Client: f, WorkflowQueue: names[0], ActivityQueue: names[1], SegmentQueue: names[2], Observe: func(name string, d time.Duration) { observed = append(observed, name) }})
			r := q.Pollers(context.Background())
			assert.Equal(t, tc.counts, [3]int{r.Workflow, r.Activity, r.Segment})
			assert.Equal(t, 3, calls)
			assert.Equal(t, names, observed)
			if tc.errs != [3]error{} {
				assert.ErrorIs(t, r.Err, broken)
			} else {
				assert.NoError(t, r.Err)
			}
		})
	}
}

func TestQueueTimeoutCancellationAndNilResponse(t *testing.T) {
	f := &fakeClient{queue: func(ctx context.Context, _ string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	q := NewQueues(Options{Client: f, PerCallTimeout: 10 * time.Millisecond})
	started := time.Now()
	r := q.Pollers(context.Background())
	assert.ErrorIs(t, r.Err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, q.Pollers(ctx).Err, context.Canceled)
	f.queue = func(context.Context, string, enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		return nil, nil
	}
	assert.Error(t, q.Pollers(context.Background()).Err)
}
