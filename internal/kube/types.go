// Package kube parses small typed views of external InfraEnv, Agent and NodePool objects.
package kube

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// LabelInfraEnv is the only supported NodePool agent selector key.
	LabelInfraEnv = "infraenvs.agent-install.openshift.io"
	// LabelAgentMachineRef marks an Agent held by an AgentMachine.
	LabelAgentMachineRef = "agentMachineRef"
	// LabelClaimed marks an Agent that was bound to a tenant cluster; the key alone retires it.
	LabelClaimed = "redbull.io/claimed"
	ClaimedValue = "true"
	// LabelBMH holds the lowercased server-scan name, the only server identity sent to remove-server-from-mce.
	LabelBMH = "agent-install.openshift.io/bmh"

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
	Labels      map[string]string
	Annotations map[string]string
	Deleting    bool
	EarlyBound  bool
}

type Requirement struct {
	Key      string
	Operator string
	Values   []string
}

type Selector struct {
	MatchLabels map[string]string
	Expressions []Requirement
}

// IsClaimedExclusion is the one expression a NodePool may add to its InfraEnv label.
func (r Requirement) IsClaimedExclusion() bool {
	return r.Key == LabelClaimed && r.Operator == string(metav1.LabelSelectorOpDoesNotExist) && len(r.Values) == 0
}

// LabelSelector evaluates the selector as CAPI does: an empty selector matches every Agent.
func (s Selector) LabelSelector() (labels.Selector, error) {
	ls := &metav1.LabelSelector{MatchLabels: s.MatchLabels}
	for _, r := range s.Expressions {
		ls.MatchExpressions = append(ls.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: r.Key, Operator: metav1.LabelSelectorOperator(r.Operator), Values: r.Values})
	}
	return metav1.LabelSelectorAsSelector(ls)
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

// WasClaimed tests the key, as a NodePool's DoesNotExist exclusion does.
func (a Agent) WasClaimed() bool { _, ok := a.Labels[LabelClaimed]; return ok }

func (a Agent) HeldByMachine() bool { _, ok := a.Labels[LabelAgentMachineRef]; return ok }

func (a Agent) ServerName() string { return a.Labels[LabelBMH] }
