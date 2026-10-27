package temporalx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/pipeline"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestF1InvalidationAndObservedAt(t *testing.T) {
	var ticks atomic.Int64
	base := time.Now()
	s := NewSweeper(Options{Client: &fakeClient{}, MaxSlots: 1,
		Now: func() time.Time { return base.Add(time.Duration(ticks.Load()) * time.Second) }})
	first := s.Sweep(t.Context(), "p")
	ticks.Store(1)
	cached := s.Sweep(t.Context(), "p")
	require.Equal(t, first.ObservedAt, cached.ObservedAt)
	s.Invalidate("p")
	fresh := s.Sweep(t.Context(), "p")
	require.True(t, fresh.ObservedAt.After(first.ObservedAt))
}

func TestF1ConcurrentInvalidation(t *testing.T) {
	s := NewSweeper(Options{Client: &fakeClient{}, MaxSlots: 2})
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			if i%3 == 0 {
				s.Invalidate("p")
			} else {
				require.NoError(t, s.Sweep(t.Context(), "p").Err)
			}
		})
	}
	wg.Wait()
}

func TestF5FailedHistoryClassifiedUnknown(t *testing.T) {
	now := time.Now()
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			e := execution(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "r")
			e.WorkflowExecutionInfo.CloseTime = timestamppb.New(now.Add(-21 * time.Minute))
			return e, nil
		},
		history: func(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			return &fakeHistory{err: errors.New("history denied")}
		},
	}
	r := NewSweeper(Options{Client: f, MaxSlots: 2}).Sweep(t.Context(), "p")
	require.NoError(t, r.Err)
	require.Len(t, r.Slots, 2)
	for _, run := range r.Slots {
		require.Equal(t, pipeline.FailUnknown, run.FailType)
	}
	facts := pipeline.PoolFacts{Key: "p", Closed: r.Slots}
	require.Equal(t, pipeline.Grow{Key: "p", Need: 1, Mode: pipeline.Probe},
		pipeline.Decide(facts, config.Defaults(), now))
}

func acquisitionEvents(name string) []*historypb.HistoryEvent {
	return []*historypb.HistoryEvent{
		{EventId: 10, Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
			ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
				ActivityType: &commonpb.ActivityType{Name: name}}}},
		{EventId: 12, Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
			ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{ScheduledEventId: 10}}},
	}
}

func TestF6LazyAcquisitionProofAndCache(t *testing.T) {
	now, p := time.Now(), config.Defaults()
	var calls atomic.Int64
	acquired := false
	f := &fakeClient{
		describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
			e := execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "probe")
			e.WorkflowExecutionInfo.StartTime = timestamppb.New(now.Add(-3 * time.Minute))
			return e, nil
		},
		history: func(ctx context.Context, _ string, run string, long bool, filter enumspb.HistoryEventFilterType) client.HistoryEventIterator {
			calls.Add(1)
			require.Equal(t, "probe", run)
			require.False(t, long)
			require.Equal(t, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT, filter)
			_, ok := ctx.Deadline()
			require.True(t, ok)
			if acquired {
				return &fakeHistory{events: acquisitionEvents("acquire_servers")}
			}
			return &fakeHistory{events: acquisitionEvents("resolve_segment")}
		},
	}
	s := NewSweeper(Options{Client: f, MaxSlots: 1, Now: func() time.Time { return now }})
	r := s.SweepWithState(t.Context(), "p", pipeline.State{}, p)
	require.False(t, r.Slots[0].Acquired)
	require.Zero(t, calls.Load())
	state := pipeline.State{CapFailAt: now.Add(-21 * time.Minute)}
	r = s.SweepWithState(t.Context(), "p", state, p)
	require.False(t, r.Slots[0].Acquired)
	require.EqualValues(t, 1, calls.Load())
	acquired = true
	r = s.SweepWithState(t.Context(), "p", state, p)
	require.True(t, r.Slots[0].Acquired)
	r = s.SweepWithState(t.Context(), "p", state, p)
	require.True(t, r.Slots[0].Acquired)
	require.EqualValues(t, 2, calls.Load())
}

func TestF6AcquisitionReadFailuresAndYoungRuns(t *testing.T) {
	now, p := time.Now(), config.Defaults()
	for _, kind := range []string{"missing run", "nil iterator", "read error", "no completed activity", "deadline", "young"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakeClient{
				describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
					e := execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "r")
					if kind == "missing run" {
						e.WorkflowExecutionInfo.Execution.RunId = ""
					}
					age := 3 * time.Minute
					if kind == "young" {
						age = time.Minute
					}
					e.WorkflowExecutionInfo.StartTime = timestamppb.New(now.Add(-age))
					return e, nil
				},
				history: func(ctx context.Context, _ string, _ string, _ bool, _ enumspb.HistoryEventFilterType) client.HistoryEventIterator {
					switch kind {
					case "nil iterator":
						return nil
					case "read error":
						return &fakeHistory{err: errors.New("denied")}
					case "deadline":
						return &fakeHistory{next: func() (*historypb.HistoryEvent, error) { <-ctx.Done(); return nil, ctx.Err() }}
					default:
						return &fakeHistory{events: acquisitionEvents("acquire_servers")[:1]}
					}
				},
			}
			s := NewSweeper(Options{Client: f, MaxSlots: 1, PerCallTimeout: time.Millisecond, Now: func() time.Time { return now }})
			r := s.SweepWithState(t.Context(), "p", pipeline.State{CapFailAt: now.Add(-time.Hour)}, p)
			require.False(t, r.Slots[0].Acquired)
		})
	}
}

func TestF1ConcurrentInvalidationDuringCanceledSweep(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	f := &fakeClient{describe: func(ctx context.Context, _, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return execution(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, "new"), nil
	}}
	s := NewSweeper(Options{Client: f, MaxSlots: 1})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan pipeline.SweepResult, 1)
	go func() { finished <- s.Sweep(ctx, "p") }()
	<-entered
	s.Invalidate("p")
	fresh := s.Sweep(t.Context(), "p")
	require.NoError(t, fresh.Err)
	cancel()
	require.ErrorIs(t, (<-finished).Err, context.Canceled)
	cached := s.Sweep(t.Context(), "p")
	require.NoError(t, cached.Err)
	require.Equal(t, fresh.ObservedAt, cached.ObservedAt)
	require.EqualValues(t, 2, calls.Load())
}
