// Package kube holds small, typed views over the three objects herzi reads but does not own:
// assisted-service InfraEnv and Agent, and HyperShift NodePool.
//
// They are read as unstructured and parsed into these views on purpose: importing the typed
// API modules pulls in well over a hundred modules (including an ORM) and a ~10 MB larger binary
// for about a dozen fields. The views are the ONLY place that knows the raw field paths.
package kube

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// LabelInfraEnv is stamped on every Agent by assisted-service and is the ONLY key a NodePool
	// agentLabelSelector may use (see the design: the selector contract).
	LabelInfraEnv = "infraenvs.agent-install.openshift.io"
	// LabelAgentMachineRef is set by cluster-api-provider-agent while an AgentMachine holds an Agent.
	LabelAgentMachineRef = "agentMachineRef"
	// StateKnownUnbound is the Agent status.debugInfo.state of a free, usable Agent.
	StateKnownUnbound = "known-unbound"
	// AnnotationBlockedUntil is written on an InfraEnv after an unclear POST (RFC 3339, UTC).
	AnnotationBlockedUntil = "herzi.redbull.io/blocked-until"
	// AnnotationPaused pauses ordering for one pool when its value is "true".
	AnnotationPaused = "herzi.redbull.io/paused"
)

// apiVersion is the served version of all three kinds.
const apiVersion = "v1beta1"

var (
	InfraEnvGVK = schema.GroupVersionKind{Group: "agent-install.openshift.io", Version: apiVersion, Kind: "InfraEnv"}
	AgentGVK    = schema.GroupVersionKind{Group: "agent-install.openshift.io", Version: apiVersion, Kind: "Agent"}
	NodePoolGVK = schema.GroupVersionKind{Group: "hypershift.openshift.io", Version: apiVersion, Kind: "NodePool"}
)

// InfraEnv is the pool: one node type in one zone.
type InfraEnv struct {
	Name        string
	Namespace   string
	Annotations map[string]string
	Deleting    bool // metadata.deletionTimestamp is set
	EarlyBound  bool // spec.clusterRef is set: hosts are bound at once, never held as spares
}

// Selector is the part of a NodePool agentLabelSelector herzi cares about.
type Selector struct {
	MatchLabels    map[string]string
	HasExpressions bool // matchExpressions is non-empty (unsupported)
}

// NodePool is the demand side.
type NodePool struct {
	Name         string
	Namespace    string
	PlatformType string // spec.platform.type; only "Agent" is ours
	Replicas     *int32 // spec.replicas; nil when unset (e.g. autoscaling)
	AutoScaling  bool   // spec.autoScaling is set
	Selector     Selector
	Paused       bool // spec.pausedUntil is "true" or an RFC 3339 time in the future (evaluated at parse time "now")
	Deleting     bool
}

// Agent is one machine that registered with assisted-service.
type Agent struct {
	Name            string
	Namespace       string
	Labels          map[string]string
	Approved        bool // spec.approved
	Bound           bool // spec.clusterDeploymentName is set
	State           string
	Connected       bool // condition Connected == True
	Deleting        bool
	CreatedAt       time.Time
	LastStateChange time.Time // newest status.conditions[].lastTransitionTime, else CreatedAt
}

// InfraEnvName is the pool an Agent claims to belong to ("" when unlabeled).
func (a Agent) InfraEnvName() string { return a.Labels[LabelInfraEnv] }
