package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type failingReader struct {
	client.Reader
	kind string
	get  bool
}

func (r failingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if r.get {
		return errors.New("API unavailable")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}
func (r failingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if list.GetObjectKind().GroupVersionKind().Kind == r.kind {
		return errors.New("API unavailable")
	}
	return r.Reader.List(ctx, list, opts...)
}

type readClient struct {
	client.Client
	reader failingReader
}

func (c readClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}
func (c readClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.reader.List(ctx, list, opts...)
}
func TestAPIFailures(t *testing.T) {
	for _, stage := range []string{"cached get", "cached InfraEnvList", "cached NodePoolList", "cached AgentList", "fresh get", "fresh NodePoolList", "fresh AgentList"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t, testConfig(), infra("p"))
			reader := failingReader{Reader: f.c}
			switch stage {
			case "cached get", "fresh get":
				reader.get = true
			case "cached InfraEnvList":
				reader.kind = "InfraEnvList"
			case "cached NodePoolList", "fresh NodePoolList":
				reader.kind = "NodePoolList"
			case "cached AgentList", "fresh AgentList":
				reader.kind = "AgentList"
			}
			if stage[:5] == "fresh" {
				f.r.Reader = reader
			} else {
				f.r.Client = readClient{Client: f.c, reader: reader}
			}
			_, err := f.r.Reconcile(context.Background(), ctrl.Request{Namespace: "inventory", Name: "p"})
			require.ErrorContains(t, errors.Unwrap(err), "API unavailable")
			require.Empty(t, f.install.requests)
		})
	}
	f := setup(t, testConfig())
	f.r.Client = readClient{Client: f.c, reader: failingReader{Reader: f.c, kind: "InfraEnvList"}}
	require.Empty(t, f.r.allPools(context.Background(), nil))
}
func TestMalformedFreshReadAndDedup(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	fresh := infra("p")
	fresh.Object["spec"] = map[string]any{"clusterRef": "bad"}
	c := setup(t, testConfig(), fresh)
	f.r.Reader = c.c
	f.reconcile(t, "p")
	require.Empty(t, f.install.requests)
	require.NotEmpty(t, f.r.reasons["p"])
	f.r.warnings(context.Background(), []pipeline.Warning{{Object: "a", Msg: "same"}, {Object: "a", Msg: "same"}})
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_warnings_total", map[string]string{"kind": "same"}))
	f.r.blocked(context.Background(), "p", nil)
	require.Empty(t, f.r.reasons)
}
func TestUnmanagedMetricsDisappear(t *testing.T) {
	cfg := testConfig()
	delete(cfg.Pools, "p")
	f := setup(t, cfg, infra("p"))
	f.reconcile(t, "p")
	families, err := f.reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				require.False(t, label.GetName() == "pool" && label.GetValue() == "p")
			}
		}
	}
}
func TestAgentBuckets(t *testing.T) {
	f := setup(t, testConfig(), infra("p"))
	a := agent("assigned", "p")
	require.NoError(t, unstructured.SetNestedMap(a.Object, map[string]any{"name": "cluster"}, "spec", "clusterDeploymentName"))
	require.NoError(t, f.c.Create(context.Background(), a))
	s := agent("settling", "p")
	require.NoError(t, unstructured.SetNestedField(s.Object, "discovering", "status", "debugInfo", "state"))
	s.SetCreationTimestamp(metav1.NewTime(f.now))
	require.NoError(t, f.c.Create(context.Background(), s))
	stuck := agent("stuck", "p")
	require.NoError(t, unstructured.SetNestedField(stuck.Object, "discovering", "status", "debugInfo", "state"))
	require.NoError(t, f.c.Create(context.Background(), stuck))
	f.reconcile(t, "p")
	require.Empty(t, f.install.requests)
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_assigned", nil))
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_settling", nil))
	require.Equal(t, 1.0, metric(t, f.reg, "herzi_pool_stuck_agents", nil))
}
func TestUnlabeledMalformedMap(t *testing.T) {
	f := setup(t, testConfig())
	u := node("bad", "p", 2)
	require.NoError(t, unstructured.SetNestedField(u.Object, "wrong", "spec", "replicas"))
	require.Equal(t, "p", f.r.mapNodePool(context.Background(), u)[0].Name)
	u = object(kube.NodePoolGVK, "tenant", "none")
	require.Empty(t, f.r.mapNodePool(context.Background(), u))
}

func TestNegativeReplicasBlockOnlyTheirPool(t *testing.T) {
	cfg := testConfig()
	f := setup(t, cfg, infra("p"), infra("q"), node("positive", "p", 20),
		node("negative", "p", -10), node("healthy", "q", 6))
	raw, err := f.r.gather(t.Context(), f.c, cfg, "p", f.now, false)
	require.NoError(t, err)
	require.Len(t, raw.ReadErrs, 1)
	require.Equal(t, pipeline.PoolKey("p"), raw.ReadErrs[0].Pool)
	require.ErrorContains(t, raw.ReadErrs[0].Err, "spec.replicas: must be >= 0")
	raw.Pollers = f.queues.result
	facts := pipeline.Attribute(raw, cfg)
	require.Equal(t, 20, facts["p"].Demand)
	require.True(t, pipeline.Validate(facts["p"], raw, cfg.ForPool("p")).Blocked)
	require.False(t, pipeline.Validate(facts["q"], raw, cfg.ForPool("q")).Blocked)
	f.reconcile(t, "p")
	require.Empty(t, f.install.requests)
	require.Equal(t, "read_error", f.r.reasons["p"])
	f.reconcile(t, "q")
	require.Equal(t, []pipeline.InstallRequest{{Key: "q", Count: 8}}, f.install.requests)
}
