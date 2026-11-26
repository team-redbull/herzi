package temporalx

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/team-redbull/herzi/internal/ids"
	"github.com/team-redbull/herzi/internal/pipeline"
)

// RemoveRuns describes a server's remove-server-from-mce and uninstall-server-from-hc ids; calls for one id share an
// in-flight Describe and its result is reused for CacheTTL. Errors are never cached.
type RemoveRuns struct {
	opts    Options
	mu      sync.Mutex
	entries map[string]*removeEntry
}

type removeEntry struct {
	done     chan struct{}
	finished bool
	at       time.Time
	run      pipeline.RemoveRun
	err      error
}

var _ pipeline.RemoveRuns = (*RemoveRuns)(nil)

func NewRemoveRuns(o Options) *RemoveRuns {
	return &RemoveRuns{opts: o.defaults(), entries: make(map[string]*removeEntry)}
}

// Describe reports StatusNotFound for a server that never had a remove run.
func (r *RemoveRuns) Describe(ctx context.Context, server string) (pipeline.RemoveRun, error) {
	return r.describe(ctx, ids.RemoveServer(server))
}

// DescribeUninstall is Describe for the server's uninstall-server-from-hc id.
func (r *RemoveRuns) DescribeUninstall(ctx context.Context, server string) (pipeline.RemoveRun, error) {
	return r.describe(ctx, ids.UninstallServer(server))
}

func (r *RemoveRuns) describe(ctx context.Context, id string) (pipeline.RemoveRun, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.RemoveRun{}, err
	}
	r.mu.Lock()
	now := r.opts.Now()
	for key, e := range r.entries {
		if e.finished && now.Sub(e.at) >= r.opts.CacheTTL {
			delete(r.entries, key)
		}
	}
	if e, ok := r.entries[id]; ok {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return pipeline.RemoveRun{}, ctx.Err()
		case <-e.done:
			return e.run, e.err
		}
	}
	e := &removeEntry{done: make(chan struct{})}
	r.entries[id] = e
	r.mu.Unlock()

	run := pipeline.RemoveRun{Status: pipeline.StatusNotFound}
	found, err := describeExecution(ctx, r.opts, id)
	if err != nil {
		err = fmt.Errorf("describe %s: %w", id, err)
	} else if found != nil {
		run = pipeline.RemoveRun{Status: found.status, Started: found.started, Closed: found.closed}
	}
	r.mu.Lock()
	e.at, e.run, e.err, e.finished = r.opts.Now(), run, err, true
	if (err != nil || ctx.Err() != nil) && r.entries[id] == e {
		delete(r.entries, id)
	}
	close(e.done)
	r.mu.Unlock()
	return run, err
}

// Invalidate makes the next Describe see a removal requested after the cached one.
func (r *RemoveRuns) Invalidate(server string) {
	r.mu.Lock()
	delete(r.entries, ids.RemoveServer(server))
	r.mu.Unlock()
}
