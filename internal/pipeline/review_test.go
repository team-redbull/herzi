package pipeline

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTargetOverflowFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name              string
		demand, pct, free int
	}{
		{"percentage product plus rounding", 1, math.MaxInt64, 2},
		{"target addition", 1, 30, math.MaxInt64},
		{"default percentage rounding overflow", 307445734561825861, 30, 2},
		{"negative demand", -1, 30, 2},
		{"negative percentage", 1, -1, 2},
		{"negative floor", 1, 30, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := poolConfig()
			p.HighPct, p.MinFree = tc.pct, tc.free
			buffer, target, ok := Target(tc.demand, p)
			require.False(t, ok)
			require.Zero(t, buffer)
			require.Zero(t, target)
			require.Equal(t, Hold{Key: "a", Reason: HoldTargetOutOfRange},
				Decide(PoolFacts{Key: "a", Demand: tc.demand}, p, clockTime()))
		})
	}
	for _, f := range []PoolFacts{
		{Assigned: math.MaxInt64, Claimable: 1},
		{Assigned: math.MaxInt64, Settling: 1},
		{Assigned: math.MaxInt64, Open: []SlotRun{{}}},
		{Assigned: -1}, {Claimable: -1}, {Settling: -1},
	} {
		require.Equal(t, Hold{Reason: HoldTargetOutOfRange}, Decide(f, poolConfig(), clockTime()))
	}
}

func TestTargetLiteralArithmetic(t *testing.T) {
	for _, tc := range []struct{ demand, pct, free, buffer, target int }{
		{0, 30, 2, 2, 2}, {1, 30, 2, 2, 3}, {5, 10, 0, 1, 6},
		{20, 30, 2, 6, 26}, {50, 30, 2, 15, 65},
		{10_000_000, 1000, 100000, 100_000_000, 110_000_000},
		{307445734561825858, 30, 2, 92233720368547757, 399679454930373615},
		{math.MaxInt64, 0, 0, 0, math.MaxInt64},
	} {
		p := poolConfig()
		p.HighPct, p.MinFree = tc.pct, tc.free
		buffer, target, ok := Target(tc.demand, p)
		require.True(t, ok)
		require.Equal(t, tc.buffer, buffer)
		require.Equal(t, tc.target, target)
	}
}

func TestAttributeImplausibleDemand(t *testing.T) {
	for _, tc := range []struct {
		counts  []int32
		blocked bool
	}{
		{[]int32{10_000_000}, false}, {[]int32{6_000_000, 4_000_000}, false},
		{[]int32{10_000_001}, true}, {[]int32{6_000_000, 4_000_001}, true},
		{[]int32{math.MaxInt32, math.MaxInt32}, true}, {[]int32{-1}, true},
	} {
		raw := snapshot()
		for _, count := range tc.counts {
			n := nodePool()
			n.Replicas = &count
			raw.NodePools = append(raw.NodePools, n)
		}
		facts := Attribute(raw, settings{poolConfig()})
		f := facts["a"]
		require.Equal(t, tc.blocked, Validate(f, raw, poolConfig()).Blocked)
		require.LessOrEqual(t, f.Demand, 10_000_000)
		if tc.blocked {
			require.Equal(t, []Problem{{Pool: "a", Msg: "/np: implausible demand"}}, f.Problems[:1])
		} else {
			require.Equal(t, 10_000_000, f.Demand)
		}
		require.False(t, Validate(facts["b"], raw, poolConfig()).Blocked)
	}
}

func TestTimedOutUnknownNeverAllowsFull(t *testing.T) {
	p := poolConfig()
	run := closed(StatusTimedOut, FailUnknown, 30)
	f := PoolFacts{Key: "a", Demand: 6, Closed: []SlotRun{run}}
	require.Equal(t, Grow{Key: "a", Need: 1, Mode: Probe}, Decide(f, p, clockTime()))
	state := MergeState(State{}, f.Closed, clockTime(), p)
	require.Equal(t, run.Closed, state.CapFailAt)
	f.State, f.Closed = state, nil
	require.Equal(t, Grow{Key: "a", Need: 1, Mode: Probe}, Decide(f, p, clockTime()))
}
