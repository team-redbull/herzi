package config

import "time"

func Defaults() PoolConfig {
	return PoolConfig{HighPct: 10, MinFree: 2, DryRunGrow: true, MaxOpenRunsPerPool: 15,
		ProbeInterval: 20 * time.Minute, DrawGrace: 2 * time.Minute, FailureMemory: 6 * time.Hour, SettlingMax: time.Hour}
}

const (
	DefaultWorkflowQueue        = "install-server-workflow"
	DefaultSegmentQueue         = "segment-lifecycle-activity"
	activityQueuePrefix         = "server-lifecycle-activity-"
	DefaultTemporalNamespaceVal = "default"
)

func DefaultHub() HubConfig {
	return HubConfig{Interval: 2 * time.Minute, ReconcileTimeout: time.Minute, AgentNamespace: "inventory",
		MaxSlots: 100, AmbiguousWait: 5 * time.Minute, StuckAfter: 2 * time.Hour,
		TemporalNamespace: DefaultTemporalNamespaceVal, WorkflowQueue: DefaultWorkflowQueue,
		SegmentQueue: DefaultSegmentQueue}
}

// Manages reports whether the pool is explicitly listed in configuration.
func (c Config) Manages(name string) bool { _, ok := c.Pools[name]; return ok }

// ForPool merges written pool overrides over defaults, field by field.
func (c Config) ForPool(name string) PoolConfig { return merge(c.Defaults, c.Pools[name]) }

func merge(p PoolConfig, o PoolOverride) PoolConfig {
	if o.HighPct != nil {
		p.HighPct = *o.HighPct
	}
	if o.MinFree != nil {
		p.MinFree = *o.MinFree
	}
	if o.MaxOpenRunsPerPool != nil {
		p.MaxOpenRunsPerPool = *o.MaxOpenRunsPerPool
	}
	if o.ProbeInterval != nil {
		p.ProbeInterval = *o.ProbeInterval
	}
	if o.DrawGrace != nil {
		p.DrawGrace = *o.DrawGrace
	}
	if o.FailureMemory != nil {
		p.FailureMemory = *o.FailureMemory
	}
	if o.SettlingMax != nil {
		p.SettlingMax = *o.SettlingMax
	}
	if o.DryRunGrow != nil {
		p.DryRunGrow = *o.DryRunGrow
	}
	if o.Paused != nil {
		p.Paused = *o.Paused
	}
	return p
}
