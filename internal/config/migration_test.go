package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedKeyEffectiveDocument(t *testing.T) {
	const base = "hubName: hub\ninstallURL: http://api/install\n"
	for _, tc := range []struct{ yaml, path, key string }{
		{"defaults:\n  lowPct: 0", "defaults", "lowPct"},
		{"defaults: {minBand: null}", "defaults", "minBand"},
		{"defaults: &old {lowPct: null}\npools: {p: *old}", "defaults", "lowPct"},
		{"pools: {p: &old {minBand: 0}, q: *old}", "pools.p", "minBand"},
		{"defaults: {<<: {minBand: 0}}", "defaults", "minBand"},
		{"<<: {defaults: {lowPct: 0}}", "defaults", "lowPct"},
		{"pools: {<<: {p: {minBand: null}}}", "pools.p", "minBand"},
		{"<<: &root {pools: {p: {lowPct: null}}}", "pools.p", "lowPct"},
		{"<<: [{defaults: {minBand: null}}, {defaults: {highPct: 30}}]", "defaults", "minBand"},
		{"pools: {<<: [{p: {minBand: 0}}, {q: {}}]}", "pools.p", "minBand"},
		{"pools: {p: {<<: [{highPct: 30}, {minBand: 0}]}}", "pools.p", "minBand"},
		{"pools:\n  <<: &pools\n    p:\n      lowPct: 0", "pools.p", "lowPct"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			good, err := Load([]byte(base + "pools: {p: {}}"))
			require.NoError(t, err)
			store := NewStore(good)
			data := []byte(base + tc.yaml)
			want := tc.path + "." + tc.key + " was removed: the controller no longer has a ceiling; delete minBand"
			if tc.key == "lowPct" {
				want = tc.path + ".lowPct was removed: use highPct as the single target buffer percentage; delete lowPct"
			}
			_, err = Load(data)
			var migration *RemovedKeyError
			require.ErrorAs(t, err, &migration)
			require.EqualError(t, err, want)
			require.EqualError(t, store.Update(data), want)
			require.Equal(t, good, store.Snapshot())
			require.False(t, store.Valid())
		})
	}
	for _, data := range []string{
		"null", "pools: []", "defaults: null", "defaults: {<<: [{highPct: 30}]}",
		"pools: {lowPct: {}, minBand: {highPct: 30}}",
		"pools: {<<: {minBand: {highPct: 30}}}",
		"<<: {defaults: {lowPct: 0}}\ndefaults: {highPct: 30}",
		"pools: {<<: {p: {minBand: null}}, p: {highPct: 30}}",
	} {
		require.NoError(t, removedPoolKeys([]byte(data)))
	}
	for _, data := range []string{
		"pools: {p: &old {<<: *old, lowPct: 0}}", "defaults: &cycle {loop: *cycle}",
	} {
		require.Error(t, removedPoolKeys([]byte(data))) // yaml.v3 rejects cycles before inspection.
	}
	for _, data := range []string{
		"pools: {lowPct: {}, minBand: {}}",
		"pools: {<<: {minBand: {highPct: 30}}}",
		"<<: {defaults: {lowPct: 0}}\ndefaults: {highPct: 30}",
	} {
		_, err := Load([]byte(base + data))
		require.NoError(t, err)
	}
	require.Equal(t, "<invalid>", safePoolName(""))
	require.Equal(t, "<invalid>", safePoolName("bad\nname"))
	require.Equal(t, "pool-1.example", safePoolName("pool-1.example"))
	require.NoError(t, removedKeysAt(nil, "defaults"))
}
