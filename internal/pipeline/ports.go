package pipeline

import (
	"context"
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

// RunSweeper returns the latest execution of every pool slot.
type RunSweeper interface {
	Sweep(ctx context.Context, pool PoolKey) SweepResult
}

type QueueChecker interface {
	Pollers(ctx context.Context) PollerStatus
}

type InstallResult struct {
	Requested int
	Started   []int
	Busy      []int
	Failed    []int // Failed slots remain uncertain until the next sweep.
	HTTPCode  int
	Duration  time.Duration // time spent in the HTTP call, set even when it failed
}

type Installer interface {
	// Install wraps ErrAmbiguous when callers must block the pool; other errors are definite failures.
	Install(ctx context.Context, req InstallRequest) (InstallResult, error)
}

type RemoveRequest struct {
	Agent  string
	Server string
}

type RemoveResult struct {
	HTTPCode       int
	AlreadyRunning bool // 409: a run for this server is in flight, possibly not ours
	WorkflowID     string
	RunID          string
	Duration       time.Duration
}

type Remover interface {
	// Remove wraps ErrAmbiguous when a run may have started and ErrRejected when nothing did; it never retries.
	Remove(ctx context.Context, req RemoveRequest) (RemoveResult, error)
}

// RemoveRuns describes a server's remove and uninstall ids; Invalidate discards the remove id observed before a
// removal request.
type RemoveRuns interface {
	Describe(ctx context.Context, server string) (RemoveRun, error)
	DescribeUninstall(ctx context.Context, server string) (RemoveRun, error)
	Invalidate(server string)
}

// SweepInvalidator discards observations made before an order attempt.
type SweepInvalidator interface{ Invalidate(pool PoolKey) }

// StatefulSweeper checks acquisition only when durable failure evidence needs proof.
type StatefulSweeper interface {
	SweepWithState(context.Context, PoolKey, State, config.PoolConfig) SweepResult
}
