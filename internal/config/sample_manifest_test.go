package config_test

import (
	"os"
	"testing"

	"github.com/team-redbull/herzi/internal/config"
)

// The sample that ships in the kustomize tree must always load through the real strict loader,
// otherwise `make deploy` would produce a pod that rejects its own configuration.
func TestShippedSampleLoads(t *testing.T) {
	data, err := os.ReadFile("../../config/samples/herzi-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(data)
	if err != nil {
		t.Fatalf("shipped sample does not load: %v", err)
	}
	if len(cfg.Pools) != 3 {
		t.Fatalf("want 3 example pools, got %d", len(cfg.Pools))
	}
	if !cfg.ForPool("32-128-zone-1").DryRunGrow {
		t.Fatal("the example pools must start in dry-run")
	}
	if cfg.ForPool("32-128-zone-1").MinFree != 5 || cfg.ForPool("8-32-zone-1").HighPct != 25 {
		t.Fatal("pool overrides were not merged")
	}
	if cfg.Defaults.DryRunGrow {
		t.Fatal("defaults must not force dry-run on every pool")
	}
}

// The three queue names must equal the workflows repo's (shared/consts.py), or every pool is blocked.
func TestQueueNamesDefaultToTheWorkflowsRepoNames(t *testing.T) {
	cfg, err := config.Load([]byte("hubName: example\ninstallURL: https://w.example/x\ntemporalAddress: t:7233\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubName != "ocp4-example" {
		t.Fatalf("hub name not canonicalised: %q", cfg.HubName)
	}
	if cfg.ActivityQueue != "server-lifecycle-activity-ocp4-example" {
		t.Fatalf("activity queue must derive from the hub: %q", cfg.ActivityQueue)
	}
	if cfg.WorkflowQueue != "install-server-workflow" || cfg.SegmentQueue != "segment-lifecycle-activity" {
		t.Fatalf("queue defaults wrong: %q %q", cfg.WorkflowQueue, cfg.SegmentQueue)
	}
}

func TestTheShippedSampleUsesTheRealQueueNames(t *testing.T) {
	data, err := os.ReadFile("../../config/samples/herzi-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SegmentQueue != "segment-lifecycle-activity" || cfg.WorkflowQueue != "install-server-workflow" {
		t.Fatalf("sample queue names are wrong: %q %q", cfg.SegmentQueue, cfg.WorkflowQueue)
	}
}
