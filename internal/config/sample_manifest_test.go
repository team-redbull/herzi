package config_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"sigs.k8s.io/yaml"
)

func TestShippedSampleLoads(t *testing.T) {
	data, err := chartConfig("hack/chart/example-values.yaml")
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
	if !cfg.Defaults.DryRunGrow {
		t.Fatal("defaults must start every pool in dry-run")
	}
}

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
	data, err := chartConfig("hack/chart/example-values.yaml")
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

func TestDefaultBufferIsTenPercentWithAFloorOfTwo(t *testing.T) {
	defaults := config.Defaults()
	if defaults.HighPct != 10 || defaults.MinFree != 2 {
		t.Fatalf("defaults are highPct %d minFree %d, want 10 and 2", defaults.HighPct, defaults.MinFree)
	}
	data, err := chartConfig("hack/chart/example-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults != defaults {
		t.Fatalf("the shipped sample's defaults differ from config.Defaults():\n sample %+v\n code   %+v", cfg.Defaults, defaults)
	}
}

func chartConfig(name string) ([]byte, error) {
	data, err := os.ReadFile("../../" + name)
	if err != nil {
		return nil, err
	}
	var values struct {
		Config map[string]any `json:"config"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return yaml.Marshal(values.Config)
}

func TestChartDefaultsMatchCode(t *testing.T) {
	data, err := chartConfig("charts/herzi/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	pool, ok := document["defaults"].(map[string]any)
	if !ok {
		t.Fatal("config.defaults must be a mapping")
	}
	assertChartFields(t, pool, config.Defaults(), nil)
	assertChartFields(t, document, config.DefaultHub(), map[string]bool{
		"hubName": true, "installURL": true, "temporalAddress": true,
	})
	if pools, ok := document["pools"].(map[string]any); !ok || len(pools) != 0 {
		t.Fatal("chart must not opt any pools in by default")
	}
}

func assertChartFields(t *testing.T, actual map[string]any, defaults any, exempt map[string]bool) {
	t.Helper()
	value := reflect.ValueOf(defaults)
	typ := value.Type()
	for i := range value.NumField() {
		field := typ.Field(i)
		key := field.Tag.Get("json")
		got, exists := actual[key]
		if !exists {
			t.Errorf("chart is missing default %s", key)
			continue
		}
		if exempt[key] {
			if got != "" {
				t.Errorf("%s must be installer-selected and empty by default", key)
			}
			continue
		}
		want := value.Field(i).Interface()
		if duration, ok := want.(time.Duration); ok {
			text, ok := got.(string)
			if !ok {
				t.Errorf("%s must be a duration string", key)
				continue
			}
			parsed, err := time.ParseDuration(text)
			if err != nil || parsed != duration {
				t.Errorf("%s = %v, want %v", key, got, duration)
			}
			continue
		}
		if number, ok := want.(int); ok {
			if got != float64(number) {
				t.Errorf("%s = %v, want %d", key, got, number)
			}
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}
