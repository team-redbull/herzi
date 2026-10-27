package pipeline

import (
	"context"

	"github.com/team-redbull/herzi/internal/config"
)

// Ports are the I/O the controller needs. They are defined here (next to the types they return) and
// implemented by internal/temporalx, internal/installer and internal/controller. Tests of the
// controller use fakes of these interfaces.

// RunSweeper describes every slot id of one pool and returns the latest run of each.
type RunSweeper interface {
	Sweep(ctx context.Context, pool PoolKey) SweepResult
}

// QueueChecker reads the poller counts of the queues a run needs.
type QueueChecker interface {
	Pollers(ctx context.Context) PollerStatus
}

// InstallResult is the per-slot report of one POST.
type InstallResult struct {
	Requested int
	Started   []int // slots whose status was "started"
	Busy      []int // "already_running"
	Failed    []int // "failed" (NOT assumed absent: the next sweep decides)
	HTTPCode  int
}

// Installer sends one POST to the install-server route.
type Installer interface {
	// Install returns ErrAmbiguous (wrapped) for a timeout, connection error or 5xx: the caller cannot
	// know whether anything started and must block the pool. Any other error is a definite failure.
	Install(ctx context.Context, req InstallRequest) (InstallResult, error)
}

// SweepInvalidator discards observations made before an order attempt.
type SweepInvalidator interface{ Invalidate(pool PoolKey) }

// StatefulSweeper lazily checks acquisition only when durable failure evidence needs proof.
type StatefulSweeper interface {
	SweepWithState(context.Context, PoolKey, State, config.PoolConfig) SweepResult
}
