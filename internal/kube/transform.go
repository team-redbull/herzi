package kube

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

func NewUnstructured(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// PartialTransform strips cache-heavy fields in place while preserving fields needed by parsers.
func PartialTransform() cache.TransformFunc {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok || u == nil {
			return obj, nil
		}
		gvk := u.GroupVersionKind()
		if gvk != AgentGVK && gvk != NodePoolGVK && gvk != InfraEnvGVK {
			return obj, nil
		}
		unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
		if gvk == AgentGVK {
			unstructured.RemoveNestedField(u.Object, "status", "inventory")
			unstructured.RemoveNestedField(u.Object, "spec", "ignitionEndpoint")
			unstructured.RemoveNestedField(u.Object, "spec", "ignitionConfigOverrides")
		}
		return obj, nil
	}
}
