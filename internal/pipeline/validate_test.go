package pipeline

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateCauses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*PoolFacts, *RawSnapshot)
		reason string
	}{
		{"healthy", func(*PoolFacts, *RawSnapshot) {}, ""},
		{"unmanaged", func(f *PoolFacts, _ *RawSnapshot) { f.Managed = false }, "unmanaged"},
		{"paused", func(f *PoolFacts, _ *RawSnapshot) { f.Paused = true }, "paused"},
		{"blocked until", func(f *PoolFacts, r *RawSnapshot) { f.BlockedUntil = r.At.Add(time.Nanosecond) }, "blocked until"},
		{"block expires exactly now", func(f *PoolFacts, r *RawSnapshot) { f.BlockedUntil = r.At }, ""},
		{"expired block", func(f *PoolFacts, r *RawSnapshot) { f.BlockedUntil = r.At.Add(-time.Second) }, ""},
		{"deleting", func(f *PoolFacts, _ *RawSnapshot) { f.Deleting = true }, "deleting"},
		{"early bound", func(f *PoolFacts, _ *RawSnapshot) { f.EarlyBound = true }, "early-bound"},
		{"problem", func(f *PoolFacts, _ *RawSnapshot) { f.Problems = []Problem{{Pool: "a", Msg: "bad selector"}} }, "bad selector"},
		{"own sweep", func(_ *PoolFacts, r *RawSnapshot) {
			r.Sweep["a"] = SweepResult{Err: errors.New("describe unavailable")}
		}, "sweep failed"},
		{"other sweep", func(_ *PoolFacts, r *RawSnapshot) {
			r.Sweep["b"] = SweepResult{Err: errors.New("describe unavailable")}
		}, ""},
		{"own read", func(_ *PoolFacts, r *RawSnapshot) {
			r.ReadErrs = []ReadError{{Pool: "a", What: "Agents", Err: errors.New("unavailable")}}
		}, "read Agents failed"},
		{"hub read", func(_ *PoolFacts, r *RawSnapshot) {
			r.ReadErrs = []ReadError{{What: "NodePools", Err: errors.New("unavailable")}}
		}, "read NodePools failed"},
		{"other read", func(_ *PoolFacts, r *RawSnapshot) {
			r.ReadErrs = []ReadError{{Pool: "b", Err: errors.New("unavailable")}}
		}, ""},
		{"poller error", func(_ *PoolFacts, r *RawSnapshot) { r.Pollers.Err = errors.New("unknown") }, "poller check failed"},
		{"activity", func(_ *PoolFacts, r *RawSnapshot) { r.Pollers.Activity = 0 }, "activity queue"},
		{"workflow", func(_ *PoolFacts, r *RawSnapshot) { r.Pollers.Workflow = 0 }, "workflow queue"},
		{"segment", func(_ *PoolFacts, r *RawSnapshot) { r.Pollers.Segment = 0 }, "segment queue"},
		{"hub warnings", func(f *PoolFacts, _ *RawSnapshot) {
			f.Warnings = []Warning{{Object: "stray", Msg: "agent not in any pool"}}
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := snapshot()
			f := PoolFacts{Key: "a", Managed: true}
			tt.change(&f, &raw)
			v := Validate(f, raw, poolConfig())
			equal(t, v.Key, f.Key)
			equal(t, v.Blocked, tt.reason != "")
			if tt.reason != "" {
				equal(t, len(v.Reasons), 1)
				if !strings.Contains(v.Reasons[0], tt.reason) {
					t.Fatalf("reasons: %v", v.Reasons)
				}
			} else {
				equal(t, len(v.Reasons), 0)
			}
		})
	}
}
func TestValidateListsAllCauses(t *testing.T) {
	raw := snapshot()
	err := errors.New("offline")
	raw.Sweep["a"] = SweepResult{Err: err}
	raw.ReadErrs = []ReadError{{Pool: "a", What: "Agents", Err: err}, {What: "NodePools", Err: err}}
	raw.Pollers = PollerStatus{Err: err}
	f := PoolFacts{Key: "a", Paused: true, Deleting: true, EarlyBound: true, BlockedUntil: raw.At.Add(time.Minute), Problems: []Problem{{Msg: "one"}, {Msg: "two"}}}
	v := Validate(f, raw, poolConfig())
	equal(t, v.Blocked, true)
	equal(t, len(v.Reasons), 14)
}
