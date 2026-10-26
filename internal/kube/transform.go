package kube

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// NewUnstructured creates a watch object carrying the external resource's GVK.
func NewUnstructured(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// PartialTransform implements Part 2, Cache: "strip managedFields and inventory
// with a DefaultTransform" while retaining the complete parsed views.
func PartialTransform() cache.TransformFunc {
	return func(obj interface{}) (interface{}, error) {
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
