package config_test

import (
	"strings"
	"testing"

	"github.com/team-redbull/herzi/internal/config"
)

const ignoreBase = "mceName: hub\ninstallURL: http://api/install\n"

func TestManagesEverythingExceptExcludedOrLabelled(t *testing.T) {
	c, err := config.Load([]byte(ignoreBase + "pools:\n  skipped: {exclude: true}\n  kept: {exclude: false}\n  tuned: {minFree: 5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"never-listed", nil, true},
		{"tuned", nil, true},
		{"kept", nil, true},
		{"skipped", nil, false},
		{"never-listed", map[string]string{config.DefaultIgnoreLabel: ""}, false},
		{"never-listed", map[string]string{config.DefaultIgnoreLabel: "false"}, false},
		{"never-listed", map[string]string{"other": ""}, true},
	} {
		if got := c.Manages(tc.name, tc.labels); got != tc.want {
			t.Errorf("Manages(%q, %v) = %v, want %v", tc.name, tc.labels, got, tc.want)
		}
	}
}

func TestIgnoreLabelIsConfigurableAndValidated(t *testing.T) {
	c, err := config.Load([]byte(ignoreBase + "ignoreLabel: example.com/skip\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Manages("p", map[string]string{"example.com/skip": ""}) || !c.Manages("p", map[string]string{config.DefaultIgnoreLabel: ""}) {
		t.Fatal("only the configured label ignores")
	}
	for _, bad := range []string{"has space", "-bad", "a/b/c", strings.Repeat("x", 64)} {
		if _, err := config.Load([]byte(ignoreBase + "ignoreLabel: \"" + bad + "\"\n")); err == nil || !strings.Contains(err.Error(), "ignoreLabel") {
			t.Errorf("ignoreLabel %q must be rejected, got %v", bad, err)
		}
	}
}

func TestExcludeIsNotAllowedInDefaults(t *testing.T) {
	_, err := config.Load([]byte(ignoreBase + "defaults: {exclude: true}\n"))
	if err == nil || !strings.Contains(err.Error(), "defaults.exclude") {
		t.Fatalf("want a defaults.exclude error, got %v", err)
	}
}
