package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Validate implements Part 2, Config: "Validation, after the merge, per pool".
// It reports all hub, default, and merged-pool problems in one joined error.
// Canonicalization belongs to Load; a manually constructed Config must already be canonical.
func (c Config) Validate() error {
	var problems []error
	check := func(ok bool, field, rule string) {
		if !ok {
			problems = append(problems, fmt.Errorf("%s: %s", field, rule))
		}
	}
	check(strings.TrimSpace(c.HubName) != "" && strings.HasPrefix(c.HubName, "ocp4-"), "hubName", "must be non-empty and canonical (ocp4- prefix)")
	u, err := url.Parse(c.InstallURL)
	check(err == nil && u != nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https"), "installURL", "must be an absolute http(s) URL")
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"interval", c.Interval}, {"reconcileTimeout", c.ReconcileTimeout}, {"ambiguousWait", c.AmbiguousWait}, {"stuckAfter", c.StuckAfter},
	} {
		check(field.value > 0, field.name, "must be > 0")
	}
	check(c.MaxSlots >= 1 && c.MaxSlots <= 100, "maxSlots", "must be between 1 and 100")
	for _, field := range []struct{ name, value string }{
		{"agentNamespace", c.AgentNamespace}, {"activityQueue", c.ActivityQueue}, {"workflowQueue", c.WorkflowQueue}, {"segmentQueue", c.SegmentQueue},
	} {
		check(strings.TrimSpace(field.value) != "", field.name, "must not be empty")
	}
	if err := validatePool(c.Defaults, c.MaxSlots); err != nil {
		problems = append(problems, fmt.Errorf("defaults: %w", err))
	}
	poolErrors := c.PoolErrors()
	names := make([]string, 0, len(poolErrors))
	for name := range poolErrors {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		problems = append(problems, poolErrors[name])
	}
	return errors.Join(problems...)
}

// PoolErrors exposes Part 2, Config's rule: "A pool that fails validation is blocked with the reason named".
// Only listed pools appear, and only when their merged settings fail validation.
func (c Config) PoolErrors() map[string]error {
	problems := make(map[string]error)
	for name := range c.Pools {
		if err := validatePool(c.ForPool(name), c.MaxSlots); err != nil {
			problems[name] = fmt.Errorf("pool %q: %w", name, err)
		}
	}
	return problems
}

func validatePool(p PoolConfig, maxSlots int) error {
	var problems []error
	check := func(ok bool, field, rule string) {
		if !ok {
			problems = append(problems, fmt.Errorf("%s: %s", field, rule))
		}
	}
	check(p.LowPct >= 0, "lowPct", "must be >= 0")
	check(p.HighPct >= 0, "highPct", "must be >= 0")
	check(p.LowPct <= p.HighPct, "lowPct/highPct", "lowPct must be <= highPct")
	check(p.MinFree >= 0, "minFree", "must be >= 0")
	check(p.MinBand >= 1, "minBand", "must be >= 1")
	check(p.MaxOpenRunsPerPool >= 1 && p.MaxOpenRunsPerPool <= maxSlots && p.MaxOpenRunsPerPool <= 100,
		"maxOpenRunsPerPool", "must be between 1 and maxSlots and <= 100")
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"probeInterval", p.ProbeInterval}, {"drawGrace", p.DrawGrace}, {"failureMemory", p.FailureMemory}, {"settlingMax", p.SettlingMax},
	} {
		check(field.value > 0, field.name, "must be > 0")
	}
	check(p.DrawGrace < p.ProbeInterval, "drawGrace", "must be < probeInterval")
	return errors.Join(problems...)
}
