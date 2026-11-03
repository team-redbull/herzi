package config

import (
	"fmt"
	"slices"
)

// DryRunSummary returns the default dry-run setting and, sorted, the pools whose own setting differs from it.
func (c Config) DryRunSummary() (def bool, differing []string) {
	def = c.Defaults.DryRunGrow
	for name := range c.Pools {
		if c.ForPool(name).DryRunGrow != def {
			differing = append(differing, name)
		}
	}
	slices.Sort(differing)
	return def, differing
}

// DryRunChanges says in words how dry-run behaviour differs between two configurations. A pool without its own
// setting follows the default, so it is covered by the default line.
func DryRunChanges(prev, next Config) []string {
	var changes []string
	switch {
	case prev.Defaults.DryRunGrow == next.Defaults.DryRunGrow:
	case next.Defaults.DryRunGrow:
		changes = append(changes, "default dry run turned ON: pools without their own setting now only log \"Would install\"")
	default:
		changes = append(changes, "default dry run turned OFF: pools without their own setting now place REAL install orders")
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
		if prev.Pools[name].DryRunGrow == nil && next.Pools[name].DryRunGrow == nil {
			continue
		}
		before, after := prev.ForPool(name).DryRunGrow, next.ForPool(name).DryRunGrow
		if before == after {
			continue
		}
		state := "OFF: it now places REAL install orders"
		if after {
			state = "ON: it now only logs \"Would install\""
		}
		changes = append(changes, fmt.Sprintf("pool %s: dry run turned %s", name, state))
	}
	return changes
}
