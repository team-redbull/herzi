package pipeline

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/kube"
)

func retiringAgent() kube.Agent {
	a := agent()
	a.Labels[kube.LabelClaimed] = kube.ClaimedValue
	a.Labels[kube.LabelBMH] = "srv-1"
	return a
}

func guardedNodePool() kube.NodePool {
	np := nodePool()
	np.Selector.Expressions = []kube.Requirement{exclusion()}
	return np
}

func removeInput() RemoveInput {
	return RemoveInput{Agent: retiringAgent(), NodePools: []kube.NodePool{guardedNodePool()},
		Pollers: PollerStatus{Activity: 1, Workflow: 1, Segment: 1, Remove: 1}, MaxOpen: 10, RetryAfter: 30 * time.Minute, Now: clockTime()}
}

func TestRetirement(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*kube.Agent)
		want   RetireStep
	}{
		{"free unlabelled", func(*kube.Agent) {}, RetireNothing},
		{"bound unlabelled is labelled", func(a *kube.Agent) { a.Bound = true }, RetireLabel},
		{"bound labelled", func(a *kube.Agent) { a.Bound = true; a.Labels[kube.LabelClaimed] = "true" }, RetireNothing},
		{"released labelled is a candidate", func(a *kube.Agent) { a.Labels[kube.LabelClaimed] = "true" }, RetireCandidate},
		{"stray labelled is a candidate", func(a *kube.Agent) { a.Labels = map[string]string{kube.LabelClaimed: "true"} }, RetireCandidate},
		{"stray bound is labelled", func(a *kube.Agent) { a.Labels = nil; a.Bound = true }, RetireLabel},
		{"deleting bound is left alone", func(a *kube.Agent) { a.Bound = true; a.Deleting = true }, RetireNothing},
		{"deleting labelled is left alone", func(a *kube.Agent) { a.Labels[kube.LabelClaimed] = "true"; a.Deleting = true }, RetireNothing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := agent()
			tt.change(&a)
			equal(t, Retirement(a), tt.want)
		})
	}
}

func TestDecideRemoveGates(t *testing.T) {
	unguarded := nodePool()
	unguarded.Namespace = "tenant"
	noSelector := kube.NodePool{Name: "any", Namespace: "tenant", PlatformType: "Agent"}
	otherPool := nodePool()
	otherPool.Selector.MatchLabels = map[string]string{kube.LabelInfraEnv: "b"}
	aws := kube.NodePool{Name: "aws", PlatformType: "AWS"}
	invalid := guardedNodePool()
	invalid.Selector.Expressions = append(invalid.Selector.Expressions, kube.Requirement{Key: "k", Operator: "In"})
	for _, tt := range []struct {
		name   string
		change func(*RemoveInput)
		state  string
		reason string
		object string
	}{
		{name: "removes", change: func(*RemoveInput) {}, state: RetireRemoving},
		{name: "stray agent removes", change: func(in *RemoveInput) { delete(in.Agent.Labels, kube.LabelInfraEnv) }, state: RetireRemoving},
		{name: "other pools and platforms do not guard", change: func(in *RemoveInput) { in.NodePools = append(in.NodePools, otherPool, aws) }, state: RetireRemoving},
		{name: "bound", change: func(in *RemoveInput) { in.Agent.Bound = true }, reason: WaitNotRetiring},
		{name: "unlabelled", change: func(in *RemoveInput) { delete(in.Agent.Labels, kube.LabelClaimed) }, reason: WaitNotRetiring},
		{name: "deleting", change: func(in *RemoveInput) { in.Agent.Deleting = true }, reason: WaitNotRetiring},
		{name: "held by an AgentMachine", change: func(in *RemoveInput) { in.Agent.Labels[kube.LabelAgentMachineRef] = "m" }, state: RetireBlocked, reason: WaitHeld},
		{name: "no bmh label", change: func(in *RemoveInput) { delete(in.Agent.Labels, kube.LabelBMH) }, state: RetireBlocked, reason: WaitNoServer},
		{name: "empty bmh label", change: func(in *RemoveInput) { in.Agent.Labels[kube.LabelBMH] = "" }, state: RetireBlocked, reason: WaitNoServer},
		{name: "pool unknown", change: func(in *RemoveInput) { in.PoolErr = errors.New("x") }, state: RetireBlocked, reason: WaitPoolUnknown},
		{name: "nodepools unknown", change: func(in *RemoveInput) { in.NodePoolsErr = errors.New("x") }, state: RetireBlocked, reason: WaitNodePools},
		{name: "invalid selector", change: func(in *RemoveInput) { in.NodePools = []kube.NodePool{invalid} }, state: RetireBlocked, reason: WaitNodePools},
		{name: "unguarded nodepool", change: func(in *RemoveInput) { in.NodePools = append(in.NodePools, unguarded) }, state: RetireBlocked, reason: WaitUnguarded, object: "tenant/np"},
		{name: "a selector-less nodepool claims everything", change: func(in *RemoveInput) { in.NodePools = []kube.NodePool{noSelector} }, state: RetireBlocked, reason: WaitUnguarded, object: "tenant/any"},
		{name: "a paused nodepool can resume and claim", change: func(in *RemoveInput) {
			np := unguarded
			np.Paused = true
			in.NodePools = []kube.NodePool{np}
		}, state: RetireBlocked, reason: WaitUnguarded, object: "tenant/np"},
		{name: "poller error", change: func(in *RemoveInput) { in.Pollers.Err = errors.New("x") }, state: RetireBlocked, reason: WaitNoWorkers},
		{name: "no remove worker", change: func(in *RemoveInput) { in.Pollers.Remove = 0 }, state: RetireBlocked, reason: WaitNoWorkers},
		{name: "no activity worker", change: func(in *RemoveInput) { in.Pollers.Activity = 0 }, state: RetireBlocked, reason: WaitNoWorkers},
		{name: "no segment worker", change: func(in *RemoveInput) { in.Pollers.Segment = 0 }, state: RetireBlocked, reason: WaitNoWorkers},
		{name: "install workflow worker is not needed", change: func(in *RemoveInput) { in.Pollers.Workflow = 0 }, state: RetireRemoving},
		{name: "paused", change: func(in *RemoveInput) { in.Paused = true }, state: RetirePaused, reason: WaitPaused},
		{name: "describe failed", change: func(in *RemoveInput) { in.RunErr = errors.New("x") }, state: RetireWaiting, reason: WaitDescribe},
		{name: "uninstall describe failed", change: func(in *RemoveInput) { in.UninstallErr = errors.New("x") }, state: RetireWaiting, reason: WaitDescribe},
		{name: "uninstall running", change: func(in *RemoveInput) { in.Uninstall = RemoveRun{Status: StatusRunning} }, state: RetireWaiting, reason: WaitUninstall},
		{name: "uninstall failed does not hold removal", change: func(in *RemoveInput) {
			in.Uninstall = RemoveRun{Status: StatusFailed, Closed: clockTime().Add(-time.Minute)}
		}, state: RetireRemoving},
		{name: "running, ours or not", change: func(in *RemoveInput) { in.Run = RemoveRun{Status: StatusRunning} }, state: RetireRemoving, reason: WaitRunning},
		{name: "not found", change: func(in *RemoveInput) { in.Run = RemoveRun{Status: StatusNotFound} }, state: RetireRemoving},
		{name: "recent failure backs off", change: func(in *RemoveInput) {
			in.Run = RemoveRun{Status: StatusFailed, Closed: clockTime().Add(-29 * time.Minute)}
		}, state: RetireWaiting, reason: WaitBackoff},
		{name: "recent completion with the agent still present backs off", change: func(in *RemoveInput) {
			in.Run = RemoveRun{Status: StatusCompleted, Closed: clockTime().Add(-time.Minute)}
		}, state: RetireWaiting, reason: WaitBackoff},
		{name: "old failure retries", change: func(in *RemoveInput) {
			in.Run = RemoveRun{Status: StatusFailed, Closed: clockTime().Add(-30 * time.Minute)}
		}, state: RetireRemoving},
		{name: "open count unknown", change: func(in *RemoveInput) { in.OpenErr = errors.New("x") }, state: RetireWaiting, reason: WaitDescribe},
		{name: "cap reached", change: func(in *RemoveInput) { in.OpenRemovals = 10 }, state: RetireWaiting, reason: WaitCap},
		{name: "below cap", change: func(in *RemoveInput) { in.OpenRemovals = 9 }, state: RetireRemoving},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := removeInput()
			tt.change(&in)
			d := DecideRemove(in)
			equal(t, []string{d.State, d.Reason, d.Object}, []string{tt.state, tt.reason, tt.object})
			if tt.reason == "" {
				equal(t, d.Server, "srv-1")
			} else {
				equal(t, d.Server, "")
			}
		})
	}
}

func TestRetiringAgentsAreNeverSupply(t *testing.T) {
	for demand := int32(0); demand <= 30; demand += 3 {
		for retiring := 0; retiring <= 20; retiring += 5 {
			raw := snapshot()
			np := guardedNodePool()
			np.Replicas = &demand
			raw.NodePools = []kube.NodePool{np}
			raw.Agents = []kube.Agent{agent()}
			before := Decide(Attribute(raw, settings{poolConfig()})["a"], poolConfig(), clockTime())
			for i := range retiring {
				a := retiringAgent()
				a.Name = fmt.Sprint("retiring-", i)
				raw.Agents = append(raw.Agents, a)
			}
			f := Attribute(raw, settings{poolConfig()})["a"]
			equal(t, f.Retiring, retiring)
			equal(t, Decide(f, poolConfig(), clockTime()), before)
		}
	}
}
