package pipeline

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPlanLimits(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		need, limit, open, count int
	}{
		{"need limited", 2, 15, 0, 2}, {"headroom limited", 58, 15, 5, 10}, {"api limited", 58, 100, 0, 20}, {"exact api", 20, 100, 0, 20}, {"no need", 0, 15, 0, 0}, {"negative need", -1, 15, 0, 0}, {"full", 3, 15, 15, 0}, {"overfull", 3, 15, 16, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := poolConfig()
			p.MaxOpenRunsPerPool = tt.limit
			got := Plan(Grow{Key: "a", Need: tt.need}, p, PoolFacts{Open: make([]SlotRun, tt.open)})
			if tt.count == 0 {
				equal(t, got, []InstallRequest(nil))
			} else {
				equal(t, got, []InstallRequest{{Key: "a", Count: tt.count}})
			}
		})
	}
}
func TestPlanProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := poolConfig()
		p.MaxOpenRunsPerPool = rapid.IntRange(-5, 200).Draw(t, "limit")
		open := rapid.IntRange(0, 200).Draw(t, "open")
		g := Grow{Key: "a", Need: rapid.IntRange(-10, 2000).Draw(t, "need")}
		got := Plan(g, p, PoolFacts{Open: make([]SlotRun, open)})
		if len(got) > 1 {
			t.Fatal("multiple requests")
		}
		for _, r := range got {
			if r.Count <= 0 || r.Count > 20 || r.Count > p.MaxOpenRunsPerPool-open || r.Count > g.Need {
				t.Fatalf("invalid plan: %+v", r)
			}
		}
	})
}
func TestBlockedPoolsNeverOrderProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := snapshot()
		f := PoolFacts{Key: "a", Managed: true, Demand: rapid.IntRange(0, 100).Draw(t, "demand")}
		switch rapid.IntRange(0, 3).Draw(t, "cause") {
		case 0:
			f.Managed = false
		case 1:
			f.Paused = true
		case 2:
			f.EarlyBound = true
		case 3:
			f.Deleting = true
		}
		v := Validate(f, raw, poolConfig())
		if !v.Blocked || len(v.Reasons) == 0 {
			t.Fatal("unsafe pool passed Validate")
		}

		var orders []InstallRequest
		if !v.Blocked {
			if g, ok := Decide(f, poolConfig(), raw.At).(Grow); ok {
				orders = Plan(g, poolConfig(), f)
			}
		}
		if len(orders) != 0 {
			t.Fatal("blocked pool ordered")
		}
	})
}
