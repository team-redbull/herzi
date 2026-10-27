package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestRemovedKeyNodeInspection(t *testing.T) {
	for _, data := range []string{
		"defaults: &old {lowPct: null}\npools: {p: *old}",
		"template: &old {lowPct: 0}\npools: {p: *old}",
		"defaults: {<<: {minBand: 0}}",
		"pools: {p: {<<: [{highPct: 30}, {minBand: 0}]}}",
		"pools: {p: &old {<<: *old, lowPct: 0}}",
	} {
		var migration *RemovedKeyError
		require.ErrorAs(t, removedPoolKeys([]byte(data)), &migration)
	}
	for _, data := range []string{"null", "pools: []", "defaults: null", "defaults: {<<: [{highPct: 30}]}", "pools: {p: {highPct: 30}}"} {
		require.NoError(t, removedPoolKeys([]byte(data)))
	}
	require.Equal(t, "<invalid>", safePoolName(""))
	require.Equal(t, "<invalid>", safePoolName("bad\nname"))
	require.Equal(t, "pool-1.example", safePoolName("pool-1.example"))
	require.NoError(t, removedKeysAt(nil, "defaults", make(map[*yaml.Node]bool)))
}
