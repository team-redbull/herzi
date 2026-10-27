package pipeline

import (
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

// Hold reasons are stable strings for callers reporting a decision.
const (
	HoldAtOrAboveTarget = "at or above target"
	HoldProbeRunning    = "probe running"
	HoldProbeWait       = "probe wait"
)

// Decide implements design Part 2, "Stage 3 — Decide (pure)":
// "running ALWAYS subtracted" and "PROOF ... STARTED AFTER the last ... failure".
func Decide(f PoolFacts, p config.PoolConfig, now time.Time) Intent {
	_, target := Target(f.Demand, p)
	total := f.Assigned + f.Claimable + f.Settling
	running := len(f.Open)
	need := target - total - running
	if need <= 0 {
		return Hold{Key: f.Key, Reason: HoldAtOrAboveTarget}
	}
	if failing, lastFail := hardwareLooksMissing(f, p, now); failing {
		if hasPostFailureRun(f.Open, lastFail) {
			return Hold{Key: f.Key, Reason: HoldProbeRunning}
		}
		if now.Before(MergeState(f.State, append(append([]SlotRun{}, f.Open...), f.Closed...), now, p).ProbeNotBefore) {
			return Hold{Key: f.Key, Reason: HoldProbeWait}
		}
		return Grow{Key: f.Key, Need: 1, Mode: Probe}
	}
	return Grow{Key: f.Key, Need: need, Mode: Full}
}

func hardwareLooksMissing(f PoolFacts, p config.PoolConfig, now time.Time) (bool, time.Time) {
	s := MergeState(f.State, append(append([]SlotRun{}, f.Open...), f.Closed...), now, p)
	last := s.CapFailAt
	if !last.After(s.LastOKAt) {
		last = time.Time{}
	}
	if len(s.OtherFails) >= 3 {
		if t := s.OtherFails[len(s.OtherFails)-1].ClosedAt; t.After(last) {
			last = t
		}
	}
	return !last.IsZero(), last
}

func hasPostFailureRun(runs []SlotRun, failure time.Time) bool {
	for _, r := range runs {
		if r.Started.After(failure) {
			return true
		}
	}
	return false
}

// Target implements the add-only target buffer with integer half-up rounding.
func Target(demand int, p config.PoolConfig) (buffer, target int) {
	buffer = max(p.MinFree, (p.HighPct*demand+50)/100)
	return buffer, demand + buffer
}
