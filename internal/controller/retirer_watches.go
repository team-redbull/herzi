package controller

import (
	"context"

	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SetupWithManager keys the retirer by Agent; NodePool and InfraEnv changes can unblock any retiring Agent.
func (t *Retirer) SetupWithManager(mgr ctrl.Manager) error {
	namespace := t.pool.startup.AgentNamespace
	inNamespace := predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetNamespace() == namespace })
	return ctrl.NewControllerManagedBy(mgr).Named("herzi-retirer").
		For(kube.NewUnstructured(kube.AgentGVK), builder.WithPredicates(inNamespace, t.pool.parsedPredicate(true))).
		Watches(kube.NewUnstructured(kube.NodePoolGVK), handler.EnqueueRequestsFromMapFunc(t.retiringAgents),
			builder.WithPredicates(t.pool.parsedPredicate(false))).
		Watches(kube.NewUnstructured(kube.InfraEnvGVK), handler.EnqueueRequestsFromMapFunc(t.retiringAgents),
			builder.WithPredicates(inNamespace)).
		WithOptions(runtimecontroller.Options{MaxConcurrentReconciles: 4}).
		Complete(t)
}

func (t *Retirer) retiringAgents(ctx context.Context, _ client.Object) []reconcile.Request {
	ctx, cancel := context.WithTimeout(ctx, t.pool.snapshot().ReconcileTimeout)
	defer cancel()
	list, err := listObjects(ctx, t.pool.Client, kube.AgentGVK, client.InNamespace(t.pool.startup.AgentNamespace))
	if err != nil {
		log.FromContext(ctx).Error(&reconcileError{cause: err}, "Failed to list Agents for retirement")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		a, err := kube.ParseAgent(&list.Items[i])
		if err == nil && pipeline.Retirement(a) == pipeline.RetireCandidate {
			requests = append(requests, reconcile.Request{Namespace: a.Namespace, Name: a.Name})
		}
	}
	return requests
}
