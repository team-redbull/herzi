package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/pipeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *Reconciler) gather(ctx context.Context,
	reader client.Reader,
	cfg config.Config,
	pool string,
	now time.Time,
	recheck bool) (pipeline.RawSnapshot,
	error) {
	raw := pipeline.RawSnapshot{At: now}
	if recheck {
		ie := kube.NewUnstructured(kube.InfraEnvGVK)
		if err := reader.Get(ctx, types.NamespacedName{Namespace: cfg.AgentNamespace, Name: pool}, ie); err != nil {
			return raw, fmt.Errorf("recheck InfraEnv: %w", err)
		}
		replaceInfraEnv(&raw, ie)
	} else {
		list, err := listObjects(ctx, reader, kube.InfraEnvGVK, client.InNamespace(cfg.AgentNamespace))
		if err != nil {
			return raw, err
		}
		for i := range list.Items {
			replaceInfraEnv(&raw, &list.Items[i])
		}
	}
	list, err := listObjects(ctx, reader, kube.NodePoolGVK)
	if err != nil {
		return raw, err
	}
	for i := range list.Items {
		u := &list.Items[i]
		identity := parseError(u, nil)
		v, err := kube.ParseNodePool(u, now)
		if err != nil {
			appendParseError(&raw, identity, err)
			continue
		}
		raw.NodePools = append(raw.NodePools, v)
	}
	opts := []client.ListOption{client.InNamespace(cfg.AgentNamespace)}
	if recheck {
		opts = append(opts, client.MatchingLabels{kube.LabelInfraEnv: pool})
	}
	list, err = listObjects(ctx, reader, kube.AgentGVK, opts...)
	if err != nil {
		return raw, err
	}
	for i := range list.Items {
		u := &list.Items[i]
		identity := parseError(u, nil)
		v, err := kube.ParseAgent(u)
		if err != nil {
			appendParseError(&raw, identity, err)
			continue
		}
		raw.Agents = append(raw.Agents, v)
	}
	return raw, nil
}
func listObjects(ctx context.Context,
	reader client.Reader,
	gvk schema.GroupVersionKind,
	opts ...client.ListOption) (*unstructured.UnstructuredList,
	error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := reader.List(ctx, list, opts...); err != nil {
		return nil, fmt.Errorf("list %s: %w", gvk.Kind, err)
	}
	return list, nil
}
func parseError(u *unstructured.Unstructured, err error) pipeline.ReadError {
	pool := ""
	switch u.GetKind() {
	case "Agent":
		pool = u.GetLabels()[kube.LabelInfraEnv]
	case "InfraEnv":
		pool = u.GetName()
	case "NodePool":
		pool, _, _ = unstructured.NestedString(u.Object, "spec", "platform", "agent",
			"agentLabelSelector", "matchLabels", kube.LabelInfraEnv)
	}
	return pipeline.ReadError{Pool: pipeline.PoolKey(pool),
		What: u.GetKind() + " " + u.GetNamespace() + "/" + u.GetName(), Err: err}
}
func appendParseError(raw *pipeline.RawSnapshot, e pipeline.ReadError, err error) {
	e.Err = err
	if e.Pool == "" {
		raw.Warnings = append(raw.Warnings, pipeline.Warning{Object: e.What, Msg: "malformed unattributed object"})
	} else {
		raw.ReadErrs = append(raw.ReadErrs, e)
	}
}
func replaceInfraEnv(raw *pipeline.RawSnapshot, u *unstructured.Unstructured) {
	identity := parseError(u, nil)
	v, err := kube.ParseInfraEnv(u)
	if err != nil {
		appendParseError(raw, identity, err)
		// Keep the identity so this pool still reports metrics while blocked.
		v = kube.InfraEnv{Name: u.GetName(), Namespace: u.GetNamespace()}
	}
	for i := range raw.InfraEnvs {
		if raw.InfraEnvs[i].Name == v.Name {
			raw.InfraEnvs[i] = v
			return
		}
	}
	raw.InfraEnvs = append(raw.InfraEnvs, v)
}
