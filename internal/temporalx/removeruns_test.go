package temporalx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/pipeline"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRemoveRunsDescribesTheServerID(t *testing.T) {
	started, closed := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC()
	var calls atomic.Int32
	status := enumspb.WORKFLOW_EXECUTION_STATUS_FAILED
	fake := &fakeClient{describe: func(_ context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		require.Equal(t, "remove-server-from-mce-ocp-dell-x", id)
		require.Empty(t, run)
		r := execution(status, "r1")
		r.WorkflowExecutionInfo.StartTime = timestamppb.New(started)
		r.WorkflowExecutionInfo.CloseTime = timestamppb.New(closed)
		return r, nil
	}}
	now := time.Unix(1000, 0)
	runs := NewRemoveRuns(Options{Client: fake, CacheTTL: 10 * time.Second, Now: func() time.Time { return now }})
	got, err := runs.Describe(context.Background(), "ocp-dell-X")
	require.NoError(t, err)
	require.Equal(t, pipeline.RemoveRun{Status: pipeline.StatusFailed, Started: started, Closed: closed}, got)

	status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	got, _ = runs.Describe(context.Background(), "ocp-dell-x")
	require.Equal(t, pipeline.StatusFailed, got.Status, "cached within the TTL")
	require.EqualValues(t, 1, calls.Load())

	runs.Invalidate("OCP-DELL-X")
	got, _ = runs.Describe(context.Background(), "ocp-dell-x")
	require.Equal(t, pipeline.RemoveRun{Status: pipeline.StatusRunning, Started: started}, got, "a running run has no close time")
	require.EqualValues(t, 2, calls.Load())

	now = now.Add(10 * time.Second)
	_, _ = runs.Describe(context.Background(), "ocp-dell-x")
	require.EqualValues(t, 3, calls.Load(), "expired after the TTL")
}

func TestRemoveRunsNotFoundAndErrors(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	fake := &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, serviceerror.NewUnavailable("down")
		}
		return nil, serviceerror.NewNotFound("never ran")
	}}
	runs := NewRemoveRuns(Options{Client: fake})
	got, err := runs.Describe(context.Background(), "srv")
	require.NoError(t, err)
	require.Equal(t, pipeline.StatusNotFound, got.Status)

	runs.Invalidate("srv")
	fail.Store(true)
	_, err = runs.Describe(context.Background(), "srv")
	require.ErrorContains(t, err, "remove-server-from-mce-srv")
	_, err = runs.Describe(context.Background(), "srv")
	require.Error(t, err)
	require.EqualValues(t, 3, calls.Load(), "errors are never cached")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runs.Describe(ctx, "srv")
	require.ErrorIs(t, err, context.Canceled)

	missing := NewRemoveRuns(Options{Client: &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		return &workflowservice.DescribeWorkflowExecutionResponse{}, nil
	}}})
	_, err = missing.Describe(context.Background(), "srv")
	require.ErrorContains(t, err, "missing execution info")
}

func TestRemoveRunsShareAnInFlightDescribe(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	fake := &fakeClient{describe: func(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
		calls.Add(1)
		<-release
		return execution(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "r"), nil
	}}
	runs := NewRemoveRuns(Options{Client: fake, PerCallTimeout: time.Minute})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			got, err := runs.Describe(context.Background(), "srv")
			require.NoError(t, err)
			require.Equal(t, pipeline.StatusRunning, got.Status)
		})
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestPollersIncludeTheRemoveQueueOnlyWhenSet(t *testing.T) {
	seen := map[string]enumspb.TaskQueueType{}
	var mu sync.Mutex
	fake := &fakeClient{queue: func(_ context.Context, name string, kind enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		mu.Lock()
		seen[name] = kind
		mu.Unlock()
		if name == "remove" {
			return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{}, {}}}, nil
		}
		return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{}}}, nil
	}}
	o := Options{Client: fake, WorkflowQueue: "w", ActivityQueue: "a", SegmentQueue: "s"}
	require.Equal(t, pipeline.PollerStatus{Workflow: 1, Activity: 1, Segment: 1}, NewQueues(o).Pollers(context.Background()))
	require.Len(t, seen, 3)
	o.RemoveQueue = "remove"
	require.Equal(t, pipeline.PollerStatus{Workflow: 1, Activity: 1, Segment: 1, Remove: 2}, NewQueues(o).Pollers(context.Background()))
	require.Equal(t, enumspb.TASK_QUEUE_TYPE_WORKFLOW, seen["remove"])

	fake.queue = func(_ context.Context, name string, _ enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
		if name == "remove" {
			return nil, errors.New("denied")
		}
		return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{}}}, nil
	}
	got := NewQueues(o).Pollers(context.Background())
	require.ErrorContains(t, got.Err, "describe queue remove")
	require.Equal(t, 1, got.Activity, "successful counts are kept on error")
}
