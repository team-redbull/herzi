package temporalx

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/ids"
	"github.com/team-redbull/herzi/internal/pipeline"
	enumspb "go.temporal.io/api/enums/v1"
)

// Sweeper implements Part 2 "Growth protocol": "describe ... <1..maxSlots>"
// and "Any other error = the pool is BLOCKED this pass". It is safe for
// concurrent use. Calls for the same pool share an in-flight sweep.
type Sweeper struct {
	opts     Options
	mu       sync.Mutex
	pools    map[pipeline.PoolKey]*poolSweep
	failures failureLRU
}

type poolSweep struct {
	done     chan struct{}
	finished bool
	at       time.Time
	result   pipeline.SweepResult
}

var _ pipeline.RunSweeper = (*Sweeper)(nil)

// NewSweeper constructs a sweeper with a bounded failure cache.
func NewSweeper(o Options) *Sweeper {
	o = o.defaults()
	return &Sweeper{opts: o, pools: make(map[pipeline.PoolKey]*poolSweep), failures: failureLRU{
		capacity: o.FailureCacheSize, entries: make(map[runKey]*list.Element), order: list.New(),
	}}
}

// Sweep describes the latest execution of each fixed slot ID, without visibility
// lists. Returned slices are copies so callers cannot mutate cached results.
func (s *Sweeper) Sweep(ctx context.Context, pool pipeline.PoolKey) pipeline.SweepResult {
	if err := ctx.Err(); err != nil {
		return pipeline.SweepResult{Err: err}
	}
	s.mu.Lock()
	now := s.opts.Now()
	for key, entry := range s.pools {
		if entry.finished && now.Sub(entry.at) >= s.opts.CacheTTL {
			delete(s.pools, key)
		}
	}
	if entry, ok := s.pools[pool]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return pipeline.SweepResult{Err: ctx.Err()}
		case <-entry.done:
			return cloneSweep(entry.result)
		}
	}
	entry := &poolSweep{done: make(chan struct{})}
	s.pools[pool] = entry
	s.mu.Unlock()

	started := time.Now()
	result := s.sweep(ctx, pool)
	s.mu.Lock()
	entry.at = s.opts.Now()
	result.ObservedAt = entry.at
	entry.result = result
	entry.finished = true
	// A canceled caller must not poison the next reconcile's cache.
	if ctx.Err() != nil && s.pools[pool] == entry {
		delete(s.pools, pool)
	}
	close(entry.done)
	s.mu.Unlock()
	if s.opts.Observe != nil {
		s.opts.Observe(string(pool), time.Since(started))
	}
	return cloneSweep(result)
}

func cloneSweep(r pipeline.SweepResult) pipeline.SweepResult {
	r.Slots = slices.Clone(r.Slots)
	return r
}

func (s *Sweeper) sweep(ctx context.Context, pool pipeline.PoolKey) pipeline.SweepResult {
	runs := make([]*pipeline.SlotRun, s.opts.MaxSlots)
	errs := make([]error, s.opts.MaxSlots)
	var next atomic.Int64
	var workers sync.WaitGroup
	for range min(s.opts.Parallelism, s.opts.MaxSlots) {
		workers.Go(func() {
			for ctx.Err() == nil {
				slot := int(next.Add(1))
				if slot > s.opts.MaxSlots {
					return
				}
				id := ids.PoolSlot(s.opts.MCE, string(pool), slot)
				runs[slot-1], errs[slot-1] = s.describe(ctx, id, slot)
			}
		})
	}
	workers.Wait() // No detached RPC goroutines: every worker exits before return.
	result := pipeline.SweepResult{Err: errors.Join(errs...)}
	if err := ctx.Err(); err != nil {
		result.Err = errors.Join(result.Err, err)
	}
	for _, run := range runs {
		if run != nil {
			result.Slots = append(result.Slots, *run)
		}
	}
	return result
}

func (s *Sweeper) describe(ctx context.Context, id string, slot int) (*pipeline.SlotRun, error) {
	callCtx, cancel := context.WithTimeout(ctx, s.opts.PerCallTimeout)
	response, err := s.opts.Client.DescribeWorkflowExecution(callCtx, id, "")
	cancel()
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("describe slot %s: %w", id, err)
	}
	info := response.GetWorkflowExecutionInfo()
	if info == nil {
		return nil, fmt.Errorf("describe slot %s: missing execution info", id)
	}
	status, err := runStatus(info.GetStatus())
	if err != nil {
		return nil, fmt.Errorf("describe slot %s: %w", id, err)
	}
	run := &pipeline.SlotRun{Slot: slot, Status: status, RunID: info.GetExecution().GetRunId()}
	if info.StartTime != nil {
		run.Started = info.StartTime.AsTime()
	}
	if status != pipeline.StatusRunning && info.CloseTime != nil {
		run.Closed = info.CloseTime.AsTime()
	}
	if status == pipeline.StatusFailed {
		run.FailType = s.failureType(ctx, runKey{id, info.GetExecution().GetRunId()})
	}
	return run, nil
}

func runStatus(status enumspb.WorkflowExecutionStatus) (string, error) {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return pipeline.StatusRunning, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return pipeline.StatusCompleted, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return pipeline.StatusFailed, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return pipeline.StatusTimedOut, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return pipeline.StatusTerminated, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return pipeline.StatusCanceled, nil
	default:
		return "", fmt.Errorf("unknown workflow status %s", status)
	}
}

// failureType implements Part 3 C: "Describe does not return the failure".
// The iterator is lazy, so the deadline must cover HasNext and Next too.
// SweepResult has no warning field; history errors leave the type unknown and
// are retried after the pool TTL rather than cached as a permanent result.
func (s *Sweeper) failureType(ctx context.Context, key runKey) string {
	// Never read an unpinned latest run after describing a failed run: the slot
	// could already have been reused by the route.
	if key.run == "" {
		return pipeline.FailUnknown
	}
	if value, ok := s.failures.get(key); ok {
		return value
	}
	callCtx, cancel := context.WithTimeout(ctx, s.opts.PerCallTimeout)
	defer cancel()
	iter := s.opts.Client.GetWorkflowHistory(callCtx,
		key.workflow,
		key.run,
		false,
		enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)
	if iter == nil {
		return pipeline.FailUnknown
	}
	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return pipeline.FailUnknown
		}
		attrs := event.GetWorkflowExecutionFailedEventAttributes()
		if attrs == nil {
			continue
		}
		value := pipeline.FailUnknown
		for failure := attrs.GetFailure(); failure != nil; failure = failure.GetCause() {
			if app := failure.GetApplicationFailureInfo(); app != nil {
				if app.GetType() != "" {
					value = app.GetType()
				}
				break
			}
		}
		s.failures.put(key, value)
		return value
	}
	return pipeline.FailUnknown
}

type runKey struct{ workflow, run string }
type failureEntry struct {
	key   runKey
	value string
}
type failureLRU struct {
	mu       sync.Mutex
	capacity int
	entries  map[runKey]*list.Element
	order    *list.List
}

func (c *failureLRU) get(key runKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(failureEntry).value, true
	}
	return "", false
}

func (c *failureLRU) put(key runKey, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.Value = failureEntry{key, value}
		c.order.MoveToFront(e)
		return
	}
	c.entries[key] = c.order.PushFront(failureEntry{key, value})
	if c.order.Len() > c.capacity {
		e := c.order.Back()
		delete(c.entries, e.Value.(failureEntry).key)
		c.order.Remove(e)
	}
}

// Invalidate makes every subsequent caller obtain a new observation.
func (s *Sweeper) Invalidate(pool pipeline.PoolKey) {
	s.mu.Lock()
	delete(s.pools, pool)
	s.mu.Unlock()
}

// SweepWithState adds acquisition proof only for pools with capacity or unknown evidence.
func (s *Sweeper) SweepWithState(ctx context.Context, pool pipeline.PoolKey,
	state pipeline.State, p config.PoolConfig) pipeline.SweepResult {
	result := s.Sweep(ctx, pool)
	state = pipeline.MergeState(state, result.Slots, s.opts.Now(), p)
	if state.CapFailAt.IsZero() || !state.CapFailAt.After(state.LastOKAt) {
		return result
	}
	for i := range result.Slots {
		r := &result.Slots[i]
		if r.Status == pipeline.StatusRunning && s.opts.Now().Sub(r.Started) >= p.DrawGrace {
			r.Acquired = s.acquired(ctx, runKey{ids.PoolSlot(s.opts.MCE, string(pool), r.Slot), r.RunID})
		}
	}
	return result
}

func (s *Sweeper) acquired(ctx context.Context, key runKey) bool {
	if key.run == "" {
		return false
	}
	cacheKey := runKey{key.workflow + "/acquired", key.run}
	if _, ok := s.failures.get(cacheKey); ok {
		return true
	}
	callCtx, cancel := context.WithTimeout(ctx, s.opts.PerCallTimeout)
	defer cancel()
	iter := s.opts.Client.GetWorkflowHistory(callCtx, key.workflow, key.run, false,
		enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	if iter == nil {
		return false
	}
	scheduled := make(map[int64]bool)
	// Bound both history size and elapsed time, including non-cooperative test iterators.
	for n := 0; n < 100000 && callCtx.Err() == nil && iter.HasNext(); n++ {
		e, err := iter.Next()
		if err != nil {
			return false
		}
		if a := e.GetActivityTaskScheduledEventAttributes(); a != nil {
			scheduled[e.GetEventId()] = a.GetActivityType().GetName() == "acquire_servers"
		}
		if a := e.GetActivityTaskCompletedEventAttributes(); a != nil && scheduled[a.GetScheduledEventId()] {
			s.failures.put(cacheKey, "yes")
			return true
		}
	}
	return false
}
