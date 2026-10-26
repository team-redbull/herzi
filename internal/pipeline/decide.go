package pipeline

import (
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

// Hold reasons are stable strings for callers reporting a decision.
const (
	HoldInsideBand   = "inside band"
	HoldProbeRunning = "probe running"
	HoldProbeWait    = "probe wait"
)

// Decide implements design Part 2, "Stage 3 — Decide (pure)":
// "running ALWAYS subtracted" and "PROOF ... STARTED AFTER the last ... failure".
func Decide(f PoolFacts, p config.PoolConfig, now time.Time) Intent {
	low := max(p.MinFree, (p.LowPct*f.Demand+50)/100)
	high := max(low+p.MinBand, (p.HighPct*f.Demand+50)/100)
	target, ceiling := f.Demand+low, f.Demand+high
	total := f.Assigned + f.Claimable + f.Settling
	running := len(f.Open)
	need := target - total - running
	if need <= 0 {
		if total > ceiling && running == 0 && f.Settling == 0 {
			if surplus := min(total-target, f.Claimable); surplus > 0 {
				return WouldRemove{Key: f.Key, Surplus: surplus, Total: total, Target: target, Ceiling: ceiling}
			}
		}
		return Hold{Key: f.Key, Reason: HoldInsideBand}
	}
	if failing, lastFail := hardwareLooksMissing(f, p, now); failing {
		if running > 0 {
			return Hold{Key: f.Key, Reason: HoldProbeRunning}
		}
		if now.Sub(lastFail) < p.ProbeInterval {
			return Hold{Key: f.Key, Reason: HoldProbeWait}
		}
		return Grow{Key: f.Key, Need: 1, Mode: Probe}
	}
	return Grow{Key: f.Key, Need: need, Mode: Full}
}

func hardwareLooksMissing(f PoolFacts, p config.PoolConfig, now time.Time) (bool, time.Time) {
	var capFail, lastSuccess time.Time
	capFound := false
	successFound := false
	for _, r := range f.Closed {
		if (r.Status == StatusFailed || r.Status == StatusTimedOut) && IsCapacityFailure(r.FailType) {
			if !capFound || r.Closed.After(capFail) {
				capFail = r.Closed
				capFound = true
			}
		}
		if r.Status == StatusCompleted && (!successFound || r.Closed.After(lastSuccess)) {
			lastSuccess = r.Closed
			successFound = true
		}
	}
	// ASSUMPTION: "after the latest Completed run" compares close times. The
	// non-capacity streak is independent of capacity evidence; both obey FailureMemory.
	otherCount := 0
	var otherFail time.Time
	for _, r := range f.Closed {
		if r.Status == StatusFailed && !IsCapacityFailure(r.FailType) && (!successFound || r.Closed.After(lastSuccess)) {
			otherCount++
			if otherCount == 1 || r.Closed.After(otherFail) {
				otherFail = r.Closed
			}
		}
	}
	capFailing := capFound && now.Sub(capFail) <= p.FailureMemory && !hasProof(f, p, now, capFail)
	// Old failures expire like capacity ones: otherwise three failures from days ago, with nothing
	// completed since, would keep a long-idle pool in probe mode forever.
	otherFailing := otherCount >= 3 && now.Sub(otherFail) <= p.FailureMemory && !hasProof(f, p, now, otherFail)
	if otherFailing && (!capFailing || otherFail.After(capFail)) {
		return true, otherFail
	}
	if capFailing {
		return true, capFail
	}
	return false, time.Time{}
}

func hasProof(f PoolFacts, p config.PoolConfig, now, lastFail time.Time) bool {
	for _, runs := range [][]SlotRun{f.Open, f.Closed} {
		for _, r := range runs {
			if r.Started.After(lastFail) && (r.Status == StatusCompleted || (r.Status == StatusRunning && now.Sub(r.Started) >= p.DrawGrace)) {
				return true
			}
		}
	}
	return false
}
