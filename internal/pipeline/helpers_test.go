package pipeline

import (
	"reflect"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
)

func clockTime() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
func poolConfig() config.PoolConfig {
	return config.PoolConfig{LowPct: 15, HighPct: 30, MinFree: 2, MinBand: 1, MaxOpenRunsPerPool: 15, ProbeInterval: 20 * time.Minute, DrawGrace: 2 * time.Minute, FailureMemory: 6 * time.Hour, SettlingMax: time.Hour}
}

type settings struct{ p config.PoolConfig }

func (s settings) Manages(name string) bool         { return name != "unmanaged" }
func (s settings) ForPool(string) config.PoolConfig { return s.p }
func snapshot() RawSnapshot {
	return RawSnapshot{At: clockTime(), InfraEnvs: []kube.InfraEnv{{Name: "a"}, {Name: "b"}}, Pollers: PollerStatus{Activity: 1, Workflow: 1, Segment: 1}, Sweep: map[PoolKey]SweepResult{}}
}
func nodePool() kube.NodePool {
	n := int32(3)
	return kube.NodePool{Name: "np", PlatformType: "Agent", Replicas: &n, Selector: kube.Selector{MatchLabels: map[string]string{kube.LabelInfraEnv: "a"}}}
}
func agent() kube.Agent {
	return kube.Agent{Name: "agent", Labels: map[string]string{kube.LabelInfraEnv: "a"}, Approved: true, Connected: true, State: kube.StateKnownUnbound, LastStateChange: clockTime().Add(-time.Minute)}
}
func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}
func closed(status, fail string, minutes int) SlotRun {
	return SlotRun{Status: status, FailType: fail, Closed: clockTime().Add(-time.Duration(minutes) * time.Minute), Started: clockTime().Add(-time.Duration(minutes+1) * time.Minute)}
}
func running(minutes int) SlotRun {
	return SlotRun{Status: StatusRunning, Acquired: true, Started: clockTime().Add(-time.Duration(minutes) * time.Minute)}
}
