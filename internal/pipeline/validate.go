package pipeline

import (
	"fmt"
	"slices"

	"github.com/team-redbull/herzi/internal/config"
)

// Validate blocks only the affected pool; blocked verdicts prohibit mutating actions.
func Validate(f PoolFacts, raw RawSnapshot, _ config.PoolConfig) Verdict {
	v := Verdict{Key: f.Key}
	if !f.Managed {
		v.Reasons = append(v.Reasons, "pool is unmanaged")
	}
	if f.Paused {
		v.Reasons = append(v.Reasons, "pool is paused")
	}
	if f.BlockedUntil.After(raw.At) {
		v.Reasons = append(v.Reasons, "pool blocked until "+f.BlockedUntil.Format("2006-01-02T15:04:05Z07:00"))
	}
	if f.Deleting {
		v.Reasons = append(v.Reasons, "InfraEnv is deleting")
	}
	if f.EarlyBound {
		v.Reasons = append(v.Reasons, "InfraEnv is early-bound")
	}
	for _, p := range f.Problems {
		v.Reasons = append(v.Reasons, p.Msg)
	}
	if err := raw.Sweep[f.Key].Err; err != nil {
		v.Reasons = append(v.Reasons, fmt.Sprintf("pool sweep failed: %v", err))
	}
	for _, r := range raw.ReadErrs {
		if r.Pool == "" || r.Pool == f.Key {
			v.Reasons = append(v.Reasons, fmt.Sprintf("read %s failed: %v", r.What, r.Err))
		}
	}
	if raw.Pollers.Err != nil {
		v.Reasons = append(v.Reasons, fmt.Sprintf("poller check failed: %v", raw.Pollers.Err))
	}
	if raw.Pollers.Activity == 0 {
		v.Reasons = append(v.Reasons, "activity queue has no pollers")
	}
	if raw.Pollers.Workflow == 0 {
		v.Reasons = append(v.Reasons, "workflow queue has no pollers")
	}
	if raw.Pollers.Segment == 0 {
		v.Reasons = append(v.Reasons, "segment queue has no pollers")
	}
	slices.Sort(v.Reasons)
	v.Blocked = len(v.Reasons) > 0
	return v
}
