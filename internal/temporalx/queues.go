package temporalx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/team-redbull/herzi/internal/pipeline"
	enumspb "go.temporal.io/api/enums/v1"
)

type Queues struct{ opts Options }

var _ pipeline.QueueChecker = (*Queues)(nil)

func NewQueues(o Options) *Queues { return &Queues{opts: o.defaults()} }

// Pollers preserves successful queue counts on error; errors mean unknown, never zero.
func (q *Queues) Pollers(ctx context.Context) pipeline.PollerStatus {
	var result pipeline.PollerStatus
	for _, queue := range []struct {
		name  string
		kind  enumspb.TaskQueueType
		count *int
	}{
		{q.opts.WorkflowQueue, enumspb.TASK_QUEUE_TYPE_WORKFLOW, &result.Workflow},
		{q.opts.ActivityQueue, enumspb.TASK_QUEUE_TYPE_ACTIVITY, &result.Activity},
		{q.opts.SegmentQueue, enumspb.TASK_QUEUE_TYPE_ACTIVITY, &result.Segment},
	} {
		if err := ctx.Err(); err != nil {
			result.Err = errors.Join(result.Err, err)
			break
		}
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, q.opts.PerCallTimeout)
		response, err := q.opts.Client.DescribeTaskQueue(callCtx, queue.name, queue.kind)
		cancel()
		if err != nil {
			result.Err = errors.Join(result.Err, fmt.Errorf("describe queue %s: %w", queue.name, err))
		} else if response == nil {
			result.Err = errors.Join(result.Err, fmt.Errorf("describe queue %s: missing response", queue.name))
		} else {
			*queue.count = len(response.Pollers)
		}
		if q.opts.Observe != nil {
			q.opts.Observe(queue.name, time.Since(started))
		}
	}
	return result
}
