// Package temporalx implements the Temporal reads in the design's Part 2,
// "Growth protocol": "never treat unknown as zero".
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

// WorkflowClient is the read-only subset of the SDK used by this package.
type WorkflowClient interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	GetWorkflowHistory(ctx context.Context, workflowID, runID string, isLongPoll bool, filterType enumspb.HistoryEventFilterType) client.HistoryEventIterator
	DescribeTaskQueue(ctx context.Context, taskqueue string, taskqueueType enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error)
}

var _ WorkflowClient = client.Client(nil)

// Options configures Sweeper and Queues; each uses only its relevant fields.
// Non-positive limits and durations use defaults. Client must be non-nil.
type Options struct {
	Client WorkflowClient
	// ASSUMPTION: MCE is already canonical (including the Python route's ocp4- prefix).
	MCE              string
	MaxSlots         int
	Parallelism      int
	PerCallTimeout   time.Duration
	CacheTTL         time.Duration
	FailureCacheSize int
	ActivityQueue    string
	WorkflowQueue    string
	SegmentQueue     string
	// Observe receives the pool name for sweeps and queue name for queue reads.
	// It may be called concurrently and should return promptly.
	Observe func(pool string, d time.Duration)
	// Now must be concurrency-safe. It governs sweep-cache age; RPC deadlines
	// and observed durations use the real clock.
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

// IsNotFound recognizes Temporal NotFound errors, including wrapped errors.
func IsNotFound(err error) bool {
	var missing *serviceerror.NotFound
	return errors.As(err, &missing)
}
