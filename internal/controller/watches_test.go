package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/kube"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestMappings(t *testing.T) {
	f := setup(t, testConfig(), infra("p"), infra("q"), object(kube.InfraEnvGVK, "other", "hidden"))
	ctx := context.Background()
	key := client.ObjectKeyFromObject(infra("p"))
	require.Equal(t, key, f.r.mapAgent(ctx, agent("a", "p"))[0].NamespacedName)
	require.Equal(t, key, f.r.mapNodePool(ctx, node("n", "p", 2))[0].NamespacedName)
	requests := f.r.allPools(ctx, nil)
	require.Len(t, requests, 2)
	found := false
	for _, r := range requests {
		if r.NamespacedName == key {
			found = true
		}
	}
	require.True(t, found)
	require.Empty(t, f.r.mapAgent(ctx, agent("a", "")))
	require.Empty(t, f.r.mapNodePool(ctx, node("n", "", 2)))
	require.Empty(t, f.r.mapNodePool(ctx, nil))
}
func TestPredicates(t *testing.T) {
	f := setup(t, testConfig())
	for _, isAgent := range []bool{true, false} {
		p := f.r.parsedPredicate(isAgent)
		old := node("n", "p", 2)
		if isAgent {
			old = agent("a", "p")
		}
		fresh := old.DeepCopy()
		fresh.SetResourceVersion("123")
		require.False(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: fresh}))
		require.True(t, p.Create(event.CreateEvent{Object: old}))
		require.True(t, p.Delete(event.DeleteEvent{Object: old}))
		require.True(t, p.Generic(event.GenericEvent{Object: old}))
		require.True(t, p.Update(event.UpdateEvent{}))
		if isAgent {
			fresh.SetLabels(map[string]string{kube.LabelInfraEnv: "q"})
		} else {
			require.NoError(t, unstructured.SetNestedField(fresh.Object, int64(4), "spec", "replicas"))
		}
		require.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: fresh}))
		fresh.Object["spec"] = "bad"
		require.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: fresh}))
	}
}
func TestCacheOptions(t *testing.T) {
	opts := CacheOptions("inventory")
	require.Len(t, opts.ByObject, 3)
	for obj, by := range opts.ByObject {
		require.Nil(t, by.Label)
		require.NotNil(t, by.Transform)
		ns := "inventory"
		if obj.GetObjectKind().GroupVersionKind() == kube.NodePoolGVK {
			ns = cache.AllNamespaces
		}
		require.Equal(t, map[string]cache.Config{ns: {}}, by.Namespaces)
		u := obj.(*unstructured.Unstructured).DeepCopy()
		require.NoError(t, unstructured.SetNestedSlice(u.Object, []any{map[string]any{"manager": "noise"}}, "metadata", "managedFields"))
		out, err := by.Transform(u)
		require.NoError(t, err)
		_, found, _ := unstructured.NestedFieldNoCopy(out.(*unstructured.Unstructured).Object, "metadata", "managedFields")
		require.False(t, found)
	}
}
