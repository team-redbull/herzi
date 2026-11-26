package config_test

import (
	"reflect"
	"testing"

	"github.com/team-redbull/herzi/internal/config"
)

func loadDry(t *testing.T, yaml string) config.Config {
	t.Helper()
	c, err := config.Load([]byte(ignoreBase + yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDryRunSummary(t *testing.T) {
	def, differing := loadDry(t, "").DryRunSummary()
	if !def || len(differing) != 0 {
		t.Fatalf("default config is dry run with no exceptions, got %v %v", def, differing)
	}
	def, differing = loadDry(t, "pools:\n  live-b: {dryRunGrow: false}\n  live-a: {dryRunGrow: false}\n  same: {dryRunGrow: true}\n  tuned: {minFree: 5}\n").DryRunSummary()
	if !def || !reflect.DeepEqual(differing, []string{"live-a", "live-b"}) {
		t.Fatalf("got %v %v", def, differing)
	}
	def, differing = loadDry(t, "defaults: {dryRunGrow: false}\npools:\n  rehearse: {dryRunGrow: true}\n").DryRunSummary()
	if def || !reflect.DeepEqual(differing, []string{"rehearse"}) {
		t.Fatalf("got %v %v", def, differing)
	}
}

func TestDryRunChanges(t *testing.T) {
	dry := loadDry(t, "")
	live := loadDry(t, "defaults: {dryRunGrow: false}\n")
	if got := config.DryRunChanges(dry, dry); len(got) != 0 {
		t.Fatalf("no change, got %v", got)
	}
	if got := config.DryRunChanges(dry, live); len(got) != 1 || got[0] != "default dry run turned OFF: pools without their own setting now place REAL install orders" {
		t.Fatalf("got %v", got)
	}
	if got := config.DryRunChanges(live, dry); len(got) != 1 || got[0] != `default dry run turned ON: pools without their own setting now only log "Would install"` {
		t.Fatalf("got %v", got)
	}
	one := loadDry(t, "pools:\n  big: {dryRunGrow: false}\n")
	got := config.DryRunChanges(dry, one)
	if len(got) != 1 || got[0] != "pool big: dry run turned OFF: it now places REAL install orders" {
		t.Fatalf("got %v", got)
	}
	if got := config.DryRunChanges(one, dry); len(got) != 1 || got[0] != `pool big: dry run turned ON: it now only logs "Would install"` {
		t.Fatalf("removing the override puts the pool back in dry run, got %v", got)
	}
	if got := config.DryRunChanges(dry, loadDry(t, "pools:\n  tuned: {minFree: 5}\n")); len(got) != 0 {
		t.Fatalf("an unrelated override is not a dry-run change, got %v", got)
	}
	both := loadDry(t, "defaults: {dryRunGrow: false}\npools:\n  big: {dryRunGrow: false}\n")
	if got := config.DryRunChanges(one, both); len(got) != 1 {
		t.Fatalf("only the default changed, the explicit pool stayed live: %v", got)
	}
}

func TestRemoveDryRun(t *testing.T) {
	dry := loadDry(t, "")
	if def, differing := dry.RemoveDryRunSummary(); !def || len(differing) != 0 {
		t.Fatalf("removal starts in dry run everywhere, got %v %v", def, differing)
	}
	excluded := loadDry(t, "pools:\n  gone: {exclude: true, dryRunRemove: false}\n")
	if def, differing := excluded.RemoveDryRunSummary(); !def || !reflect.DeepEqual(differing, []string{"gone"}) {
		t.Fatalf("an excluded pool may still remove for real, got %v %v", def, differing)
	}
	if got := config.DryRunChanges(dry, excluded); len(got) != 1 || got[0] != "pool gone: removal dry run turned OFF: it now REALLY removes used servers from the MCE" {
		t.Fatalf("got %v", got)
	}
	live := loadDry(t, "defaults: {dryRunRemove: false}\n")
	if got := config.DryRunChanges(dry, live); len(got) != 1 || got[0] != "default removal dry run turned OFF: pools without their own setting now REALLY remove used servers from the MCE" {
		t.Fatalf("got %v", got)
	}
	if got := config.DryRunChanges(live, dry); len(got) != 1 || got[0] != `default removal dry run turned ON: pools without their own setting now only log "Would remove"` {
		t.Fatalf("got %v", got)
	}
	if got := config.DryRunChanges(dry, loadDry(t, "defaults: {dryRunGrow: false, dryRunRemove: false}\n")); len(got) != 2 {
		t.Fatalf("each setting reports its own change, got %v", got)
	}
}
