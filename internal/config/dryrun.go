package config

import (
	"fmt"
	"slices"
)

// dryRunSetting phrases one dry-run flag; "pools ..." takes the plural verb and "it ..." the singular.
type dryRunSetting struct {
	merged                                   func(PoolConfig) bool
	written                                  func(PoolOverride) *bool
	name, poolsOn, poolsOff, poolOn, poolOff string
}

var (
	growDryRun = dryRunSetting{
		merged:  func(p PoolConfig) bool { return p.DryRunGrow },
		written: func(o PoolOverride) *bool { return o.DryRunGrow },
		name:    "dry run",
		poolsOn: `only log "Would install"`, poolsOff: "place REAL install orders",
		poolOn: `only logs "Would install"`, poolOff: "places REAL install orders",
	}
	removeDryRun = dryRunSetting{
		merged:  func(p PoolConfig) bool { return p.DryRunRemove },
		written: func(o PoolOverride) *bool { return o.DryRunRemove },
		name:    "removal dry run",
		poolsOn: `only log "Would remove"`, poolsOff: "REALLY remove used servers from the MCE",
		poolOn: `only logs "Would remove"`, poolOff: "REALLY removes used servers from the MCE",
	}
)

// DryRunSummary returns the default dry-run setting and, sorted, the pools whose own setting differs from it.
func (c Config) DryRunSummary() (def bool, differing []string) { return c.summary(growDryRun) }

// RemoveDryRunSummary is DryRunSummary for dryRunRemove.
func (c Config) RemoveDryRunSummary() (def bool, differing []string) { return c.summary(removeDryRun) }

func (c Config) summary(s dryRunSetting) (def bool, differing []string) {
	def = s.merged(c.Defaults)
	for name := range c.Pools {
		if s.merged(c.ForPool(name)) != def {
			differing = append(differing, name)
		}
	}
	slices.Sort(differing)
	return def, differing
}

// DryRunChanges says in words how dry-run behaviour differs between two configurations. A pool without its own
// setting follows the default, so it is covered by the default line.
func DryRunChanges(prev, next Config) []string {
	return append(dryRunChanges(prev, next, growDryRun), dryRunChanges(prev, next, removeDryRun)...)
}

func dryRunChanges(prev, next Config, s dryRunSetting) []string {
	var changes []string
	switch before, after := s.merged(prev.Defaults), s.merged(next.Defaults); {
	case before == after:
	case after:
		changes = append(changes, fmt.Sprintf("default %s turned ON: pools without their own setting now %s", s.name, s.poolsOn))
	default:
		changes = append(changes, fmt.Sprintf("default %s turned OFF: pools without their own setting now %s", s.name, s.poolsOff))
	}
	names := make([]string, 0, len(prev.Pools)+len(next.Pools))
	for name := range prev.Pools {
		names = append(names, name)
	}
	for name := range next.Pools {
		if _, seen := prev.Pools[name]; !seen {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if s.written(prev.Pools[name]) == nil && s.written(next.Pools[name]) == nil {
			continue
		}
		before, after := s.merged(prev.ForPool(name)), s.merged(next.ForPool(name))
		if before == after {
			continue
		}
		state := "OFF: it now " + s.poolOff
		if after {
			state = "ON: it now " + s.poolOn
		}
		changes = append(changes, fmt.Sprintf("pool %s: %s turned %s", name, s.name, state))
	}
	return changes
}
