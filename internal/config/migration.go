package config

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RemovedKeyError identifies an obsolete configuration key without retaining YAML values.
// Its message is safe to include in reload diagnostics.
type RemovedKeyError struct{ path, key string }

func (e *RemovedKeyError) Error() string {
	if e.key == "lowPct" {
		return e.path + " was removed: use highPct as the single target buffer percentage; delete lowPct"
	}
	return e.path + " was removed: the controller no longer has a ceiling; delete minBand"
}

func removedPoolKeys(data []byte) error {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		switch key {
		case "defaults":
			if err := removedKeysAt(value, "defaults", make(map[*yaml.Node]bool)); err != nil {
				return err
			}
		case "pools":
			if value.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(value.Content); j += 2 {
				name := safePoolName(value.Content[j].Value)
				if err := removedKeysAt(value.Content[j+1], "pools."+name, make(map[*yaml.Node]bool)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func removedKeysAt(node *yaml.Node, path string, seen map[*yaml.Node]bool) error {
	if node == nil || seen[node] {
		return nil
	}
	seen[node] = true
	if node.Kind == yaml.AliasNode {
		return removedKeysAt(node.Alias, path, seen)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := removedKeysAt(child, path, seen); err != nil {
				return err
			}
		}
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key == "lowPct" || key == "minBand" {
			return &RemovedKeyError{path: path + "." + key, key: key}
		}
		if key == "<<" {
			if err := removedKeysAt(node.Content[i+1], path, seen); err != nil {
				return err
			}
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
