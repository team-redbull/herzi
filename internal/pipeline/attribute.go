package pipeline

import (
	"cmp"
	"slices"
	"time"

	"github.com/team-redbull/herzi/internal/kube"
)

// Attribute implements design Part 2, "Stage 1 — Attribute (pure)":
// "Open = Running slots; Closed = closed slots" and pool-scoped demand attribution.
//
//nolint:gocyclo // a flat list of the design's attribution rules; each rule has its own test
func Attribute(raw RawSnapshot, s Settings) map[PoolKey]PoolFacts {
	facts := make(map[PoolKey]PoolFacts, len(raw.InfraEnvs))
	var warnings []Warning
	for _, ie := range raw.InfraEnvs {
		f := PoolFacts{Key: PoolKey(ie.Name), Managed: s.Manages(ie.Name), Paused: s.ForPool(ie.Name).Paused || ie.Annotations[kube.AnnotationPaused] == "true", Deleting: ie.Deleting, EarlyBound: ie.EarlyBound}
		if v, ok := ie.Annotations[kube.AnnotationBlockedUntil]; ok {
			var err error
			f.BlockedUntil, err = time.Parse(time.RFC3339, v)
			if err != nil {
				warnings = append(warnings, Warning{Object: ie.Namespace + "/" + ie.Name, Msg: "invalid blocked-until annotation"})
			}
		}
		facts[f.Key] = f
	}
	for _, np := range raw.NodePools {
		object := np.Namespace + "/" + np.Name
		key := PoolKey(np.Selector.MatchLabels[kube.LabelInfraEnv])
		f, known := facts[key]
		msg := ""
		switch {
		case np.PlatformType != "Agent":
			continue
		case np.Deleting:
			msg = "deleting NodePool"
		case np.Paused:
			msg = "paused NodePool"
		case !hasInfraEnvLabel(np.Selector.MatchLabels):
			msg = "not tied to an InfraEnv"
		case !known:
			msg = "unknown InfraEnv"
		case len(np.Selector.MatchLabels) != 1 || np.Selector.HasExpressions:
			f.Problems = append(f.Problems, Problem{Pool: key, Msg: object + ": selector must be only the InfraEnv label"})
		case np.Replicas == nil:
			f.Problems = append(f.Problems, Problem{Pool: key, Msg: object + ": no replicas (autoscaling?)"})
		default:
			f.Demand += int(*np.Replicas)
		}
		if msg != "" {
			warnings = append(warnings, Warning{Object: object, Msg: msg})
		} else {
			facts[key] = f
		}
	}
	for _, a := range raw.Agents {
		key := PoolKey(a.InfraEnvName())
		f, ok := facts[key]
		if key == "" || !ok {
			warnings = append(warnings, Warning{Object: a.Namespace + "/" + a.Name, Msg: "agent not in any pool"})
			continue
		}
		_, held := a.Labels[kube.LabelAgentMachineRef]
		switch {
		case a.Bound:
			f.Assigned++
		case a.Deleting:
			continue
		case a.Approved && a.Connected && a.State == kube.StateKnownUnbound && !held:
			f.Claimable++
		case raw.At.Sub(a.LastStateChange) < s.ForPool(string(key)).SettlingMax:
			f.Settling++
		default:
			f.StuckAgents++
		}
		facts[key] = f
	}
	// ASSUMPTION: the contract has no hub-level return value, so every pool carries
	// the same hub warnings. Consumers should deduplicate warnings when reporting a hub.
	slices.SortFunc(warnings, func(a, b Warning) int {
		return cmp.Or(cmp.Compare(a.Object, b.Object), cmp.Compare(a.Msg, b.Msg))
	})
	keys := make([]PoolKey, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		f := facts[key]
		for _, r := range raw.Sweep[key].Slots {
			switch r.Status {
			case StatusNotFound:
				continue
			case StatusRunning:
				f.Open = append(f.Open, r)
			default:
				f.Closed = append(f.Closed, r)
			}
		}
		slices.SortFunc(f.Open, func(a, b SlotRun) int { return cmp.Compare(a.Slot, b.Slot) })
		slices.SortFunc(f.Closed, func(a, b SlotRun) int { return cmp.Compare(a.Slot, b.Slot) })
		slices.SortFunc(f.Problems, func(a, b Problem) int { return cmp.Compare(a.Msg, b.Msg) })
		f.Warnings = append([]Warning(nil), warnings...)
		facts[key] = f
	}
	return facts
}

func hasInfraEnvLabel(labels map[string]string) bool { _, ok := labels[kube.LabelInfraEnv]; return ok }
