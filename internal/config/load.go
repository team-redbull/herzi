package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// durationText preserves the YAML scalar type so numbers cannot silently become strings.
// Parsing is deferred so errors include the complete hub/default/pool field path.
type durationText struct {
	value    string
	isString bool
}

func (d *durationText) UnmarshalYAML(n *yaml.Node) error {
	d.value, d.isString = n.Value, n.Kind == yaml.ScalarNode && n.Tag == "!!str"
	return nil
}

type poolInput struct {
	LowPct             *int          `yaml:"lowPct"`
	HighPct            *int          `yaml:"highPct"`
	MinFree            *int          `yaml:"minFree"`
	MinBand            *int          `yaml:"minBand"`
	MaxOpenRunsPerPool *int          `yaml:"maxOpenRunsPerPool"`
	ProbeInterval      *durationText `yaml:"probeInterval"`
	DrawGrace          *durationText `yaml:"drawGrace"`
	FailureMemory      *durationText `yaml:"failureMemory"`
	SettlingMax        *durationText `yaml:"settlingMax"`
	DryRunGrow         *bool         `yaml:"dryRunGrow"`
	Paused             *bool         `yaml:"paused"`
}

type configInput struct {
	Interval          *durationText        `yaml:"interval"`
	ReconcileTimeout  *durationText        `yaml:"reconcileTimeout"`
	HubName           *string              `yaml:"hubName"`
	AgentNamespace    *string              `yaml:"agentNamespace"`
	InstallURL        *string              `yaml:"installURL"`
	MaxSlots          *int                 `yaml:"maxSlots"`
	AmbiguousWait     *durationText        `yaml:"ambiguousWait"`
	StuckAfter        *durationText        `yaml:"stuckAfter"`
	TemporalAddress   *string              `yaml:"temporalAddress"`
	TemporalNamespace *string              `yaml:"temporalNamespace"`
	ActivityQueue     *string              `yaml:"activityQueue"`
	WorkflowQueue     *string              `yaml:"workflowQueue"`
	SegmentQueue      *string              `yaml:"segmentQueue"`
	Defaults          poolInput            `yaml:"defaults"`
	Pools             map[string]poolInput `yaml:"pools"`
}

// Load implements Part 2, Config: "An unknown key under a pool is an error".
// It validates the document, hub and defaults; merged pool errors remain available through PoolErrors.
func Load(data []byte) (Config, error) {
	var input configInput
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&input); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, fmt.Errorf("decode config trailing document: %w", err)
		}
		return Config{}, errors.New("config must contain exactly one YAML document")
	}
	c := Config{HubConfig: DefaultHub(), Defaults: Defaults(), Pools: make(map[string]PoolOverride, len(input.Pools))}
	var problems []error
	if input.Interval != nil {
		d, err := parseDuration(input.Interval, "interval")
		if err != nil {
			problems = append(problems, err)
		} else {
			c.Interval = *d
		}
	}
	if input.ReconcileTimeout != nil {
		d, err := parseDuration(input.ReconcileTimeout, "reconcileTimeout")
		if err != nil {
			problems = append(problems, err)
		} else {
			c.ReconcileTimeout = *d
		}
	}
	if input.HubName != nil {
		c.HubName = *input.HubName
	}
	if input.AgentNamespace != nil {
		c.AgentNamespace = *input.AgentNamespace
	}
	if input.InstallURL != nil {
		c.InstallURL = *input.InstallURL
	}
	if input.MaxSlots != nil {
		c.MaxSlots = *input.MaxSlots
	}
	if input.AmbiguousWait != nil {
		d, err := parseDuration(input.AmbiguousWait, "ambiguousWait")
		if err != nil {
			problems = append(problems, err)
		} else {
			c.AmbiguousWait = *d
		}
	}
	if input.StuckAfter != nil {
		d, err := parseDuration(input.StuckAfter, "stuckAfter")
		if err != nil {
			problems = append(problems, err)
		} else {
			c.StuckAfter = *d
		}
	}
	if input.TemporalAddress != nil {
		c.TemporalAddress = *input.TemporalAddress
	}
	if input.TemporalNamespace != nil {
		c.TemporalNamespace = *input.TemporalNamespace
	}
	if input.ActivityQueue != nil {
		c.ActivityQueue = *input.ActivityQueue
	}
	if input.WorkflowQueue != nil {
		c.WorkflowQueue = *input.WorkflowQueue
	}
	if input.SegmentQueue != nil {
		c.SegmentQueue = *input.SegmentQueue
	}
	c.HubName = strings.TrimSpace(c.HubName)
	if c.HubName != "" && !strings.HasPrefix(c.HubName, "ocp4-") {
		c.HubName = "ocp4-" + c.HubName
	}
	if c.ActivityQueue == "" && c.HubName != "" {
		c.ActivityQueue = activityQueuePrefix + c.HubName
	}
	o, err := input.Defaults.convert("defaults")
	if err != nil {
		problems = append(problems, err)
	}
	c.Defaults = merge(c.Defaults, o)
	for name, p := range input.Pools {
		o, err := p.convert(fmt.Sprintf("pool %q", name))
		if err != nil {
			problems = append(problems, err)
		}
		c.Pools[name] = o
	}
	problems = append(problems, c.validateDocument())
	if err := errors.Join(problems...); err != nil {
		return Config{}, err
	}
	return c, nil
}

func parseDuration(raw *durationText, field string) (*time.Duration, error) {
	if !raw.isString {
		return nil, fmt.Errorf("%s: duration must be a string, got %q", field, raw.value)
	}
	d, err := time.ParseDuration(raw.value)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid duration: %w", field, err)
	}
	if d < 0 {
		return nil, fmt.Errorf("%s: duration must not be negative", field)
	}
	return &d, nil
}

func (p poolInput) convert(path string) (PoolOverride, error) {
	o := PoolOverride{
		LowPct:             p.LowPct,
		HighPct:            p.HighPct,
		MinFree:            p.MinFree,
		MinBand:            p.MinBand,
		MaxOpenRunsPerPool: p.MaxOpenRunsPerPool,
		DryRunGrow:         p.DryRunGrow,
		Paused:             p.Paused,
	}
	var problems []error
	if p.ProbeInterval != nil {
		d, err := parseDuration(p.ProbeInterval, path+".probeInterval")
		if err != nil {
			problems = append(problems, err)
		} else {
			o.ProbeInterval = d
		}
	}
	if p.DrawGrace != nil {
		d, err := parseDuration(p.DrawGrace, path+".drawGrace")
		if err != nil {
			problems = append(problems, err)
		} else {
			o.DrawGrace = d
		}
	}
	if p.FailureMemory != nil {
		d, err := parseDuration(p.FailureMemory, path+".failureMemory")
		if err != nil {
			problems = append(problems, err)
		} else {
			o.FailureMemory = d
		}
	}
	if p.SettlingMax != nil {
		d, err := parseDuration(p.SettlingMax, path+".settlingMax")
		if err != nil {
			problems = append(problems, err)
		} else {
			o.SettlingMax = d
		}
	}
	return o, errors.Join(problems...)
}
