package tools

import (
	"fmt"
	"strings"

	"incident-response-swarm/policy"

	"google.golang.org/adk/v2/agent"
)

type catalogResult struct {
	Actions []string `json:"actions"`
	Note    string   `json:"note"`
}

func (k *Kit) listAllowedActions(_ agent.Context, _ emptyArgs) (catalogResult, error) {
	return catalogResult{
		Actions: k.Policy.Catalog(),
		Note:    "This tool only lists actions. Execution is owned by the Reviewer via execute_remediation.",
	}, nil
}

type policyArgs struct {
	Type     string `json:"type"`
	Service  string `json:"service"`
	Pod      string `json:"pod"`
	Replicas int    `json:"replicas"`
	Reason   string `json:"reason"`
}

type policyResult struct {
	Allowed    bool               `json:"allowed"`
	Violations []policy.Violation `json:"violations"`
}

func (k *Kit) checkPolicy(_ agent.Context, in policyArgs) (policyResult, error) {
	_, v := k.evaluate(policy.Action{
		Type: policy.ActionType(strings.ToLower(in.Type)), Service: in.Service,
		Pod: in.Pod, Replicas: in.Replicas, Reason: in.Reason,
	})
	return policyResult{Allowed: len(v) == 0, Violations: v}, nil
}

// evaluate fills the cluster-derived fields of a model-proposed action and
// runs the policy. For a restart the model names a pod; the cluster says whose
// pod it is. Other actions are service-scoped, so a stray pod field (models
// often echo the service name there) is ignored.
func (k *Kit) evaluate(a policy.Action) (policy.Action, []policy.Violation) {
	var v []policy.Violation
	if a.Type == policy.ActionRestart && a.Pod != "" {
		pod, ok := k.Cluster.GetPod(a.Pod)
		if !ok {
			v = append(v, policy.Violation{Code: "pod_not_found", Message: fmt.Sprintf("pod %q does not exist", a.Pod)})
		} else {
			a.PodService = pod.Service
			a.PodReady = pod.Ready
			for _, p := range k.Cluster.ListPods(pod.Service) {
				if p.Ready {
					a.ReadyReplicas++
				}
			}
		}
	}
	return a, append(v, k.Policy.Check(a)...)
}

type pageArgs struct {
	Oncall   string `json:"oncall"`
	Summary  string `json:"summary"`
	Approved bool   `json:"approved"`
}

type pageResult struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func (k *Kit) pageOncall(_ agent.Context, in pageArgs) (pageResult, error) {
	who := in.Oncall
	if who == "" {
		who = "sre-oncall"
	}
	k.mu.Lock()
	k.pages = append(k.pages, Page{At: k.Cluster.Now(), Oncall: who, Summary: in.Summary, Approved: in.Approved})
	k.mu.Unlock()
	return pageResult{
		Status: "paged",
		Detail: fmt.Sprintf("paged %s approved=%v: %s", who, in.Approved, in.Summary),
	}, nil
}

type executeArgs struct {
	Type     string `json:"type"`
	Service  string `json:"service"`
	Pod      string `json:"pod"`
	Replicas int    `json:"replicas"`
	Reason   string `json:"reason"`
}

type executeResult struct {
	Status     string             `json:"status"`
	Detail     string             `json:"detail"`
	Violations []policy.Violation `json:"violations"`
}

func (k *Kit) executeRemediation(_ agent.Context, in executeArgs) (executeResult, error) {
	action, v := k.evaluate(policy.Action{
		Type: policy.ActionType(strings.ToLower(in.Type)), Service: in.Service,
		Pod: in.Pod, Replicas: in.Replicas, Reason: in.Reason,
	})
	if len(v) > 0 {
		return executeResult{Status: "blocked", Detail: "policy rejected this action", Violations: v}, nil
	}
	var err error
	switch action.Type {
	case policy.ActionRestart:
		err = k.Cluster.RestartPod("reviewer", in.Pod)
	case policy.ActionRollback:
		err = k.Cluster.Rollback("reviewer", in.Service)
	case policy.ActionScale:
		err = k.Cluster.Scale("reviewer", in.Service, in.Replicas)
	default:
		return executeResult{Status: "blocked", Detail: "unknown action"}, nil
	}
	if err != nil {
		return executeResult{Status: "error", Detail: err.Error()}, nil
	}
	target := action.Service
	if target == "" {
		target = action.PodService
	}
	return executeResult{Status: "executed", Detail: strings.TrimSpace(fmt.Sprintf("%s applied to %s %s", action.Type, target, in.Pod))}, nil
}
