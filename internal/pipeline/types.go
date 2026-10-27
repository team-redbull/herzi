// Package pipeline is herzi's decision logic as four PURE stages:
//
//	Attribute -> Validate -> Decide -> Plan
//
// Attribute interprets RawSnapshot; Validate returns a pool-scoped Verdict; Decide returns an Intent;
// Plan bounds a Grow by the open-run headroom and the HTTP API limit.
//
// Pure means: no clients, no goroutines, no time.Now(), no global state. Time and settings are passed
// in. Everything risky about the controller's behaviour is a function in this package and is tested
// by tables and properties without a cluster. See docs/design/herzi-design.md (Parts 1-3).
package pipeline

import (
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
)

// PoolKey is the InfraEnv name inside the configured agent namespace.
type PoolKey string

// Settings is what the pipeline needs from configuration. config.Config satisfies it.
type Settings interface {
	// Manages reports whether the pool is listed (opt-in) in the configuration.
	Manages(name string) bool
	// ForPool returns the defaults with this pool's overrides merged in field by field.
	ForPool(name string) config.PoolConfig
}

// Run statuses as reported by the sweep. NotFound means the slot id was never started (a free slot).
const (
	StatusRunning    = "Running"
	StatusCompleted  = "Completed"
	StatusFailed     = "Failed"
	StatusTimedOut   = "TimedOut"
	StatusTerminated = "Terminated"
	StatusCanceled   = "Canceled"
	StatusNotFound   = "NotFound"
)

// Failure types raised by the install-server workflow (shared/exceptions.py in the workflows repo).
const (
	FailUnknown            = "unknown"
	FailServerNotAvailable = "ServerNotAvailableError" // server-scan had nothing
	FailServerReserved     = "ServerReservedError"     // every candidate was locked by another run
)

// IsCapacityFailure reports whether a failure type means "the inventory has no free machine".
func IsCapacityFailure(failType string) bool {
	return failType == FailServerNotAvailable || failType == FailServerReserved
}

// SlotRun is the latest execution of one slot id, as seen by Describe.
type SlotRun struct {
	RunID    string
	Acquired bool
	Slot     int
	Status   string
	Started  time.Time
	Closed   time.Time // zero while Running
	FailType string    // ApplicationError type for Failed; FailUnknown if history is unavailable
}

// SweepResult is the outcome of describing every slot of one pool.
// Err != nil means at least one Describe failed for a reason other than NotFound: the pool must be
// blocked for the pass, never treated as "zero open runs".
type SweepResult struct {
	ObservedAt time.Time
	Slots      []SlotRun // NotFound slots are omitted
	Err        error
}

// PollerStatus is the number of pollers on each queue a run needs. Err != nil means unknown.
type PollerStatus struct {
	Activity int // server-lifecycle activity queue of this hub
	Workflow int // install-server workflow queue
	Segment  int // segment-lifecycle activity queue
	Err      error
}

// ReadError records a failed read; Pool == "" means hub-wide.
type ReadError struct {
	Pool PoolKey
	What string
	Err  error
}

// RawSnapshot is everything one reconcile gathered, before any interpretation.
type RawSnapshot struct {
	Warnings  []Warning
	InfraEnvs []kube.InfraEnv
	NodePools []kube.NodePool
	Agents    []kube.Agent
	Sweep     map[PoolKey]SweepResult
	Pollers   PollerStatus
	ReadErrs  []ReadError
	At        time.Time // the single clock of the pass
}

// Problem blocks ONE pool. Warning never blocks anything.
type Problem struct {
	Pool PoolKey
	Msg  string
}
type Warning struct{ Object, Msg string }

// PoolFacts is the interpreted state of one pool: the input of Validate and Decide.
type PoolFacts struct {
	State        State
	Key          PoolKey
	Managed      bool
	Paused       bool
	BlockedUntil time.Time // from the InfraEnv annotation; zero when absent
	Deleting     bool      // the InfraEnv is being deleted
	EarlyBound   bool      // the InfraEnv has spec.clusterRef set

	Demand      int // sum of eligible NodePool replicas
	Assigned    int // Agents bound to a cluster
	Claimable   int // unbound, approved, connected, known-unbound, no agentMachineRef, not deleting
	Settling    int // free but not claimable yet, last state change < SettlingMax
	StuckAgents int // same as Settling but older: NOT counted, alert

	Open   []SlotRun // every Running slot
	Closed []SlotRun // closed slots (Completed/Failed/TimedOut/...): probe evidence

	Problems []Problem // pool-scoped: block this pool only
	Warnings []Warning // hub-scoped: metric + log, never block
}

// Verdict is Validate's answer.
type Verdict struct {
	Key     PoolKey
	Blocked bool
	Reasons []string
}

// Mode is how much Grow asks for.
type Mode int

const (
	Full  Mode = iota // order everything that is missing
	Probe             // order exactly one machine to test whether hardware is back
)

// Intent is what Decide wants. It is sealed: only the types below implement it.
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

// InstallRequest is one POST to the install-server route. Count is 1..20 (a hard API limit).
type InstallRequest struct {
	Key   PoolKey
	Count int
}

// APIMaxCount is the install-server route's per-request limit (router.py: count le=20).
const APIMaxCount = 20
