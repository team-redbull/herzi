// Package kube parses small typed views of external InfraEnv, Agent and NodePool objects.
package kube

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// LabelInfraEnv is the only supported NodePool agent selector key.
	LabelInfraEnv = "infraenvs.agent-install.openshift.io"
	// LabelAgentMachineRef marks an Agent held by an AgentMachine.
	LabelAgentMachineRef = "agentMachineRef"

	StateKnownUnbound = "known-unbound"

	AnnotationState        = "herzi.redbull.io/state"
	AnnotationBlockedUntil = "herzi.redbull.io/blocked-until"
	// AnnotationPaused pauses pool ordering only when its value is "true".
	AnnotationPaused = "herzi.redbull.io/paused"
)

const apiVersion = "v1beta1"

var (
	InfraEnvGVK = schema.GroupVersionKind{Group: "agent-install.openshift.io", Version: apiVersion, Kind: "InfraEnv"}
	AgentGVK    = schema.GroupVersionKind{Group: "agent-install.openshift.io", Version: apiVersion, Kind: "Agent"}
	NodePoolGVK = schema.GroupVersionKind{Group: "hypershift.openshift.io", Version: apiVersion, Kind: "NodePool"}
)

type InfraEnv struct {
	Name        string
	Namespace   string
	Annotations map[string]string
	Deleting    bool
	EarlyBound  bool
}

type Selector struct {
	MatchLabels    map[string]string
	HasExpressions bool
}

type NodePool struct {
	Name         string
	Namespace    string
	PlatformType string
	Replicas     *int32
	AutoScaling  bool
	Selector     Selector
	Paused       bool
	Deleting     bool
}

type Agent struct {
	Name            string
	Namespace       string
	Labels          map[string]string
	Approved        bool
	Bound           bool
	State           string
	Connected       bool
	Deleting        bool
	CreatedAt       time.Time
	LastStateChange time.Time // Newest condition transition, or CreatedAt when absent.
}

func (a Agent) InfraEnvName() string { return a.Labels[LabelInfraEnv] }
