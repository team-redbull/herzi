package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestF4SlotReuseRetainsCapacityEvidence(t *testing.T) {
	now, p := clockTime(), poolConfig()
	failure := SlotRun{Slot: 1, RunID: "original", Status: StatusFailed,
		FailType: FailServerNotAvailable, Closed: now.Add(-21 * time.Minute)}
	state := MergeState(State{}, []SlotRun{failure}, now, p)
	probe := SlotRun{Slot: 1, RunID: "probe", Status: StatusRunning, Started: now.Add(-10 * time.Second)}
	f := PoolFacts{Key: "a", Demand: 10, Assigned: 7, State: ParseState(EncodeState(state)), Open: []SlotRun{probe}}
	require.Equal(t, Hold{Key: "a", Reason: HoldProbeRunning}, Decide(f, p, now))
}

func TestF4HundredDistinctFailuresReusingTwoSlots(t *testing.T) {
	now, p := clockTime(), poolConfig()
	var state State
	for i := range 100 {
		r := SlotRun{Slot: i%2 + 1, RunID: fmt.Sprint(i), Status: StatusFailed,
			FailType: "BmcCredentialsMissingError", Closed: now.Add(time.Duration(i) * time.Second)}
		state = MergeState(ParseState(EncodeState(state)), []SlotRun{r, r}, r.Closed, p)
		require.LessOrEqual(t, len(state.OtherFails), 3)
		require.LessOrEqual(t, len(EncodeState(state)), 1024)
		f := PoolFacts{Key: "a", State: state}
		failing, _ := hardwareLooksMissing(f, p, r.Closed)
		require.Equal(t, i >= 2, failing)
	}
	require.Equal(t, Grow{Key: "a", Need: 1, Mode: Probe},
		Decide(PoolFacts{Key: "a", State: state}, p, now.Add(22*time.Minute)))
}

func TestF5UnknownHistoryNeverAllowsFull(t *testing.T) {
	f := PoolFacts{Key: "a", Closed: []SlotRun{
		{RunID: "1", Status: StatusFailed, FailType: FailUnknown, Closed: clockTime().Add(-21 * time.Minute)},
		{RunID: "2", Status: StatusFailed, FailType: FailUnknown, Closed: clockTime().Add(-21 * time.Minute)},
	}}
	require.Equal(t, Grow{Key: "a", Need: 1, Mode: Probe}, Decide(f, poolConfig(), clockTime()))
}

func TestF6ProbeMustAcquireMachine(t *testing.T) {
	now, p := clockTime(), poolConfig()
	f := PoolFacts{Key: "a", Demand: 6, Closed: []SlotRun{closed(StatusFailed, FailServerReserved, 21)},
		Open: []SlotRun{{RunID: "probe", Status: StatusRunning, Started: now.Add(-3 * time.Minute)}}}
	require.Equal(t, Hold{Key: "a", Reason: HoldProbeRunning}, Decide(f, p, now))
	f.Open[0].Acquired = true
	require.Equal(t, Grow{Key: "a", Need: 7, Mode: Full}, Decide(f, p, now))
}

func TestF9OldHungWorkDoesNotStarveProbe(t *testing.T) {
	now, p := clockTime(), poolConfig()
	f := PoolFacts{Key: "a", Demand: 6, Open: []SlotRun{{Status: StatusRunning,
		Started: now.Add(-30 * time.Minute)}}, Closed: []SlotRun{{Status: StatusFailed,
		FailType: FailServerReserved, Closed: now.Add(-30*time.Minute + 20*time.Second)}}}
	require.Equal(t, Grow{Key: "a", Need: 1, Mode: Probe}, Decide(f, p, now))
	require.Equal(t, []InstallRequest{{Key: "a", Count: 1}}, Plan(Grow{Key: "a", Need: 1, Mode: Probe}, p, f))
	p.MaxOpenRunsPerPool = 1
	require.Empty(t, Plan(Grow{Key: "a", Need: 1, Mode: Probe}, p, f))
}

func TestF4StateDegradationAndMerge(t *testing.T) {
	for _, value := range []string{"", "{", strings.Repeat("x", 1025),
		`{"otherFails":[{},{},{},{}]}`} {
		require.Equal(t, State{}, ParseState(value))
	}
	require.Equal(t, State{}, ParseState(`{"future":true}`))
	now, p := clockTime(), poolConfig()
	old := now.Add(-7 * time.Hour)
	state := MergeState(State{CapFailAt: old, OtherFails: []Failure{{"old", old}}}, nil, now, p)
	require.True(t, state.CapFailAt.IsZero())
	require.Empty(t, state.OtherFails)
	evidence := []SlotRun{{RunID: "duplicate", Status: StatusFailed, Closed: now.Add(-time.Hour)},
		{RunID: "duplicate", Status: StatusFailed, Closed: now.Add(-time.Hour)}}
	state = MergeState(State{}, evidence, now, p)
	require.Len(t, state.OtherFails, 1)
	require.Equal(t, state, MergeState(state, evidence, now, p))
	require.Equal(t, "{}", EncodeState(State{OtherFails: []Failure{{strings.Repeat("a", 2000), now}}}))
	state = MergeState(state, []SlotRun{{Status: StatusCompleted, Started: now.Add(-time.Minute), Closed: now}}, now, p)
	require.Empty(t, state.OtherFails)
	require.Equal(t, now, state.LastOKAt)
}
