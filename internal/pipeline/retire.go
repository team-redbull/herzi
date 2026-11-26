package pipeline

import (
	"fmt"
	"time"

	"github.com/team-redbull/herzi/internal/kube"
	"k8s.io/apimachinery/pkg/labels"
)

// RetireStep is what an Agent's own state asks of the retirer, before any hub observation.
type RetireStep int

const (
	RetireNothing RetireStep = iota
	RetireLabel
	RetireCandidate
)

// Retirement labels a bound Agent whatever pause, ignore or dry run say: a missed label cannot be repaired.
func Retirement(a kube.Agent) RetireStep {
	switch {
	case a.Deleting:
		return RetireNothing
	case a.Bound && !a.EverInstalled():
		return RetireLabel
	case a.Bound || !a.EverInstalled():
		return RetireNothing
	}
	return RetireCandidate
}

// Retirement states; also the state label of herzi_retiring_agents. Blocked needs a person; paused and dry_run
// are chosen by one; waiting and removing resolve on their own.
const (
	RetireBlocked  = "blocked"
	RetirePaused   = "paused"
	RetireDryRun   = "dry_run"
	RetireWaiting  = "waiting"
	RetireRemoving = "removing"
)

// Wait reasons are stable and safe to log.
const (
	WaitNotRetiring = "not retiring"
	WaitHeld        = "held by an AgentMachine"
	WaitNoServer    = "no bmh label"
	WaitPoolUnknown = "pool unknown"
	WaitNodePools   = "nodepools unknown"
	WaitUnguarded   = "unguarded nodepool"
	WaitNoWorkers   = "no workers"
	WaitPaused      = "pool paused"
	WaitDescribe    = "describe failed"
	WaitRunning     = "removal running"
	WaitBackoff     = "retry backoff"
	WaitCap         = "removal cap"
	WaitRecheck     = "recheck failed"
	WaitRejected    = "rejected by the API"
	WaitDryRun      = "dry run"
)

// RemoveRun is the latest execution of a server's remove id; the zero value means none exists.
type RemoveRun struct {
	Status  string
	Started time.Time
	Closed  time.Time
}

type RemoveInput struct {
	Agent        kube.Agent
	NodePools    []kube.NodePool // every NodePool on the hub
	NodePoolsErr error
	PoolErr      error // the Agent's InfraEnv could not be read
	Paused       bool
	Pollers      PollerStatus
	Run          RemoveRun
	RunErr       error
	OpenRemovals int
	OpenErr      error
	MaxOpen      int
	RetryAfter   time.Duration
	Now          time.Time
}

// RemoveDecision.Server is set only when the removal may be requested now.
type RemoveDecision struct {
	Server string
	State  string
	Reason string
	Object string // the NodePool that could still claim the Agent
}

// DecideRemove fails closed: anything unknown waits.
func DecideRemove(in RemoveInput) RemoveDecision {
	a := in.Agent
	wait := func(state, reason string) RemoveDecision { return RemoveDecision{State: state, Reason: reason} }
	switch {
	case Retirement(a) != RetireCandidate:
		return wait("", WaitNotRetiring)
	case a.HeldByMachine():
		return wait(RetireBlocked, WaitHeld)
	case a.ServerName() == "":
		return wait(RetireBlocked, WaitNoServer)
	case in.PoolErr != nil:
		return wait(RetireBlocked, WaitPoolUnknown)
	case in.NodePoolsErr != nil:
		return wait(RetireBlocked, WaitNodePools)
	}
	nodePool, err := Unguarded(a, in.NodePools)
	if err != nil {
		return wait(RetireBlocked, WaitNodePools)
	}
	if nodePool != "" {
		d := wait(RetireBlocked, WaitUnguarded)
		d.Object = nodePool
		return d
	}
	p := in.Pollers
	if p.Err != nil || p.Remove == 0 || p.Activity == 0 || p.Segment == 0 {
		return wait(RetireBlocked, WaitNoWorkers)
	}
	if in.Paused {
		return wait(RetirePaused, WaitPaused)
	}
	if in.RunErr != nil {
		return wait(RetireWaiting, WaitDescribe)
	}
	switch in.Run.Status {
	case "", StatusNotFound:
	case StatusRunning:
		return wait(RetireRemoving, WaitRunning)
	default:
		if in.Now.Sub(in.Run.Closed) < in.RetryAfter {
			return wait(RetireWaiting, WaitBackoff)
		}
	}
	if in.OpenErr != nil {
		return wait(RetireWaiting, WaitDescribe)
	}
	if in.OpenRemovals >= in.MaxOpen {
		return wait(RetireWaiting, WaitCap)
	}
	return RemoveDecision{Server: a.ServerName(), State: RetireRemoving}
}

// Unguarded names the first Agent-platform NodePool whose selector still matches the Agent: CAPI could claim it there.
func Unguarded(a kube.Agent, nodePools []kube.NodePool) (string, error) {
	set := labels.Set(a.Labels)
	for _, np := range nodePools {
		if np.PlatformType != "Agent" {
			continue
		}
		s, err := np.Selector.LabelSelector()
		if err != nil {
			return "", fmt.Errorf("NodePool %s/%s selector: %w", np.Namespace, np.Name, err)
		}
		if s.Matches(set) {
			return np.Namespace + "/" + np.Name, nil
		}
	}
	return "", nil
}
