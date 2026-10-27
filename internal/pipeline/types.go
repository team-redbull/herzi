// Package pipeline computes pool decisions from snapshots without I/O or implicit clocks.
package pipeline

import (
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
)

// PoolKey is an InfraEnv name in the configured agent namespace.
type PoolKey string

type Settings interface {
	Manages(name string) bool

	ForPool(name string) config.PoolConfig
}

// StatusNotFound denotes a free workflow slot.
const (
	StatusRunning    = "Running"
	StatusCompleted  = "Completed"
	StatusFailed     = "Failed"
	StatusTimedOut   = "TimedOut"
	StatusTerminated = "Terminated"
	StatusCanceled   = "Canceled"
	StatusNotFound   = "NotFound"
)

const (
	FailUnknown            = "unknown"
	FailServerNotAvailable = "ServerNotAvailableError"
	FailServerReserved     = "ServerReservedError"
)

func IsCapacityFailure(failType string) bool {
	return failType == FailServerNotAvailable || failType == FailServerReserved
}

type SlotRun struct {
	RunID    string
	Acquired bool
	Slot     int
	Status   string
	Started  time.Time
	Closed   time.Time
	FailType string // ApplicationError type; FailUnknown if history is unavailable
}

// SweepResult.Err blocks the pool for the pass; unknown runs must never count as zero.
type SweepResult struct {
	ObservedAt time.Time
	Slots      []SlotRun
	Err        error
}

// PollerStatus.Err means poller counts are unknown.
type PollerStatus struct {
	Activity int
	Workflow int
	Segment  int
	Err      error
}

// ReadError.Pool is empty for hub-wide failures.
type ReadError struct {
	Pool PoolKey
	What string
	Err  error
}

type RawSnapshot struct {
	Warnings  []Warning
	InfraEnvs []kube.InfraEnv
	NodePools []kube.NodePool
	Agents    []kube.Agent
	Sweep     map[PoolKey]SweepResult
	Pollers   PollerStatus
	ReadErrs  []ReadError
	At        time.Time
}

// Problem blocks only its pool; Warning never blocks.
type Problem struct {
	Pool PoolKey
	Msg  string
}
type Warning struct{ Object, Msg string }

type PoolFacts struct {
	State        State
	Key          PoolKey
	Managed      bool
	Paused       bool
	BlockedUntil time.Time
	Deleting     bool
	EarlyBound   bool

	Demand      int
	Assigned    int
	Claimable   int
	Settling    int
	StuckAgents int

	Open   []SlotRun
	Closed []SlotRun

	Problems []Problem
	Warnings []Warning
}

type Verdict struct {
	Key     PoolKey
	Blocked bool
	Reasons []string
}

type Mode int

const (
	Full Mode = iota
	Probe
)

type Intent interface{ isIntent() }

type Grow struct {
	Key  PoolKey
	Need int
	Mode Mode
}
type Hold struct {
	Key    PoolKey
	Reason string
}

func (Grow) isIntent() {}
func (Hold) isIntent() {}

// InstallRequest.Count must be between 1 and APIMaxCount.
type InstallRequest struct {
	Key   PoolKey
	Count int
}

// APIMaxCount must match the install route limit.
const APIMaxCount = 20
