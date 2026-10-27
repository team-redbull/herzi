package config_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/pipeline"
)

var _ pipeline.Settings = config.Config{}

const minimal = `hubName: bat-yam-01
installURL: https://workflows.example.com/install
activityQueue: activities
workflowQueue: workflows
segmentQueue: segments
`

func ptr[T any](v T) *T { return new(v) }
func validConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func requireError(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, text := range contains {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("error %q does not contain %q", err, text)
		}
	}
}

func TestLoad(t *testing.T) {
	tests := []struct{ name, data, problem string }{
		{"minimal", minimal, ""},
		{"unknown top", minimal + "typo: 1\n", "typo"},
		{"hub nesting", minimal + "hub: {}\n", "hub"},
		{"unknown defaults", minimal + "defaults: {minfree: 1}\n", "minfree"},
		{"unknown pool", minimal + "pools: {test: {minfree: 1}}\n", "minfree"},
		{"wrong case", minimal + "Interval: 1m\n", "Interval"},
		{"pool hub key", minimal + "pools: {test: {interval: 1m}}\n", "interval"},
		{"bad duration", minimal + "interval: nonsense\n", "interval"},
		{"negative duration", minimal + "interval: -1s\n", "interval"},
		{"numeric duration", minimal + "interval: 120\n", "interval"},
		{"float duration", minimal + "interval: 1.5\n", "interval"},
		{"bool duration", minimal + "interval: true\n", "interval"},
		{"object duration", minimal + "interval: {}\n", "interval"},
		{"zero duration", minimal + "interval: 0s\n", "interval"},
		{"empty duration", minimal + "interval: ''\n", "interval"},
		{"empty file", "", "decode config"},
		{"comments only", "# empty\n", "decode config"},
		{"empty object", "{}", "hubName"},
		{"null", "null", "hubName"},
		{"empty pool", minimal + "pools: {test: {}}\n", ""},
		{"missing defaults fields", minimal + "defaults: {minFree: 0}\n", ""},
		{"yaml syntax", minimal + "pools: [\n", "decode config"},
		{"wrong scalar", minimal + "maxSlots: nope\n", "into int"},
		{"duplicate key", minimal + "maxSlots: 100\nmaxSlots: 99\n", "maxSlots"},
		{"second document", minimal + "---\n{}\n", "exactly one"},
		{"bad second document", minimal + "---\n[\n", "trailing document"},
		{"noncanonical input", strings.ReplaceAll(minimal, "bat-yam-01", "  bat-yam-01  "), ""},
		{"blank hub", strings.ReplaceAll(minimal, "bat-yam-01", "'   '"), "hubName"},
		{"prefixed input", strings.ReplaceAll(minimal, "bat-yam-01", "ocp4-bat-yam-01"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := config.Load([]byte(tt.data))
			if tt.problem != "" {
				requireError(t, err, tt.problem)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.HubName != "ocp4-bat-yam-01" {
				t.Fatalf("hubName = %q", c.HubName)
			}
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if tt.name == "minimal" {
				hub := config.DefaultHub()
				hub.HubName = c.HubName
				hub.InstallURL = c.InstallURL
				hub.ActivityQueue = "activities"
				hub.WorkflowQueue = "workflows"
				hub.SegmentQueue = "segments"
				if diff := cmp.Diff(hub, c.HubConfig); diff != "" {
					t.Fatal(diff)
				}
				if diff := cmp.Diff(config.Defaults(), c.Defaults); diff != "" {
					t.Fatal(diff)
				}
			}
			if tt.name == "empty pool" && (!c.Manages("test") || c.ForPool("test") != config.Defaults()) {
				t.Fatal("empty pool must be managed with defaults")
			}
			if tt.name == "missing defaults fields" && (c.Defaults.MinFree != 0 || c.Defaults.ProbeInterval != 20*time.Minute) {
				t.Fatal("partial defaults not merged")
			}
		})
	}
}

func TestLoadDurationFields(t *testing.T) {
	for _, path := range []struct{ prefix, field string }{
		{"", "interval"}, {"", "reconcileTimeout"}, {"", "ambiguousWait"}, {"", "stuckAfter"},
		{"defaults", "probeInterval"}, {"defaults", "drawGrace"}, {"defaults", "failureMemory"}, {"defaults", "settlingMax"},
		{"pools:\n  test", "probeInterval"}, {"pools:\n  test", "drawGrace"}, {"pools:\n  test", "failureMemory"}, {"pools:\n  test", "settlingMax"},
	} {
		for _, value := range []string{"garbage", "-1s", "123", "0s"} {
			t.Run(path.prefix+path.field+value, func(t *testing.T) {
				suffix := path.field + ": " + value + "\n"
				if path.prefix == "defaults" {
					suffix = "defaults:\n  " + suffix
				} else if path.prefix != "" {
					suffix = path.prefix + ":\n    " + suffix
				}
				c, err := config.Load([]byte(minimal + suffix))
				if strings.HasPrefix(path.prefix, "pools") && value == "0s" {
					if err != nil {
						t.Fatal(err)
					}
					requireError(t, c.PoolErrors()["test"], path.field)
					return
				}
				requireError(t, err, path.field)
				if strings.HasPrefix(path.prefix, "pools") {
					requireError(t, err, "test")
				}
				if path.prefix == "defaults" {
					requireError(t, err, "defaults")
				}
			})
		}
	}
}

func TestGoldenSample(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	hub := config.DefaultHub()
	hub.HubName = "ocp4-bat-yam-01"
	hub.InstallURL = "https://workflows-api.example.com/workflows/server-lifecycle/install-server"
	hub.TemporalAddress = "temporal.example.com:7233"
	hub.TemporalNamespace = "default"
	hub.ActivityQueue = "server-lifecycle-activity-ocp4-bat-yam-01"
	hub.WorkflowQueue = "install-server-workflow"
	hub.SegmentQueue = "segment-lifecycle-activity"
	want := config.Config{HubConfig: hub, Defaults: config.PoolConfig{
		HighPct: 30, MinFree: 2, MaxOpenRunsPerPool: 15, ProbeInterval: 20 * time.Minute,
		DrawGrace: 2 * time.Minute, FailureMemory: 6 * time.Hour, SettlingMax: time.Hour,
	}, Pools: map[string]config.PoolOverride{
		"32-128-zone-1": {MinFree: new(5), MaxOpenRunsPerPool: new(8)},
		"16-64-zone-2":  {Paused: new(true)}, "8-32-zone-1": {HighPct: new(25), DryRunGrow: new(true)},
	}}
	if diff := cmp.Diff(want, c); diff != "" {
		t.Fatal(diff)
	}
}

func TestMergeEveryField(t *testing.T) {
	c, err := config.Load([]byte(minimal + `defaults:
  dryRunGrow: true
  paused: true
pools:
  test:
    highPct: 0
    minFree: 0
    maxOpenRunsPerPool: 1
    probeInterval: 1h
    drawGrace: 1s
    failureMemory: 1h
    settlingMax: 1m
    dryRunGrow: false
    paused: false
`))
	if err != nil {
		t.Fatal(err)
	}
	want := config.PoolConfig{HighPct: 0, MinFree: 0, MaxOpenRunsPerPool: 1,
		ProbeInterval: time.Hour, DrawGrace: time.Second, FailureMemory: time.Hour, SettlingMax: time.Minute}
	if diff := cmp.Diff(want, c.ForPool("test")); diff != "" {
		t.Fatal(diff)
	}
	if c.Manages("absent") || !c.Manages("test") {
		t.Fatal("incorrect managed membership")
	}
	if c.ForPool("absent") != c.Defaults {
		t.Fatal("unlisted pool did not get defaults")
	}
	p := c.ForPool("test")
	p.MinFree = 999
	if c.ForPool("test").MinFree != 0 {
		t.Fatal("ForPool must return a value copy")
	}
}

func TestValidatePoolRules(t *testing.T) {
	tests := []struct {
		field     string
		bad, good any
	}{
		{"HighPct", -1, 30},
		{"MinFree", -1, 0}, {"MaxOpenRunsPerPool", 0, 1},
		{"MaxOpenRunsPerPool", 101, 100},
		{"ProbeInterval", time.Duration(0), time.Minute * 3},
		{"DrawGrace", time.Duration(0), time.Second}, {"FailureMemory", time.Duration(0), time.Hour},
		{"SettlingMax", time.Duration(0), time.Hour}, {"DrawGrace", 20 * time.Minute, 19 * time.Minute},
	}
	for _, tt := range tests {
		for _, where := range []string{"defaults", "pool"} {
			t.Run(fmt.Sprintf("%s/%s=%v", where, tt.field, tt.bad), func(t *testing.T) {
				for _, pass := range []bool{false, true} {
					c := validConfig(t)
					value := tt.bad
					if pass {
						value = tt.good
					}
					if where == "defaults" {
						reflect.ValueOf(&c.Defaults).Elem().FieldByName(tt.field).Set(reflect.ValueOf(value))
					} else {
						o := config.PoolOverride{}
						v := reflect.New(reflect.TypeOf(value))
						v.Elem().Set(reflect.ValueOf(value))
						reflect.ValueOf(&o).Elem().FieldByName(tt.field).Set(v)
						c.Pools = map[string]config.PoolOverride{"broken": o, "healthy": {}}
					}
					err := c.Validate()
					if pass {
						if err != nil {
							t.Fatal(err)
						}
						continue
					}
					field := strings.ToLower(tt.field[:1]) + tt.field[1:]
					requireError(t, err, field)
					if where == "defaults" {
						requireError(t, err, "defaults")
					} else {
						requireError(t, err, "broken")
						pe := c.PoolErrors()
						if len(pe) != 1 || pe["broken"] == nil {
							t.Fatalf("PoolErrors: %v", pe)
						}
					}
				}
			})
		}
	}

	c := validConfig(t)
	c.MaxSlots = 14
	requireError(t, c.Validate(), "maxOpenRunsPerPool")
	c.Defaults.MaxOpenRunsPerPool = 14
	requireError(t, c.Validate(), "maxSlots", "exactly 100")

	c.MaxSlots = 200
	c.Defaults.MaxOpenRunsPerPool = 101
	requireError(t, c.Validate(), "maxSlots", "maxOpenRunsPerPool")
}

func TestValidateHubRules(t *testing.T) {
	tests := []struct {
		field     string
		bad, good any
	}{
		{"HubName", "", "ocp4-hub"}, {"HubName", "hub", "ocp4-hub"},
		{"InstallURL", "relative/path", "http://example.com/path"}, {"InstallURL", "ftp://example.com", "https://example.com"},
		{"InstallURL", "http:///path", "http://example.com"}, {"InstallURL", "https://%zz", "https://example.com"},
		{"Interval", time.Duration(0), time.Second}, {"ReconcileTimeout", time.Duration(-1), time.Second},
		{"AmbiguousWait", time.Duration(0), time.Second}, {"StuckAfter", time.Duration(0), time.Second},
		{"MaxSlots", 0, 100}, {"MaxSlots", 101, 100},
		{"AgentNamespace", "  ", "inventory"}, {"ActivityQueue", "", "activity"},
		{"WorkflowQueue", "", "workflow"}, {"SegmentQueue", "", "segment"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s=%v", tt.field, tt.bad), func(t *testing.T) {
			c := validConfig(t)
			field := reflect.ValueOf(&c.HubConfig).Elem().FieldByName(tt.field)
			field.Set(reflect.ValueOf(tt.bad))
			requireError(t, c.Validate(), strings.ToLower(tt.field[:1])+tt.field[1:])
			field.Set(reflect.ValueOf(tt.good))
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	c := validConfig(t)
	c.MaxSlots = 1
	c.Defaults.MaxOpenRunsPerPool = 1
	requireError(t, c.Validate(), "maxSlots")
}

func TestValidationCollectsAllProblems(t *testing.T) {
	c := validConfig(t)
	c.HubName = ""
	c.Interval = 0
	c.Defaults.MinFree = -1
	c.Pools = map[string]config.PoolOverride{"z": {HighPct: new(-2)}, "a": {MinFree: new(-3)}}
	err := c.Validate()
	requireError(t, err, "hubName", "interval", "defaults", "minFree", `pool "z"`, "highPct", `pool "a"`, "minFree")
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) < 3 {
		t.Fatal("must join every problem")
	}
	if len(c.PoolErrors()) != 2 {
		t.Fatal("invalid inherited defaults must affect both pools")
	}
}

func TestLoadHubOverrides(t *testing.T) {
	c, err := config.Load([]byte(`hubName: '  ocp4-custom  '
installURL: http://example.com/install
interval: 3m
reconcileTimeout: 90s
agentNamespace: custom
maxSlots: 100
ambiguousWait: 10m
stuckAfter: 3h
temporalAddress: localhost:7233
temporalNamespace: custom
activityQueue: a
workflowQueue: w
segmentQueue: s
`))
	if err != nil {
		t.Fatal(err)
	}
	want := config.HubConfig{HubName: "ocp4-custom", InstallURL: "http://example.com/install", Interval: 3 * time.Minute,
		ReconcileTimeout: 90 * time.Second, AgentNamespace: "custom", MaxSlots: 100, AmbiguousWait: 10 * time.Minute, StuckAfter: 3 * time.Hour,
		TemporalAddress: "localhost:7233", TemporalNamespace: "custom", ActivityQueue: "a", WorkflowQueue: "w", SegmentQueue: "s"}
	if diff := cmp.Diff(want, c.HubConfig); diff != "" {
		t.Fatal(diff)
	}
}

func TestRemovedPoolKeysRejected(t *testing.T) {
	for _, path := range []string{"defaults", "pools.test"} {
		for _, key := range []string{"lowPct", "minBand"} {
			for _, value := range []string{"0", "null", "15"} {
				t.Run(path+"."+key+"="+value, func(t *testing.T) {
					suffix := fmt.Sprintf("defaults: {%s: %s}\n", key, value)
					if path == "pools.test" {
						suffix = fmt.Sprintf("pools: {test: {%s: %s}}\n", key, value)
					}
					_, err := config.Load([]byte(minimal + suffix))
					message := path + "." + key + " was removed: use highPct as the single target buffer percentage; delete lowPct"
					if key == "minBand" {
						message = path + "." + key + " was removed: the controller no longer has a ceiling; delete minBand"
					}
					if err == nil || err.Error() != message {
						t.Fatalf("got %v, want %s", err, message)
					}
				})
			}
		}
	}
}
