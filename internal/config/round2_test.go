package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
)

func TestF3AllHundredSlotsMustRemainVisible(t *testing.T) {
	c := validConfig(t)
	for _, n := range []int{1, 15, 99, 101} {
		c.MaxSlots = n
		c.Defaults.MaxOpenRunsPerPool = min(n, 15)
		require.ErrorContains(t, c.Validate(), "exactly 100 slots")
	}
	c.MaxSlots = 100
	c.Defaults.MaxOpenRunsPerPool = 100
	require.NoError(t, c.Validate())
}

func TestF7TimingRelationships(t *testing.T) {
	for _, tc := range []struct {
		field  string
		change func(*config.Config, bool)
	}{
		{"failureMemory", func(c *config.Config, pass bool) {
			c.Defaults.FailureMemory = 40 * time.Minute
			if !pass {
				c.Defaults.FailureMemory -= time.Nanosecond
			}
		}},
		{"drawGrace", func(c *config.Config, pass bool) {
			c.Defaults.DrawGrace = 20 * time.Minute
			if pass {
				c.Defaults.DrawGrace -= time.Nanosecond
			}
		}},
		{"settlingMax", func(c *config.Config, pass bool) {
			c.Defaults.SettlingMax = 20 * time.Minute
			if !pass {
				c.Defaults.SettlingMax -= time.Nanosecond
			}
		}},
		{"interval", func(c *config.Config, pass bool) {
			c.Interval = 20 * time.Minute
			if !pass {
				c.Interval += time.Nanosecond
			}
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			for _, pass := range []bool{false, true} {
				c := validConfig(t)
				tc.change(&c, pass)
				if pass {
					require.NoError(t, c.Validate())
				} else {
					require.ErrorContains(t, c.Validate(), tc.field)
				}
			}
		})
	}
	_, err := config.Load([]byte(minimal + "defaults: {failureMemory: 1m, probeInterval: 20m}\n"))
	require.ErrorContains(t, err, "failureMemory")
}

func TestF10InvalidPoolDoesNotRejectPauseUpdate(t *testing.T) {
	data := []byte(minimal + "pools: {a: {paused: true}, b: {minBand: 0}, c: {}}\n")
	c, err := config.Load(data)
	require.NoError(t, err)
	require.True(t, c.ForPool("a").Paused)
	require.Len(t, c.PoolErrors(), 1)
	require.ErrorContains(t, c.PoolErrors()["b"], "minBand")
	s := config.NewStore(validConfig(t))
	require.NoError(t, s.Update(data))
	require.True(t, s.Snapshot().ForPool("a").Paused)
	require.True(t, s.Valid())
}
