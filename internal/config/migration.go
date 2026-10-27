package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RemovedKeyError omits YAML values so its message is safe for reload diagnostics.
type RemovedKeyError struct{ path, key string }

func (e *RemovedKeyError) Error() string {
	if e.key == "lowPct" {
		return e.path + " was removed: use highPct as the single target buffer percentage; delete lowPct"
	}
	return e.path + " was removed: the controller no longer has a ceiling; delete minBand"
}

func removedPoolKeys(data []byte) error {
	var doc map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if err := removedKeysAt(doc["defaults"], "defaults"); err != nil {
		return err
	}
	pools, _ := doc["pools"].(map[string]any)
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := removedKeysAt(pools[name], "pools."+safePoolName(name)); err != nil {
			return err
		}
	}
	return nil
}

func removedKeysAt(value any, path string) error {
	fields, _ := value.(map[string]any)
	for _, key := range []string{"lowPct", "minBand"} {
		if _, present := fields[key]; present {
			return &RemovedKeyError{path: path + "." + key, key: key}
		}
	}
	return nil
}

func safePoolName(name string) string {
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '.'
	}) >= 0 {
		return "<invalid>"
	}
	return name
}
