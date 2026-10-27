// Package temporalx reads Temporal workflow and worker state for pool decisions.
package temporalx

import (
	"context"
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

type WorkflowClient interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	GetWorkflowHistory(ctx context.Context, workflowID, runID string, isLongPoll bool, filterType enumspb.HistoryEventFilterType) client.HistoryEventIterator
	DescribeTaskQueue(ctx context.Context, taskqueue string, taskqueueType enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error)
}

var _ WorkflowClient = client.Client(nil)

// Options requires a non-nil Client; non-positive limits and durations use defaults.
type Options struct {
	Client WorkflowClient
	// MCE must already be canonical, including the route's ocp4- prefix.
	MCE              string
	MaxSlots         int
	Parallelism      int
	PerCallTimeout   time.Duration
	CacheTTL         time.Duration
	FailureCacheSize int
	ActivityQueue    string
	WorkflowQueue    string
	SegmentQueue     string
	// Observe receives a pool or queue name and must return promptly under concurrent calls.
	Observe func(pool string, d time.Duration)
	// Now must be concurrency-safe; it controls cache age, while RPC timing uses the real clock.
	Now func() time.Time
}

func (o Options) defaults() Options {
	if o.MaxSlots <= 0 {
		o.MaxSlots = 100
	}
	if o.Parallelism <= 0 {
		o.Parallelism = 10
	}
	if o.PerCallTimeout <= 0 {
		o.PerCallTimeout = 3 * time.Second
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 10 * time.Second
	}
	if o.FailureCacheSize <= 0 {
		o.FailureCacheSize = 1024
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func IsNotFound(err error) bool {
	var missing *serviceerror.NotFound
	return errors.As(err, &missing)
}
