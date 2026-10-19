// Package tools splits cluster capabilities into two ADK tool lists.
// ReadOnly is what Triage and Diagnostics get. Mutating execute_remediation
// is attached only to the Reviewer and requires human confirmation.
package tools

import (
	"sync"
	"time"

	"incident-response-swarm/cluster"
	"incident-response-swarm/policy"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type Kit struct {
	Cluster *cluster.Cluster
	Policy  *policy.Policy

	mu    sync.Mutex // ADK runs parallel tool calls concurrently
	pages []Page
}

type Page struct {
	At       time.Time `json:"at"`
	Oncall   string    `json:"oncall"`
	Summary  string    `json:"summary"`
	Approved bool      `json:"approved"`
}

func New(c *cluster.Cluster, p *policy.Policy) *Kit {
	return &Kit{Cluster: c, Policy: p}
}

// Pages returns a copy of every page sent so far.
func (k *Kit) Pages() []Page {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]Page(nil), k.pages...)
}

func (k *Kit) ReadOnly() ([]tool.Tool, error) {
	overview, err := functiontool.New(functiontool.Config{
		Name:        "cluster_overview",
		Description: "Read-only snapshot of services, replica readiness, error rate, p99, CPU and memory.",
	}, k.clusterOverview)
	if err != nil {
		return nil, err
	}
	pods, err := functiontool.New(functiontool.Config{
		Name:        "list_pods",
		Description: "List pods for a service (phase, ready, restarts, image, last state). Read-only.",
	}, k.listPods)
	if err != nil {
		return nil, err
	}
	metrics, err := functiontool.New(functiontool.Config{
		Name:        "get_metrics",
		Description: "Get error rate, p99 latency, RPS, CPU and memory for a service. Read-only.",
	}, k.getMetrics)
	if err != nil {
		return nil, err
	}
	logs, err := functiontool.New(functiontool.Config{
		Name:        "get_logs",
		Description: "Tail recent logs for a service or a specific pod. Read-only. Never mutates the cluster.",
	}, k.getLogs)
	if err != nil {
		return nil, err
	}
	history, err := functiontool.New(functiontool.Config{
		Name:        "get_deploy_history",
		Description: "Show recent image deploys for a service. Read-only.",
	}, k.getDeployHistory)
	if err != nil {
		return nil, err
	}
	events, err := functiontool.New(functiontool.Config{
		Name:        "get_cluster_events",
		Description: "Tail Kubernetes-style events (OOMKilled, BackOff, HighErrorRate). Read-only.",
	}, k.getEvents)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{overview, pods, metrics, logs, history, events}, nil
}

func (k *Kit) CatalogAndPolicy() ([]tool.Tool, error) {
	catalog, err := functiontool.New(functiontool.Config{
		Name:        "list_allowed_actions",
		Description: "Describe remediation actions that exist in the catalog. Does not execute them.",
	}, k.listAllowedActions)
	if err != nil {
		return nil, err
	}
	check, err := functiontool.New(functiontool.Config{
		Name:        "check_policy",
		Description: "Evaluate a proposed action against production constraints. Does not execute the action.",
	}, k.checkPolicy)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{catalog, check}, nil
}

func (k *Kit) ReviewerOnly() ([]tool.Tool, error) {
	base, err := k.CatalogAndPolicy()
	if err != nil {
		return nil, err
	}
	page, err := functiontool.New(functiontool.Config{
		Name:        "page_oncall",
		Description: "Page the on-call engineer with a reviewed plan and a 1-click execute payload. Does not mutate the cluster.",
	}, k.pageOncall)
	if err != nil {
		return nil, err
	}
	exec, err := functiontool.New(functiontool.Config{
		Name:                "execute_remediation",
		Description:         "Execute a policy-checked remediation (restart, rollback, or scale). Requires human confirmation. Blocked if policy check fails.",
		RequireConfirmation: true,
	}, k.executeRemediation)
	if err != nil {
		return nil, err
	}
	return append(base, page, exec), nil
}
