// Package config is herzi's configuration: hub-wide settings, per-pool defaults, and per-pool overrides.
//
// Settings are of two kinds. HUB settings describe this hub or protect against a problem of this hub
// and cannot differ per pool. POOL settings all live under Defaults and every one can be overridden for
// a single pool; a pool is only managed when it is listed under Pools.
package config

import "time"

// HubConfig is one value per hub. Nothing here can be overridden per pool.
type HubConfig struct {
	Interval         time.Duration `json:"interval"`         // steady requeue, default 2m
	ReconcileTimeout time.Duration `json:"reconcileTimeout"` // overall deadline of one reconcile, default 60s
	HubName          string        `json:"hubName"`          // canonical MCE name, "ocp4-..."
	AgentNamespace   string        `json:"agentNamespace"`   // where InfraEnvs and Agents live, default "inventory"
	InstallURL       string        `json:"installURL"`       // POST /workflows/server-lifecycle/install-server
	MaxSlots         int           `json:"maxSlots"`         // must equal the route's slot range, default 100
	AmbiguousWait    time.Duration `json:"ambiguousWait"`    // block after an unclear POST, default 5m
	StuckAfter       time.Duration `json:"stuckAfter"`       // open run older than this => alert only, default 2h
	// Temporal and the activity/workflow queue names the poller check looks at.
	TemporalAddress   string `json:"temporalAddress"`
	TemporalNamespace string `json:"temporalNamespace"`
	ActivityQueue     string `json:"activityQueue"` // "server-lifecycle-activity-<mce>"
	WorkflowQueue     string `json:"workflowQueue"` // "install-server-workflow"
	SegmentQueue      string `json:"segmentQueue"`  // segment-lifecycle activity queue
}

// PoolConfig is the fully merged, concrete settings of ONE pool.
type PoolConfig struct {
	LowPct             int           `json:"lowPct"`
	HighPct            int           `json:"highPct"`
	MinFree            int           `json:"minFree"`
	MinBand            int           `json:"minBand"`
	MaxOpenRunsPerPool int           `json:"maxOpenRunsPerPool"`
	ProbeInterval      time.Duration `json:"probeInterval"`
	DrawGrace          time.Duration `json:"drawGrace"`
	FailureMemory      time.Duration `json:"failureMemory"`
	SettlingMax        time.Duration `json:"settlingMax"`
	DryRunGrow         bool          `json:"dryRunGrow"`
	Paused             bool          `json:"paused"`
}

// PoolOverride mirrors PoolConfig with pointers: nil means "not written, inherit the default",
// so minFree: 0 and paused: false are real overrides.
type PoolOverride struct {
	LowPct             *int           `json:"lowPct,omitempty"`
	HighPct            *int           `json:"highPct,omitempty"`
	MinFree            *int           `json:"minFree,omitempty"`
	MinBand            *int           `json:"minBand,omitempty"`
	MaxOpenRunsPerPool *int           `json:"maxOpenRunsPerPool,omitempty"`
	ProbeInterval      *time.Duration `json:"probeInterval,omitempty"`
	DrawGrace          *time.Duration `json:"drawGrace,omitempty"`
	FailureMemory      *time.Duration `json:"failureMemory,omitempty"`
	SettlingMax        *time.Duration `json:"settlingMax,omitempty"`
	DryRunGrow         *bool          `json:"dryRunGrow,omitempty"`
	Paused             *bool          `json:"paused,omitempty"`
}

// Config is the whole file. The hub settings sit at the TOP LEVEL of the YAML (HubConfig is embedded),
// followed by `defaults` and `pools`:
//
//	interval: 120s
//	hubName: ocp4-bat-yam-01
//	defaults: {lowPct: 15, ...}
//	pools: {"32-128-zone-1": {minFree: 5}}
//
// Durations are strings ("20m"). The methods (ForPool, Manages, Validate) and the strict loader
// live in other files of this package; the json tags here describe field NAMES only, because
// time.Duration does not unmarshal from "20m" by itself.
type Config struct {
	HubConfig
	Defaults PoolConfig              `json:"defaults"`
	Pools    map[string]PoolOverride `json:"pools"` // keyed by InfraEnv name; presence == managed
}
