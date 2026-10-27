package pipeline

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/kube"
)

func TestAttributeInfraEnvs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		ie      kube.InfraEnv
		paused  bool
		managed bool
		blocked bool
		warn    bool
	}{
		{name: "managed", ie: kube.InfraEnv{Name: "a"}, managed: true},
		{name: "unmanaged", ie: kube.InfraEnv{Name: "unmanaged"}},
		{name: "deleting retained", ie: kube.InfraEnv{Name: "a", Deleting: true}, managed: true},
		{name: "early bound", ie: kube.InfraEnv{Name: "a", EarlyBound: true}, managed: true},
		{name: "annotation pause", ie: kube.InfraEnv{Name: "a", Annotations: map[string]string{kube.AnnotationPaused: "true"}}, paused: true, managed: true},
		{name: "annotation case sensitive", ie: kube.InfraEnv{Name: "a", Annotations: map[string]string{kube.AnnotationPaused: "True"}}, managed: true},
		{name: "config pause", ie: kube.InfraEnv{Name: "a"}, paused: true, managed: true},
		{name: "block annotation", ie: kube.InfraEnv{Name: "a", Annotations: map[string]string{kube.AnnotationBlockedUntil: clockTime().Add(time.Minute).Format(time.RFC3339)}}, blocked: true, managed: true},
		{name: "bad block annotation", ie: kube.InfraEnv{Name: "a", Annotations: map[string]string{kube.AnnotationBlockedUntil: "invalid"}}, warn: true, managed: true},
		{name: "empty block annotation", ie: kube.InfraEnv{Name: "a", Annotations: map[string]string{kube.AnnotationBlockedUntil: ""}}, warn: true, managed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := snapshot()
			raw.InfraEnvs = []kube.InfraEnv{tt.ie}
			p := poolConfig()
			p.Paused = tt.name == "config pause"
			all := Attribute(raw, settings{p})
			equal(t, len(all), 1)
			f := all[PoolKey(tt.ie.Name)]
			equal(t, f.Managed, tt.managed)
			equal(t, f.Paused, tt.paused)
			equal(t, f.Deleting, tt.ie.Deleting)
			equal(t, f.EarlyBound, tt.ie.EarlyBound)
			equal(t, !f.BlockedUntil.IsZero(), tt.blocked)
			equal(t, len(f.Warnings) > 0, tt.warn)
		})
	}
	equal(t, len(Attribute(RawSnapshot{}, settings{poolConfig()})), 0)
}

func TestAttributeNodePoolRules(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*kube.NodePool)
		demand  int
		warning string
		problem bool
	}{
		{name: "eligible", change: func(*kube.NodePool) {}, demand: 3},
		{name: "other platform skipped before deletion", change: func(n *kube.NodePool) { n.PlatformType = "AWS"; n.Deleting = true }},
		{name: "deleting before paused", change: func(n *kube.NodePool) { n.Deleting = true; n.Paused = true }, warning: "deleting"},
		{name: "paused before selector", change: func(n *kube.NodePool) { n.Paused = true; n.Selector.MatchLabels = nil }, warning: "paused"},
		{name: "missing selector", change: func(n *kube.NodePool) { n.Selector.MatchLabels = nil }, warning: "not tied to an InfraEnv"},
		{name: "other label only", change: func(n *kube.NodePool) { n.Selector.MatchLabels = map[string]string{"other": "a"} }, warning: "not tied to an InfraEnv"},
		{name: "unknown before complex selector", change: func(n *kube.NodePool) {
			n.Selector.MatchLabels[kube.LabelInfraEnv] = "unknown"
			n.Selector.HasExpressions = true
		}, warning: "unknown InfraEnv"},
		{name: "empty label value", change: func(n *kube.NodePool) { n.Selector.MatchLabels[kube.LabelInfraEnv] = "" }, warning: "unknown InfraEnv"},
		{name: "extra label", change: func(n *kube.NodePool) { n.Selector.MatchLabels["other"] = "x" }, problem: true},
		{name: "expressions", change: func(n *kube.NodePool) { n.Selector.HasExpressions = true }, problem: true},
		{name: "nil replicas", change: func(n *kube.NodePool) { n.Replicas = nil; n.AutoScaling = true }, problem: true},
		{name: "zero replicas", change: func(n *kube.NodePool) { v := int32(0); n.Replicas = &v }},
		{name: "autoscaling flag with replicas", change: func(n *kube.NodePool) { n.AutoScaling = true }, demand: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := snapshot()
			np := nodePool()
			tt.change(&np)
			raw.NodePools = []kube.NodePool{np}
			all := Attribute(raw, settings{poolConfig()})
			f := all["a"]
			equal(t, f.Demand, tt.demand)
			equal(t, len(f.Problems) > 0, tt.problem)
			equal(t, len(all["b"].Problems), 0)
			if tt.warning != "" {
				if len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0].Msg, tt.warning) {
					t.Fatalf("warnings: %+v", f.Warnings)
				}
			} else {
				equal(t, len(f.Warnings), 0)
			}
			equal(t, Validate(all["b"], raw, poolConfig()).Blocked, false)
		})
	}
	raw := snapshot()
	raw.NodePools = []kube.NodePool{nodePool(), nodePool()}
	equal(t, Attribute(raw, settings{poolConfig()})["a"].Demand, 6)
}

func TestAttributeAgentRules(t *testing.T) {
	for _, tt := range []struct {
		name                                 string
		change                               func(*kube.Agent)
		assigned, claimable, settling, stuck int
		warn                                 bool
	}{
		{name: "claimable", change: func(*kube.Agent) {}, claimable: 1},
		{name: "bound before deleting", change: func(a *kube.Agent) { a.Bound = true; a.Deleting = true }, assigned: 1},
		{name: "deleting", change: func(a *kube.Agent) { a.Deleting = true }},
		{name: "unapproved", change: func(a *kube.Agent) { a.Approved = false }, settling: 1},
		{name: "disconnected", change: func(a *kube.Agent) { a.Connected = false }, settling: 1},
		{name: "other state", change: func(a *kube.Agent) { a.State = "discovering" }, settling: 1},
		{name: "stale agentMachineRef", change: func(a *kube.Agent) { a.Labels[kube.LabelAgentMachineRef] = "stale" }, settling: 1},
		{name: "empty agentMachineRef still present", change: func(a *kube.Agent) { a.Labels[kube.LabelAgentMachineRef] = "" }, settling: 1},
		{name: "settling just before boundary", change: func(a *kube.Agent) {
			a.Approved = false
			a.LastStateChange = clockTime().Add(-time.Hour + time.Nanosecond)
		}, settling: 1},
		{name: "stuck at boundary", change: func(a *kube.Agent) { a.Approved = false; a.LastStateChange = clockTime().Add(-time.Hour) }, stuck: 1},
		{name: "stuck after boundary", change: func(a *kube.Agent) { a.Approved = false; a.LastStateChange = clockTime().Add(-2 * time.Hour) }, stuck: 1},
		{name: "claimable regardless age", change: func(a *kube.Agent) { a.LastStateChange = time.Time{} }, claimable: 1},
		{name: "missing label", change: func(a *kube.Agent) { a.Labels = nil }, warn: true},
		{name: "unknown pool even if bound", change: func(a *kube.Agent) { a.Labels[kube.LabelInfraEnv] = "unknown"; a.Bound = true }, warn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := snapshot()
			a := agent()
			tt.change(&a)
			raw.Agents = []kube.Agent{a}
			all := Attribute(raw, settings{poolConfig()})
			f := all["a"]
			equal(t, []int{f.Assigned, f.Claimable, f.Settling, f.StuckAgents}, []int{tt.assigned, tt.claimable, tt.settling, tt.stuck})
			equal(t, len(f.Warnings) > 0, tt.warn)
			equal(t, Validate(all["b"], raw, poolConfig()).Blocked, false)
		})
	}
}

func TestAttributeSlotsAndDeterminism(t *testing.T) {
	raw := snapshot()
	raw.Sweep["a"] = SweepResult{Slots: []SlotRun{{Slot: 9, Status: StatusNotFound}, {Slot: 5, Status: StatusRunning}, {Slot: 4, Status: StatusFailed}, {Slot: 3, Status: StatusCompleted}, {Slot: 2, Status: StatusTimedOut}, {Slot: 1, Status: StatusTerminated}, {Slot: 6, Status: StatusCanceled}, {Slot: 7, Status: StatusRunning}}}
	raw.NodePools = []kube.NodePool{nodePool(), nodePool()}
	raw.NodePools[0].Name = "z"
	raw.NodePools[0].Replicas = nil
	raw.NodePools[1].Replicas = nil
	raw.Agents = []kube.Agent{{Name: "z"}, {Name: "a"}}
	all := Attribute(raw, settings{poolConfig()})
	f := all["a"]
	equal(t, len(f.Open), 2)
	equal(t, len(f.Closed), 5)
	equal(t, f.Open[0].Slot, 5)
	equal(t, f.Closed[0].Slot, 1)
	for range 10 {
		equal(t, Attribute(raw, settings{poolConfig()}), all)
	}
	// Slice inputs are never sorted or changed in place.
	equal(t, raw.Sweep["a"].Slots[0].Slot, 9)
	raw.NodePools[0], raw.NodePools[1] = raw.NodePools[1], raw.NodePools[0]
	raw.Agents[0], raw.Agents[1] = raw.Agents[1], raw.Agents[0]
	if !reflect.DeepEqual(all, Attribute(raw, settings{poolConfig()})) {
		t.Fatal("input ordering changed facts")
	}
}

// These exercise the pure portion of the Part 2 invariants. Temporal Describe,
// the id/Python contract, HTTP acknowledgements and controller config snapshots
// belong to other work packages; here the supplied snapshot is authoritative.
func TestSnapshotSafetyInvariants(t *testing.T) {
	t.Run("unclear POST annotation survives reconstruction and expires at boundary", func(t *testing.T) {
		raw := snapshot()
		raw.InfraEnvs[0].Annotations = map[string]string{kube.AnnotationBlockedUntil: raw.At.Add(5 * time.Minute).Format(time.RFC3339)}
		for range 2 {
			facts := Attribute(raw, settings{poolConfig()})
			equal(t, Validate(facts["a"], raw, poolConfig()).Blocked, true)
		}
		raw.At = raw.At.Add(5 * time.Minute)
		// Three latest runs exist even if the caller lost the order acknowledgement.
		raw.Sweep["a"] = SweepResult{Slots: []SlotRun{running(1), running(1), running(1)}}
		facts := Attribute(raw, settings{poolConfig()})
		equal(t, Validate(facts["a"], raw, poolConfig()).Blocked, false)
		equal(t, Decide(facts["a"], poolConfig(), raw.At), Hold{Key: "a", Reason: HoldAtOrAboveTarget})
	})
	t.Run("all Running slots count irrespective of age or Agent overlap", func(t *testing.T) {
		raw := snapshot()
		raw.Agents = []kube.Agent{agent()}
		raw.Sweep["a"] = SweepResult{Slots: []SlotRun{running(180)}}
		f := Attribute(raw, settings{poolConfig()})["a"]
		equal(t, f.Claimable, 1)
		equal(t, len(f.Open), 1)
		equal(t, Decide(f, poolConfig(), raw.At), Hold{Key: "a", Reason: HoldAtOrAboveTarget})
	})
	t.Run("settling stale reference remains supply", func(t *testing.T) {
		raw := snapshot()
		a := agent()
		a.Labels[kube.LabelAgentMachineRef] = "stale"
		raw.Agents = []kube.Agent{agent(), a}
		f := Attribute(raw, settings{poolConfig()})["a"]
		equal(t, f.Settling, 1)
		equal(t, Decide(f, poolConfig(), raw.At), Hold{Key: "a", Reason: HoldAtOrAboveTarget})
	})
	t.Run("frozen inputs reused without mutation", func(t *testing.T) {
		raw := snapshot()
		raw.NodePools = []kube.NodePool{nodePool()}
		raw.Agents = []kube.Agent{agent()}
		s := settings{poolConfig()}
		before := Attribute(raw, s)
		for range 2 {
			f := Attribute(raw, s)["a"]
			_ = Validate(f, raw, s.p)
			if g, ok := Decide(f, s.p, raw.At).(Grow); ok {
				_ = Plan(g, s.p, f)
			}
		}
		equal(t, Attribute(raw, s), before)
	})
}
