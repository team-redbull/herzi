package config

import "time"

// Defaults implements Part 2, Config: "Pool settings all live under defaults".
func Defaults() PoolConfig {
	return PoolConfig{LowPct: 15, HighPct: 30, MinFree: 2, MinBand: 1, MaxOpenRunsPerPool: 15,
		ProbeInterval: 20 * time.Minute, DrawGrace: 2 * time.Minute, FailureMemory: 6 * time.Hour, SettlingMax: time.Hour}
}

// Queue names fixed by the workflows repo (shared/consts.py). They are defaults so a hub cannot ship a
// misspelt one: a queue nobody polls reads as "zero pollers" and would block every pool.
const (
	DefaultWorkflowQueue        = "install-server-workflow"
	DefaultSegmentQueue         = "segment-lifecycle-activity"
	activityQueuePrefix         = "server-lifecycle-activity-"
	DefaultTemporalNamespaceVal = "default"
)

// DefaultHub supplies Part 2, Config's hub-wide timing, slot and queue defaults.
// The hub name, install URL and Temporal address must be supplied by the operator. The activity queue
// is derived from the hub name (server-lifecycle-activity-<mce>) when it is not written.
func DefaultHub() HubConfig {
	return HubConfig{Interval: 2 * time.Minute, ReconcileTimeout: time.Minute, AgentNamespace: "inventory",
		MaxSlots: 100, AmbiguousWait: 5 * time.Minute, StuckAfter: 2 * time.Hour,
		TemporalNamespace: DefaultTemporalNamespaceVal, WorkflowQueue: DefaultWorkflowQueue, SegmentQueue: DefaultSegmentQueue}
}

// Manages implements Part 2, Config: "A pool that is not listed is not managed".
func (c Config) Manages(name string) bool { _, ok := c.Pools[name]; return ok }

// ForPool implements Part 2, Config: "each field takes the pool's value if it is written, otherwise the default".
func (c Config) ForPool(name string) PoolConfig { return merge(c.Defaults, c.Pools[name]) }

func merge(p PoolConfig, o PoolOverride) PoolConfig {
	if o.LowPct != nil {
		p.LowPct = *o.LowPct
	}
	if o.HighPct != nil {
		p.HighPct = *o.HighPct
	}
	if o.MinFree != nil {
		p.MinFree = *o.MinFree
	}
	if o.MinBand != nil {
		p.MinBand = *o.MinBand
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
