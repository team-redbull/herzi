// Package config loads, validates and publishes hub settings and per-pool overrides.
package config

import "time"

type HubConfig struct {
	Interval         time.Duration `json:"interval"`
	ReconcileTimeout time.Duration `json:"reconcileTimeout"`
	HubName          string        `json:"hubName"`
	AgentNamespace   string        `json:"agentNamespace"`
	InstallURL       string        `json:"installURL"`
	MaxSlots         int           `json:"maxSlots"`
	AmbiguousWait    time.Duration `json:"ambiguousWait"`
	StuckAfter       time.Duration `json:"stuckAfter"`

	TemporalAddress   string `json:"temporalAddress"`
	TemporalNamespace string `json:"temporalNamespace"`
	ActivityQueue     string `json:"activityQueue"`
	WorkflowQueue     string `json:"workflowQueue"`
	SegmentQueue      string `json:"segmentQueue"`
}

type PoolConfig struct {
	HighPct            int           `json:"highPct"`
	MinFree            int           `json:"minFree"`
	MaxOpenRunsPerPool int           `json:"maxOpenRunsPerPool"`
	ProbeInterval      time.Duration `json:"probeInterval"`
	DrawGrace          time.Duration `json:"drawGrace"`
	FailureMemory      time.Duration `json:"failureMemory"`
	SettlingMax        time.Duration `json:"settlingMax"`
	DryRunGrow         bool          `json:"dryRunGrow"`
	Paused             bool          `json:"paused"`
}

// PoolOverride uses nil to inherit defaults; explicit zero and false override them.
type PoolOverride struct {
	HighPct            *int           `json:"highPct,omitempty"`
	MinFree            *int           `json:"minFree,omitempty"`
	MaxOpenRunsPerPool *int           `json:"maxOpenRunsPerPool,omitempty"`
	ProbeInterval      *time.Duration `json:"probeInterval,omitempty"`
	DrawGrace          *time.Duration `json:"drawGrace,omitempty"`
	FailureMemory      *time.Duration `json:"failureMemory,omitempty"`
	SettlingMax        *time.Duration `json:"settlingMax,omitempty"`
	DryRunGrow         *bool          `json:"dryRunGrow,omitempty"`
	Paused             *bool          `json:"paused,omitempty"`
}

type Config struct {
	HubConfig
	Defaults PoolConfig              `json:"defaults"`
	Pools    map[string]PoolOverride `json:"pools"`
}
