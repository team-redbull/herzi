package pipeline

import (
	"context"

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
}

type Installer interface {
	// Install wraps ErrAmbiguous when callers must block the pool; other errors are definite failures.
	Install(ctx context.Context, req InstallRequest) (InstallResult, error)
}

// SweepInvalidator discards observations made before an order attempt.
type SweepInvalidator interface{ Invalidate(pool PoolKey) }

// StatefulSweeper checks acquisition only when durable failure evidence needs proof.
type StatefulSweeper interface {
	SweepWithState(context.Context, PoolKey, State, config.PoolConfig) SweepResult
}
