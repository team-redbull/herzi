package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/pipeline"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func reconcileOnce(t *testing.T, f *fixture, pool string) ctrl.Result {
	t.Helper()
	result, err := f.r.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: pool})
	require.NoError(t, err)
	return result
}

func TestEveryInfraEnvIsManagedWithoutBeingListed(t *testing.T) {
	cfg := testConfig()
	cfg.Pools = map[string]config.PoolOverride{}
	f := setup(t, cfg, infra("p"), node("workers", "p", 4))
	reconcileOnce(t, f, "p")
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 6}}, f.install.requests)
}

func TestIgnoredInfraEnvIsNotTouched(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *fixture, *config.Config){
		"label": func(*testing.T, *fixture, *config.Config) {},
		"exclude": func(_ *testing.T, _ *fixture, cfg *config.Config) {
			cfg.Pools["p"] = config.PoolOverride{Exclude: new(true)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			ie := infra("p")
			if name == "label" {
				ie.SetLabels(map[string]string{config.DefaultIgnoreLabel: ""})
			}
			mutate(t, nil, &cfg)
			f := setup(t, cfg, ie, node("workers", "p", 4), agent("a", "p"))
			result := reconcileOnce(t, f, "p")
			require.Zero(t, result.RequeueAfter, "an ignored InfraEnv is not requeued; events or a reload wake it")
			require.Zero(t, f.sweep.calls, "no Temporal reads for an ignored InfraEnv")
			require.Empty(t, f.install.requests)
			requireNoPoolMetrics(t, f, "p")
		})
	}
}

func TestIgnoreLabelNameIsConfigurable(t *testing.T) {
	cfg := testConfig()
	cfg.IgnoreLabel = "example.com/skip"
	ie := infra("p")
	ie.SetLabels(map[string]string{config.DefaultIgnoreLabel: ""})
	f := setup(t, cfg, ie, node("workers", "p", 4))
	reconcileOnce(t, f, "p")
	require.NotEmpty(t, f.install.requests, "the default label no longer ignores when another name is configured")

	skipped := infra("q")
	skipped.SetLabels(map[string]string{"example.com/skip": "anything"})
	g := setup(t, cfg, skipped, node("workers", "q", 4))
	reconcileOnce(t, g, "q")
	require.Empty(t, g.install.requests)
}

func TestRemovingTheIgnoreLabelStartsManaging(t *testing.T) {
	cfg := testConfig()
	ie := infra("p")
	ie.SetLabels(map[string]string{config.DefaultIgnoreLabel: "true"})
	f := setup(t, cfg, ie, node("workers", "p", 4))
	reconcileOnce(t, f, "p")
	require.Empty(t, f.install.requests)

	current := infra("p")
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKeyFromObject(current), current))
	current.SetLabels(nil)
	require.NoError(t, f.c.Update(context.Background(), current))
	reconcileOnce(t, f, "p")
	require.Equal(t, []pipeline.InstallRequest{{Key: "p", Count: 6}}, f.install.requests)
}

func requireNoPoolMetrics(t *testing.T, f *fixture, pool string) {
	t.Helper()
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				require.False(t, label.GetName() == "pool" && label.GetValue() == pool, "%s still has a %s series", pool, family.GetName())
			}
		}
	}
}

func TestReloadWarnsAboutAnOverrideThatMatchesNoInfraEnv(t *testing.T) {
	cfg := testConfig()
	cfg.Pools = map[string]config.PoolOverride{"p": {}, "typo-name": {}}
	f := setup(t, cfg, infra("p"))
	require.Len(t, f.r.allPools(context.Background(), nil), 1)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_warnings_total", map[string]string{"kind": "pool override matches no InfraEnv"}))
}
