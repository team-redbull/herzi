package pipeline

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

type State struct {
	CapFailAt      time.Time `json:"capFailAt,omitempty"`
	OtherFails     []Failure `json:"otherFails,omitempty"`
	LastOKAt       time.Time `json:"lastOKAt,omitempty"`
	ProbeNotBefore time.Time `json:"probeNotBefore,omitempty"`
}

type Failure struct {
	RunID    string    `json:"runId"`
	ClosedAt time.Time `json:"closedAt"`
}

// ParseState falls back to sweep-only evidence for missing, oversized or malformed state.
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

// EncodeState returns "{}" if serialization fails or exceeds the annotation bound.
func EncodeState(s State) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) > 1024 {
		return "{}"
	}
	return string(b)
}

// MergeState retains unexpired evidence monotonically and deduplicates by execution ID.
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
		// Missing run IDs use close times to avoid merging all unidentified failures.
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
