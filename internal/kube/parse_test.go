package kube

import (
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var testNow = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func fixtures(t *testing.T, name string) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var list unstructured.Unstructured
	data, err = yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = list.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	items, err := list.ToList()
	if err != nil {
		t.Fatal(err)
	}
	result := make([]*unstructured.Unstructured, len(items.Items))
	for i := range items.Items {
		result[i] = &items.Items[i]
	}
	return result
}
func TestFixtures(t *testing.T) {
	for _, u := range fixtures(t, "agents") {
		t.Run("Agent/"+u.GetName(), func(t *testing.T) {
			a, err := ParseAgent(u)
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != u.GetName() || a.Namespace != "inventory" {
				t.Fatal(a)
			}
			switch a.Name {
			case "ready":
				if !a.Approved || a.Bound || !a.Connected || a.State != StateKnownUnbound || a.InfraEnvName() != "pool-a" || !a.LastStateChange.Equal(testNow.Add(-40*time.Minute)) || !a.CreatedAt.Equal(testNow.Add(-time.Hour)) {
					t.Fatal(a)
				}
			case "bound":
				if !a.Bound || a.State != "installed" {
					t.Fatal(a)
				}
			case "stale":
				if a.Labels[LabelAgentMachineRef] != "old-machine" || a.Bound || a.Connected || !a.LastStateChange.Equal(a.CreatedAt) {
					t.Fatal(a)
				}
			case "deleting":
				if !a.Deleting {
					t.Fatal(a)
				}
			}
		})
	}
	for _, u := range fixtures(t, "nodepools") {
		t.Run("NodePool/"+u.GetName(), func(t *testing.T) {
			n, err := ParseNodePool(u, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if n.Name != u.GetName() || n.Namespace != "clusters" {
				t.Fatal(n)
			}
			switch n.Name {
			case "replicas":
				if n.Replicas == nil || *n.Replicas != 4 || n.AutoScaling || n.PlatformType != "Agent" || !n.Selector.HasExpressions || n.Selector.MatchLabels[LabelInfraEnv] != "pool-a" {
					t.Fatal(n)
				}
			case "deleting":
				if !n.Deleting || n.Replicas == nil || *n.Replicas != 0 {
					t.Fatal(n)
				}
			case "autoscaling":
				if n.Replicas != nil || !n.AutoScaling {
					t.Fatal(n)
				}
			default:
				if n.Paused != (n.Name == "paused" || n.Name == "future") {
					t.Fatal(n)
				}
			}
		})
	}
	for _, u := range fixtures(t, "infraenvs") {
		t.Run("InfraEnv/"+u.GetName(), func(t *testing.T) {
			v, err := ParseInfraEnv(u)
			if err != nil {
				t.Fatal(err)
			}
			if v.Name != u.GetName() || v.Namespace != "inventory" || v.EarlyBound != (v.Name == "early") || v.Deleting != (v.Name == "deleting") {
				t.Fatal(v)
			}
			if v.EarlyBound && (v.Annotations[AnnotationPaused] != "true" || v.Annotations[AnnotationBlockedUntil] == "") {
				t.Fatal(v)
			}
		})
	}
}
func parseAll(u *unstructured.Unstructured) []error {
	_, a := ParseAgent(u)
	_, n := ParseNodePool(u, testNow)
	_, i := ParseInfraEnv(u)
	return []error{a, n, i}
}
func TestMissingAndNil(t *testing.T) {
	for _, err := range parseAll(nil) {
		if err == nil {
			t.Fatal("nil must fail")
		}
	}
	for _, u := range []*unstructured.Unstructured{{}, {Object: map[string]any{"metadata": map[string]any{"deletionTimestamp": nil}, "spec": map[string]any{"approved": nil}}}} {
		for _, err := range parseAll(u) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestMalformedFields(t *testing.T) {
	cases := []struct {
		path   string
		value  any
		parser int
	}{
		{"metadata.name", true, 0}, {"metadata.namespace", true, 1}, {"metadata.labels", []any{}, 0}, {"metadata.labels", map[string]any{"x": true}, 0}, {"metadata.annotations", false, 2},
		{"metadata.creationTimestamp", 42.0, 0}, {"metadata.creationTimestamp", "invalid", 0}, {"metadata.deletionTimestamp", false, 2}, {"metadata.deletionTimestamp", "invalid", 1},
		{"spec.approved", "true", 0}, {"spec.clusterDeploymentName", "tenant", 0}, {"spec.clusterDeploymentName.name", true, 0}, {"spec.clusterDeploymentName.namespace", true, 0},
		{"spec.clusterRef", false, 2}, {"spec.clusterRef.name", 42.0, 2}, {"status.debugInfo.state", true, 0}, {"status.conditions", false, 0},
		{"status.conditions", []any{false}, 0}, {"status.conditions", []any{map[string]any{"type": true}}, 0},
		{"status.conditions", []any{map[string]any{"status": false}}, 0}, {"status.conditions", []any{map[string]any{"lastTransitionTime": "invalid"}}, 0},
		{"spec.replicas", int64(-10), 1}, {"spec.replicas", int64(math.MinInt32), 1}, {"spec.replicas", "4", 1}, {"spec.replicas", 1.5, 1}, {"spec.replicas", int64(math.MaxInt32) + 1, 1}, {"spec.replicas", float64(math.MaxInt32) + 1, 1}, {"spec.replicas", math.NaN(), 1},
		{"spec.autoScaling", true, 1}, {"spec.platform.type", true, 1}, {"spec.platform.agent.agentLabelSelector.matchLabels", false, 1}, {"spec.platform.agent.agentLabelSelector.matchExpressions", false, 1}, {"spec.pausedUntil", true, 1},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d/%s", i, tc.path), func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{}}
			if err := unstructured.SetNestedField(u.Object, tc.value, strings.Split(tc.path, ".")...); err != nil {
				t.Fatal(err)
			}
			err := parseAll(u)[tc.parser]
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("expected path %s: %v", tc.path, err)
			}
		})
	}

	u := &unstructured.Unstructured{Object: map[string]any{"metadata": false, "spec": false, "status": false}}
	for _, err := range parseAll(u) {
		if err == nil || !strings.Contains(err.Error(), "metadata.name") {
			t.Fatal(err)
		}
	}
}
func TestParserBoundaries(t *testing.T) {
	for _, value := range []any{int32(4), int64(0), int64(math.MaxInt32), float64(4)} {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicas": value}}}
		n, err := ParseNodePool(u, testNow)
		if err != nil || n.Replicas == nil {
			t.Fatalf("%v %v", n, err)
		}
	}
	for _, paused := range []string{"", "false", "invalid", testNow.Format(time.RFC3339), testNow.Add(time.Nanosecond).Format(time.RFC3339Nano), "true"} {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"pausedUntil": paused}}}
		n, err := ParseNodePool(u, testNow)
		if err != nil || n.Paused != (paused == "true" || strings.Contains(paused, ".000000001")) {
			t.Fatalf("%s: %v %v", paused, n, err)
		}
	}
	for _, name := range []string{"", "tenant"} {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"clusterRef": map[string]any{"name": name}, "clusterDeploymentName": map[string]any{"name": name}, "autoScaling": map[string]any{}}}}
		a, err := ParseAgent(u)
		if err != nil || a.Bound != (name != "") {
			t.Fatal(a, err)
		}
		ie, err := ParseInfraEnv(u)
		if err != nil || ie.EarlyBound != (name != "") {
			t.Fatal(ie, err)
		}
		n, err := ParseNodePool(u, testNow)
		if err != nil || !n.AutoScaling {
			t.Fatal(n, err)
		}
	}

	for _, conditions := range [][]any{
		{map[string]any{"type": "Connected", "status": "False", "lastTransitionTime": "2026-10-25T08:00:00Z"}, map[string]any{"type": "Other", "status": "True", "lastTransitionTime": "2026-10-25T09:00:00Z"}},
		{map[string]any{"lastTransitionTime": "2026-10-25T09:00:00Z"}, map[string]any{"lastTransitionTime": "2026-10-25T08:00:00Z"}},
	} {
		u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"creationTimestamp": "2026-10-26T08:00:00Z"}, "status": map[string]any{"conditions": conditions}}}
		a, err := ParseAgent(u)
		if err != nil || a.Connected || a.LastStateChange.Format(time.RFC3339) != "2026-10-25T09:00:00Z" {
			t.Fatal(a, err)
		}
	}
}
func TestTransform(t *testing.T) {
	transform := PartialTransform()
	for _, file := range []string{"agents", "nodepools", "infraenvs"} {
		for _, u := range fixtures(t, file) {
			before := u.DeepCopy()
			a, _ := ParseAgent(before)
			n, _ := ParseNodePool(before, testNow)
			ie, _ := ParseInfraEnv(before)
			got, err := transform(u)
			if err != nil || got != u {
				t.Fatal(err)
			}
			aa, _ := ParseAgent(u)
			nn, _ := ParseNodePool(u, testNow)
			ii, _ := ParseInfraEnv(u)
			if !reflect.DeepEqual(a, aa) || !reflect.DeepEqual(n, nn) || !reflect.DeepEqual(ie, ii) {
				t.Fatal("parsed fields changed")
			}
			for _, path := range [][]string{{"metadata", "managedFields"}, {"status", "inventory"}, {"spec", "ignitionEndpoint"}, {"spec", "ignitionConfigOverrides"}} {
				if _, found, _ := unstructured.NestedFieldNoCopy(u.Object, path...); found {
					t.Fatal("retained", path)
				}
			}
		}
	}
	for _, obj := range []any{nil, (*unstructured.Unstructured)(nil), "other", NewUnstructured(NodePoolGVK), NewUnstructured(InfraEnvGVK)} {
		got, err := transform(obj)
		if err != nil || got != obj {
			t.Fatal(got, err)
		}
	}
	other := NewUnstructured(AgentGVK)
	other.SetKind("Other")
	other.Object["metadata"] = map[string]any{"managedFields": []any{"keep"}}
	before := other.DeepCopy()
	_, _ = transform(other)
	if !reflect.DeepEqual(before, other) {
		t.Fatal("changed other type")
	}
	if NewUnstructured(AgentGVK).GroupVersionKind() != AgentGVK {
		t.Fatal("wrong GVK")
	}
}

func TestNonJSONMistypedFieldsDoNotPanic(t *testing.T) {
	cases := []struct {
		object map[string]any
		parser int
		path   string
	}{
		{map[string]any{"spec": map[string]any{"clusterDeploymentName": map[string]any{"name": int32(1)}}}, 0, "spec.clusterDeploymentName.name"},
		{map[string]any{"spec": map[string]any{"clusterRef": map[string]any{"name": int32(1)}}}, 2, "spec.clusterRef.name"},
		{map[string]any{"status": map[string]any{"conditions": []any{map[string]any{"type": int32(1)}}}}, 0, "status.conditions[0]"},
	}
	for _, tc := range cases {
		err := parseAll(&unstructured.Unstructured{Object: tc.object})[tc.parser]
		if err == nil || !strings.Contains(err.Error(), tc.path) {
			t.Fatal(err)
		}
	}
}
