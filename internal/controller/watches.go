package controller

import (
	"context"
	"reflect"

	"github.com/team-redbull/herzi/internal/kube"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// CacheOptions requires client.CacheOptions.Unstructured=true so unstructured reads use these informers.
func CacheOptions(agentNamespace string) cache.Options {
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		kube.NewUnstructured(kube.InfraEnvGVK): {Namespaces: map[string]cache.Config{agentNamespace: {}}, Transform: kube.PartialTransform()},
		kube.NewUnstructured(kube.AgentGVK):    {Namespaces: map[string]cache.Config{agentNamespace: {}}, Transform: kube.PartialTransform()},
		kube.NewUnstructured(kube.NodePoolGVK): {Namespaces: map[string]cache.Config{cache.AllNamespaces: {}}, Transform: kube.PartialTransform()},
	}}
}
func (r *Reconciler) request(pool string) []reconcile.Request {
	if pool == "" {
		return nil
	}
	return []reconcile.Request{{Namespace: r.startup.AgentNamespace, Name: pool}}
}
func (r *Reconciler) mapAgent(_ context.Context, obj client.Object) []reconcile.Request {
	if obj == nil {
		return nil
	}
	return r.request(obj.GetLabels()[kube.LabelInfraEnv])
}
func (r *Reconciler) mapNodePool(_ context.Context, obj client.Object) []reconcile.Request {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	np, _ := kube.ParseNodePool(u, r.Clock())
	return r.request(np.Selector.MatchLabels[kube.LabelInfraEnv])
}
func (r *Reconciler) allPools(ctx context.Context, _ client.Object) []reconcile.Request {
	ctx, cancel := context.WithTimeout(ctx, r.snapshot().ReconcileTimeout)
	defer cancel()
	list, err := listObjects(ctx, r.Client, kube.InfraEnvGVK, client.InNamespace(r.startup.AgentNamespace))
	if err != nil {
		log.FromContext(ctx).Error(&reconcileError{cause: err}, "Failed to list InfraEnvs for config reload")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, ie := range list.Items {
		requests = append(requests, r.request(ie.GetName())...)
	}
	return requests
}
func (r *Reconciler) parsedPredicate(agent bool) predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		old, ok1 := e.ObjectOld.(*unstructured.Unstructured)
		fresh, ok2 := e.ObjectNew.(*unstructured.Unstructured)
		if !ok1 || !ok2 {
			return true
		}
		if agent {
			a, err1 := kube.ParseAgent(old)
			b, err2 := kube.ParseAgent(fresh)
			return err1 != nil || err2 != nil || !reflect.DeepEqual(a, b)
		}
		now := r.Clock()
		a, err1 := kube.ParseNodePool(old, now)
		b, err2 := kube.ParseNodePool(fresh, now)
		return err1 != nil || err2 != nil || !reflect.DeepEqual(a, b)
	}}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	namespace := r.startup.AgentNamespace
	b := ctrl.NewControllerManagedBy(mgr).Named("herzi").
		For(kube.NewUnstructured(kube.InfraEnvGVK), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetNamespace() == namespace }))).
		Watches(kube.NewUnstructured(kube.NodePoolGVK), handler.EnqueueRequestsFromMapFunc(r.mapNodePool), builder.WithPredicates(r.parsedPredicate(false))).
		Watches(kube.NewUnstructured(kube.AgentGVK), handler.EnqueueRequestsFromMapFunc(r.mapAgent), builder.WithPredicates(r.parsedPredicate(true))).
		WithOptions(runtimecontroller.Options{MaxConcurrentReconciles: 4})
	if r.Reload != nil {
		b = b.WatchesRawSource(source.Channel(r.Reload, handler.EnqueueRequestsFromMapFunc(r.allPools)))
	}
	return b.Complete(r)
}
