package kube

import (
	"fmt"
	"math"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// reader keeps the first malformed field error, including its full path. Missing
// fields have zero values; Kubernetes admission validates required field presence.
type reader struct {
	object map[string]interface{}
	err    error
}

func newReader(u *unstructured.Unstructured) *reader {
	r := &reader{}
	if u == nil {
		r.err = fmt.Errorf("object: must not be nil")
	} else {
		r.object = u.Object
	}
	return r
}
func (r *reader) fail(path []string, err error) {
	if r.err == nil && err != nil {
		r.err = fmt.Errorf("%s: %w", strings.Join(path, "."), err)
	}
}
func (r *reader) exists(path ...string) bool {
	v, found, err := unstructured.NestedFieldNoCopy(r.object, path...)
	r.fail(path, err)
	return found && v != nil
}
func (r *reader) str(path ...string) string {
	if !r.exists(path...) {
		return ""
	}
	v, _, err := unstructured.NestedString(r.object, path...)
	r.fail(path, err)
	return v
}
func (r *reader) boolean(path ...string) bool {
	if !r.exists(path...) {
		return false
	}
	v, _, err := unstructured.NestedBool(r.object, path...)
	r.fail(path, err)
	return v
}
func (r *reader) stringMap(path ...string) map[string]string {
	if !r.exists(path...) {
		return nil
	}
	v, _, err := unstructured.NestedStringMap(r.object, path...)
	r.fail(path, err)
	return v
}
func (r *reader) objectAt(path ...string) map[string]interface{} {
	if !r.exists(path...) {
		return nil
	}
	raw, _, err := unstructured.NestedFieldNoCopy(r.object, path...)
	r.fail(path, err)
	value, ok := raw.(map[string]interface{})
	if !ok {
		r.fail(path, fmt.Errorf("must be an object, got %T", raw))
	}
	return value
}
func (r *reader) slice(path ...string) []interface{} {
	if !r.exists(path...) {
		return nil
	}
	raw, _, err := unstructured.NestedFieldNoCopy(r.object, path...)
	r.fail(path, err)
	value, ok := raw.([]interface{})
	if !ok {
		r.fail(path, fmt.Errorf("must be an array, got %T", raw))
	}
	return value
}
func (r *reader) timestamp(path ...string) time.Time {
	s := r.str(path...)
	if s == "" {
		return time.Time{}
	}
	v, err := time.Parse(time.RFC3339, s)
	r.fail(path, err)
	return v
}
func (r *reader) reference(path ...string) bool {
	if r.objectAt(path...) == nil {
		return false
	}
	name := r.str(append(append([]string{}, path...), "name")...)
	r.str(append(append([]string{}, path...), "namespace")...)
	return name != ""
}
func (r *reader) deleting() bool { return !r.timestamp("metadata", "deletionTimestamp").IsZero() }

// ParseInfraEnv implements Part 2, Validate: "InfraEnv deleting, or spec.clusterRef set (early-bound)".
func ParseInfraEnv(u *unstructured.Unstructured) (InfraEnv, error) {
	r := newReader(u)
	v := InfraEnv{Name: r.str("metadata", "name"), Namespace: r.str("metadata", "namespace"), Annotations: r.stringMap("metadata", "annotations"), Deleting: r.deleting(), EarlyBound: r.reference("spec", "clusterRef")}
	return v, r.err
}

// ParseNodePool implements Part 2, Attribute: "pausedUntil: \"true\" or RFC3339"
// and "no replicas (autoscaling?)". Absent replicas stay nil.
func ParseNodePool(u *unstructured.Unstructured, now time.Time) (NodePool, error) {
	r := newReader(u)
	v := NodePool{Name: r.str("metadata", "name"), Namespace: r.str("metadata", "namespace"), PlatformType: r.str("spec", "platform", "type"), Deleting: r.deleting()}
	if r.exists("spec", "replicas") {
		raw, _, err := unstructured.NestedFieldNoCopy(r.object, "spec", "replicas")
		r.fail([]string{"spec", "replicas"}, err)
		var n int64
		switch x := raw.(type) {
		case int32:
			n = int64(x)
		case int64:
			n = x
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x < math.MinInt32 || x > math.MaxInt32 {
				r.fail([]string{"spec", "replicas"}, fmt.Errorf("must be an int32 integer"))
			} else {
				n = int64(x)
			}
		default:
			r.fail([]string{"spec", "replicas"}, fmt.Errorf("must be an integer, got %T", raw))
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			r.fail([]string{"spec", "replicas"}, fmt.Errorf("outside int32 range"))
		}
		if r.err == nil {
			replicas := int32(n)
			v.Replicas = &replicas
		}
	}
	v.AutoScaling = r.objectAt("spec", "autoScaling") != nil
	v.Selector.MatchLabels = r.stringMap("spec", "platform", "agent", "agentLabelSelector", "matchLabels")
	v.Selector.HasExpressions = len(r.slice("spec", "platform", "agent", "agentLabelSelector", "matchExpressions")) > 0
	paused := r.str("spec", "pausedUntil")
	until, err := time.Parse(time.RFC3339, paused)
	v.Paused = paused == "true" || (err == nil && until.After(now))
	return v, r.err
}

// ParseAgent implements Part 1's supply terms and Part 2, Attribute:
// "settling ... measured from its last state change" (including stale labels).
func ParseAgent(u *unstructured.Unstructured) (Agent, error) {
	r := newReader(u)
	v := Agent{Name: r.str("metadata", "name"), Namespace: r.str("metadata", "namespace"), Labels: r.stringMap("metadata", "labels"), Approved: r.boolean("spec", "approved"), Bound: r.reference("spec", "clusterDeploymentName"), State: r.str("status", "debugInfo", "state"), Deleting: r.deleting(), CreatedAt: r.timestamp("metadata", "creationTimestamp")}
	for i, raw := range r.slice("status", "conditions") {
		path := []string{fmt.Sprintf("status.conditions[%d]", i)}
		m, ok := raw.(map[string]interface{})
		if !ok {
			r.fail(path, fmt.Errorf("must be an object"))
			continue
		}
		c := &reader{object: m}
		kind, status := c.str("type"), c.str("status")
		if kind == "Connected" && status == "True" {
			v.Connected = true
		}
		at := c.timestamp("lastTransitionTime")
		if at.After(v.LastStateChange) {
			v.LastStateChange = at
		}
		r.fail(path, c.err)
	}
	if v.LastStateChange.IsZero() {
		v.LastStateChange = v.CreatedAt
	}
	return v, r.err
}
