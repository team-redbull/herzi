package pipeline

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

// State retains evidence independently of Temporal's reusable slot identities.
type State struct {
	CapFailAt      time.Time `json:"capFailAt,omitempty"`
	OtherFails     []Failure `json:"otherFails,omitempty"`
	LastOKAt       time.Time `json:"lastOKAt,omitempty"`
	ProbeNotBefore time.Time `json:"probeNotBefore,omitempty"`
}

// Failure identifies an execution, rather than its reusable workflow slot.
type Failure struct {
	RunID    string    `json:"runId"`
	ClosedAt time.Time `json:"closedAt"`
}

// ParseState degrades missing, oversized and malformed annotations to sweep-only evidence.
func ParseState(value string) State {
	var s State
	if len(value) > 1024 || json.Unmarshal([]byte(value), &s) != nil {
		return State{}
	}
	if len(s.OtherFails) > 3 {
		return State{}
	}
	return s
}

// EncodeState returns bounded compact JSON. Untrusted oversized run IDs cannot grow the annotation.
func EncodeState(s State) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) > 1024 {
		return "{}"
	}
	return string(b)
}

// MergeState monotonically combines unexpired evidence and deduplicates by execution ID.
func MergeState(prev State, evidence []SlotRun, now time.Time, p config.PoolConfig) State {
	s := prev
	s.OtherFails = slices.Clone(prev.OtherFails)
	cutoff := now.Add(-p.FailureMemory)
	if s.CapFailAt.Before(cutoff) {
		s.CapFailAt = time.Time{}
	}
	s.CapFailAt = latestCapacity(s.CapFailAt, evidence, cutoff)
	s.OtherFails = collectFailures(s.OtherFails, evidence, cutoff)
	s.LastOKAt = latestProof(s, evidence, now, p)
	s.OtherFails = slices.DeleteFunc(s.OtherFails, func(f Failure) bool {
		return f.ClosedAt.Before(cutoff) || !f.ClosedAt.After(s.LastOKAt)
	})
	slices.SortFunc(s.OtherFails, func(a, b Failure) int {
		return cmp.Or(a.ClosedAt.Compare(b.ClosedAt), cmp.Compare(a.RunID, b.RunID))
	})
	seen := make(map[string]bool)
	for i, f := range slices.Backward(s.OtherFails) {
		if seen[f.RunID] {
			s.OtherFails = slices.Delete(s.OtherFails, i, i+1)
		} else {
			seen[f.RunID] = true
		}
	}
	if len(s.OtherFails) > 3 {
		s.OtherFails = slices.Clone(s.OtherFails[len(s.OtherFails)-3:])
	}
	last := s.CapFailAt
	if len(s.OtherFails) == 3 && s.OtherFails[2].ClosedAt.After(last) {
		last = s.OtherFails[2].ClosedAt
	}
	if until := last.Add(p.ProbeInterval); !last.IsZero() && until.After(s.ProbeNotBefore) {
		s.ProbeNotBefore = until
	}
	if len(s.OtherFails) == 0 {
		s.OtherFails = nil
	}
	return s
}

func latestCapacity(latest time.Time, evidence []SlotRun, cutoff time.Time) time.Time {
	for _, r := range evidence {
		if !r.Closed.Before(cutoff) && (r.Status == StatusFailed || r.Status == StatusTimedOut) &&
			(IsCapacityFailure(r.FailType) || r.FailType == FailUnknown) && r.Closed.After(latest) {
			latest = r.Closed
		}
	}
	return latest
}

func collectFailures(previous []Failure, evidence []SlotRun, cutoff time.Time) []Failure {
	for _, r := range evidence {
		if r.Status != StatusFailed || IsCapacityFailure(r.FailType) ||
			r.FailType == FailUnknown || r.Closed.Before(cutoff) {
			continue
		}
		id := r.RunID
		// Older ports without IDs degrade to evidence visible in this sweep.
		if id == "" {
			id = r.Closed.Format(time.RFC3339Nano)
		}
		if len(id) <= 128 {
			previous = append(previous, Failure{id, r.Closed})
		}
	}
	return previous
}

func latestProof(s State, evidence []SlotRun, now time.Time, p config.PoolConfig) time.Time {
	barrier := s.CapFailAt
	for _, f := range s.OtherFails {
		if f.ClosedAt.After(barrier) {
			barrier = f.ClosedAt
		}
	}
	latest := s.LastOKAt
	for _, r := range evidence {
		completed := r.Status == StatusCompleted && r.Started.After(s.CapFailAt)
		acquired := r.Status == StatusRunning && r.Started.After(barrier) &&
			r.Acquired && now.Sub(r.Started) >= p.DrawGrace
		if !completed && !acquired {
			continue
		}
		at := r.Closed
		if acquired {
			at = r.Started.Add(p.DrawGrace)
		}
		if at.After(latest) {
			latest = at
		}
	}
	return latest
}
