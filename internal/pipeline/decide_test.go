package pipeline

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestDecideDesignTable reproduces every row of Part 2's "Test table for Decide".
// Set percentages to zero and express each row's low/high via MinFree/MinBand.
func TestDecideDesignTable(t *testing.T) {
	cap21 := closed(StatusFailed, FailServerNotAvailable, 21)
	for _, tt := range []struct {
		name                     string
		d, a, c, s, o, low, high int
		closed                   []SlotRun
		open                     []SlotRun
		want                     Intent
		blocked                  bool
		plan                     int
	}{
		{name: "01 short by one", d: 20, a: 18, c: 3, low: 2, high: 4, want: Grow{Key: "a", Need: 1, Mode: Full}},
		{name: "02 target", d: 20, a: 20, c: 2, low: 2, high: 4, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "03 remove four", d: 20, a: 20, c: 6, low: 2, high: 4, want: WouldRemove{Key: "a", Surplus: 4, Total: 26, Target: 22, Ceiling: 24}},
		{name: "04 running subtracted", d: 20, a: 18, c: 3, o: 1, low: 2, high: 4, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "05 no removal with running", d: 20, a: 20, c: 6, o: 2, low: 2, high: 4, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "06 settling supply", d: 20, a: 4, c: 3, s: 16, low: 2, high: 4, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "07 no removal with settling", d: 12, a: 4, c: 3, s: 16, low: 2, high: 4, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "08 seed managed", low: 2, high: 3, want: Grow{Key: "a", Need: 2, Mode: Full}},
		{name: "09 unmanaged", low: 2, high: 3, blocked: true},
		{name: "10 recent failure", d: 6, low: 2, high: 3, closed: []SlotRun{closed(StatusFailed, FailServerNotAvailable, 3)}, want: Hold{Key: "a", Reason: HoldProbeWait}},
		{name: "11 probe due", d: 6, low: 2, high: 3, closed: []SlotRun{cap21}, want: Grow{Key: "a", Need: 1, Mode: Probe}},
		{name: "12 young probe", d: 6, low: 2, high: 3, closed: []SlotRun{cap21}, open: []SlotRun{running(1)}, want: Hold{Key: "a", Reason: HoldProbeRunning}},
		{name: "13 mature probe proves recovery", d: 6, low: 2, high: 3, closed: []SlotRun{cap21}, open: []SlotRun{running(3)}, want: Grow{Key: "a", Need: 7, Mode: Full}},
		{name: "14 partial batch no proof", d: 10, a: 4, low: 2, high: 3, closed: []SlotRun{{Status: StatusFailed, FailType: FailServerReserved, Closed: clockTime().Add(-10*time.Minute + 20*time.Second)}, {Status: StatusFailed, FailType: FailServerReserved, Closed: clockTime().Add(-10*time.Minute + 20*time.Second)}, {Status: StatusFailed, FailType: FailServerReserved, Closed: clockTime().Add(-10*time.Minute + 20*time.Second)}, {Status: StatusFailed, FailType: FailServerReserved, Closed: clockTime().Add(-10*time.Minute + 20*time.Second)}}, open: []SlotRun{running(10), running(10), running(10), running(10), running(10), running(10)}, want: Hold{Key: "a", Reason: HoldProbeWait}},
		{name: "15 completed proof", d: 6, c: 1, low: 2, high: 3, closed: []SlotRun{closed(StatusFailed, FailServerNotAvailable, 30), closed(StatusCompleted, "", 5)}, want: Grow{Key: "a", Need: 7, Mode: Full}},
		{name: "16 completed started before failure", d: 6, low: 2, high: 3, closed: []SlotRun{closed(StatusFailed, FailServerNotAvailable, 30), {Status: StatusCompleted, Started: clockTime().Add(-40 * time.Minute), Closed: clockTime().Add(-5 * time.Minute)}}, want: Grow{Key: "a", Need: 1, Mode: Probe}},
		{name: "17 two other failures", d: 6, low: 2, high: 3, closed: []SlotRun{closed(StatusFailed, "broken", 30), closed(StatusFailed, "broken", 25)}, want: Grow{Key: "a", Need: 8, Mode: Full}},
		{name: "18 three other failures", d: 6, low: 2, high: 3, closed: []SlotRun{closed(StatusFailed, "broken", 35), closed(StatusFailed, "broken", 30), closed(StatusFailed, "broken", 25)}, want: Grow{Key: "a", Need: 1, Mode: Probe}},
		{name: "19 large wave", d: 50, low: 8, high: 15, want: Grow{Key: "a", Need: 58, Mode: Full}, plan: 15},
		{name: "20 assigned exceeds demand", d: 9, a: 12, low: 2, high: 3, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "21 ceiling equality", d: 6, a: 6, c: 3, low: 2, high: 3, want: Hold{Key: "a", Reason: HoldInsideBand}},
		{name: "22 above ceiling", d: 6, a: 6, c: 4, low: 2, high: 3, want: WouldRemove{Key: "a", Surplus: 2, Total: 10, Target: 8, Ceiling: 9}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := poolConfig()
			p.LowPct = 0
			p.HighPct = 0
			p.MinFree = tt.low
			p.MinBand = tt.high - tt.low
			f := PoolFacts{Key: "a", Managed: !tt.blocked, Demand: tt.d, Assigned: tt.a, Claimable: tt.c, Settling: tt.s, Closed: tt.closed, Open: tt.open}
			for i := 0; i < tt.o; i++ {
				f.Open = append(f.Open, running(1))
			}
			v := Validate(f, snapshot(), p)
			equal(t, v.Blocked, tt.blocked)
			if v.Blocked {
				return
			}
			got := Decide(f, p, clockTime())
			equal(t, got, tt.want)
			if tt.plan > 0 {
				equal(t, Plan(got.(Grow), p, f), []InstallRequest{{Key: "a", Count: tt.plan}})
			}
		})
	}
}

func TestDecideBuffers(t *testing.T) {
	for _, tt := range []struct{ d, low, high int }{{0, 2, 3}, {4, 2, 3}, {6, 2, 3}, {10, 2, 3}, {12, 2, 4}, {20, 3, 6}, {50, 8, 15}} {
		t.Run(time.Duration(tt.d).String(), func(t *testing.T) {
			p := poolConfig()
			equal(t, Decide(PoolFacts{Key: "a", Demand: tt.d}, p, clockTime()), Grow{Key: "a", Need: tt.d + tt.low, Mode: Full})
			f := PoolFacts{Key: "a", Demand: tt.d, Claimable: tt.d + tt.high + 1}
			got := Decide(f, p, clockTime()).(WouldRemove)
			equal(t, got.Ceiling, tt.d+tt.high)
			equal(t, got.Target, tt.d+tt.low)
		})
	}
	// Half-up at exactly 0.5, and minimum band overrides percentage.
	p := poolConfig()
	p.MinFree = 0
	p.LowPct = 10
	p.HighPct = 10
	equal(t, Decide(PoolFacts{Key: "a", Demand: 5}, p, clockTime()), Grow{Key: "a", Need: 6, Mode: Full})
	f := PoolFacts{Key: "a", Demand: 5, Claimable: 8}
	equal(t, Decide(f, p, clockTime()), WouldRemove{Key: "a", Surplus: 2, Total: 8, Target: 6, Ceiling: 7})
}

func TestProbeProofTable(t *testing.T) {
	p := poolConfig()
	failure := closed(StatusFailed, FailServerNotAvailable, 30)
	three := []SlotRun{closed(StatusFailed, "broken", 40), closed(StatusFailed, "broken", 35), closed(StatusFailed, "broken", 30)}
	for _, tt := range []struct {
		name         string
		closed, open []SlotRun
		want         Intent
	}{
		{"failure no proof", []SlotRun{failure}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"reserved capacity", []SlotRun{closed(StatusFailed, FailServerReserved, 30)}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"timed out capacity", []SlotRun{closed(StatusTimedOut, FailServerReserved, 30)}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"terminated capacity type ignored", []SlotRun{closed(StatusTerminated, FailServerReserved, 30)}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"proof started before fail", []SlotRun{failure, {Status: StatusCompleted, Started: failure.Closed.Add(-time.Second), Closed: clockTime()}}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"proof started equal fail", []SlotRun{failure, {Status: StatusCompleted, Started: failure.Closed, Closed: clockTime()}}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"completed proof after fail", []SlotRun{failure, closed(StatusCompleted, "", 5)}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"probe still running after grace", []SlotRun{failure}, []SlotRun{running(3)}, Grow{Key: "a", Need: 7, Mode: Full}},
		{"probe at grace", []SlotRun{failure}, []SlotRun{running(2)}, Grow{Key: "a", Need: 7, Mode: Full}},
		{"probe before grace", []SlotRun{failure}, []SlotRun{{Status: StatusRunning, Started: clockTime().Add(-p.DrawGrace + time.Nanosecond)}}, Hold{Key: "a", Reason: HoldProbeRunning}},
		{"running started before fail", []SlotRun{failure}, []SlotRun{running(31)}, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"failure older than memory", []SlotRun{closed(StatusFailed, FailServerNotAvailable, 361)}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"failure at memory", []SlotRun{closed(StatusFailed, FailServerNotAvailable, 360)}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"probe at interval", []SlotRun{closed(StatusFailed, FailServerNotAvailable, 20)}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"probe before interval", []SlotRun{{Status: StatusFailed, FailType: FailServerNotAvailable, Closed: clockTime().Add(-p.ProbeInterval + time.Nanosecond)}}, nil, Hold{Key: "a", Reason: HoldProbeWait}},
		// Describe exposes only the latest execution per slot; overwritten evidence is absent.
		{"slot reuse overwrote failure with young run", nil, []SlotRun{running(1)}, Grow{Key: "a", Need: 7, Mode: Full}},
		{"slot reuse overwrote proof with failure", []SlotRun{failure}, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"slot reuse new mature run proves remaining failure", []SlotRun{failure}, []SlotRun{running(3)}, Grow{Key: "a", Need: 7, Mode: Full}},
		{"two noncapacity", three[:2], nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"three noncapacity", three, nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"noncapacity success resets", append(append([]SlotRun{}, three...), closed(StatusCompleted, "", 25)), nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"noncapacity mature run proves recovery", three, []SlotRun{running(3)}, Grow{Key: "a", Need: 7, Mode: Full}},
		{"noncapacity young run holds", three, []SlotRun{running(1)}, Hold{Key: "a", Reason: HoldProbeRunning}},
		{"noncapacity recent last failure", []SlotRun{three[0], three[1], closed(StatusFailed, "broken", 1)}, nil, Hold{Key: "a", Reason: HoldProbeWait}},
		{"noncapacity timedout does not count", []SlotRun{three[0], three[1], closed(StatusTimedOut, "broken", 25)}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"older failures do not count after success", []SlotRun{three[0], three[1], closed(StatusCompleted, "", 32), three[2]}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"noncapacity streak older than the failure memory expires", []SlotRun{closed(StatusFailed, "broken", 24*60), closed(StatusFailed, "broken", 24*60-5), closed(StatusFailed, "broken", 24*60-10)}, nil, Grow{Key: "a", Need: 8, Mode: Full}},
		{"newer capacity takes precedence", append(append([]SlotRun{}, three...), closed(StatusFailed, FailServerReserved, 1)), nil, Hold{Key: "a", Reason: HoldProbeWait}},
		{"newer noncapacity takes precedence", append([]SlotRun{failure}, closed(StatusFailed, "broken", 20), closed(StatusFailed, "broken", 10), closed(StatusFailed, "broken", 1)), nil, Hold{Key: "a", Reason: HoldProbeWait}},
		{"old capacity does not hide noncapacity streak", append([]SlotRun{closed(StatusFailed, FailServerReserved, 361)}, three...), nil, Grow{Key: "a", Need: 1, Mode: Probe}},
		{"latest failure chosen independent of order", []SlotRun{closed(StatusFailed, FailServerReserved, 1), failure}, nil, Hold{Key: "a", Reason: HoldProbeWait}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := PoolFacts{Key: "a", Demand: 6, Closed: tt.closed, Open: tt.open}
			if tt.name == "slot reuse overwrote failure with young run" {
				f.State = MergeState(State{}, []SlotRun{failure}, clockTime(), p)
				tt.want = Hold{Key: "a", Reason: HoldProbeRunning}
			}
			equal(t, Decide(f, p, clockTime()), tt.want)
		})
	}
}

func TestShrinkGuards(t *testing.T) {
	for _, tt := range []struct {
		name string
		f    PoolFacts
		want Intent
	}{
		{"no free victims", PoolFacts{Key: "a", Assigned: 10}, Hold{Key: "a", Reason: HoldInsideBand}},
		{"cap by claimable", PoolFacts{Key: "a", Assigned: 10, Claimable: 1}, WouldRemove{Key: "a", Surplus: 1, Total: 11, Target: 2, Ceiling: 3}},
		{"settling prevents shrink", PoolFacts{Key: "a", Assigned: 10, Claimable: 1, Settling: 1}, Hold{Key: "a", Reason: HoldInsideBand}},
		{"open prevents shrink", PoolFacts{Key: "a", Assigned: 10, Claimable: 1, Open: []SlotRun{running(1)}}, Hold{Key: "a", Reason: HoldInsideBand}},
	} {
		t.Run(tt.name, func(t *testing.T) { equal(t, Decide(tt.f, poolConfig(), clockTime()), tt.want) })
	}
}

func TestDecideProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := poolConfig()
		p.LowPct = rapid.IntRange(0, 100).Draw(t, "lowPct")
		p.HighPct = rapid.IntRange(p.LowPct, 200).Draw(t, "highPct")
		p.MinFree = rapid.IntRange(0, 30).Draw(t, "minFree")
		p.MinBand = rapid.IntRange(1, 20).Draw(t, "minBand")
		f := PoolFacts{Key: "a", Demand: rapid.IntRange(0, 1000).Draw(t, "demand"), Assigned: rapid.IntRange(0, 1000).Draw(t, "assigned"), Claimable: rapid.IntRange(0, 1000).Draw(t, "claimable"), Settling: rapid.IntRange(0, 100).Draw(t, "settling"), Open: make([]SlotRun, rapid.IntRange(0, 100).Draw(t, "running"))}
		for i := range f.Open {
			f.Open[i] = running(rapid.IntRange(0, 60).Draw(t, "run age"))
		}
		for i, n := 0, rapid.IntRange(0, 6).Draw(t, "closed count"); i < n; i++ {
			fail := rapid.SampledFrom([]string{FailServerReserved, FailServerNotAvailable, "broken"}).Draw(t, "failure type")
			status := rapid.SampledFrom([]string{StatusFailed, StatusCompleted, StatusTimedOut}).Draw(t, "status")
			f.Closed = append(f.Closed, closed(status, fail, rapid.IntRange(0, 400).Draw(t, "closed age")))
		}
		need := f.Demand + max(p.MinFree, (p.LowPct*f.Demand+50)/100) - f.Assigned - f.Claimable - f.Settling - len(f.Open)
		got := Decide(f, p, clockTime())
		g, isGrow := got.(Grow)
		if need <= 0 && isGrow {
			t.Fatalf("Grow with need %d: %+v", need, g)
		}
		if !reflectIntentEqual(got, Decide(f, p, clockTime())) {
			t.Fatal("nondeterministic decision")
		}
		f.Claimable++
		next := Decide(f, p, clockTime())
		g2, grows2 := next.(Grow)
		before := 0
		if isGrow {
			before = g.Need
		}
		after := 0
		if grows2 {
			after = g2.Need
		}
		if after > before {
			t.Fatalf("additional claimable increased need: %d -> %d", before, after)
		}
	})
}
func reflectIntentEqual(a, b Intent) bool { return a == b }
