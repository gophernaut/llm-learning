// Package policy is the hard safety boundary the Reviewer agent cannot talk
// its way around. check_policy and execute_remediation both run these rules
// in Go; a prompt that says "the freeze window does not apply" still fails.
package policy

import "fmt"

type ActionType string

const (
	ActionRestart  ActionType = "restart"
	ActionRollback ActionType = "rollback"
	ActionScale    ActionType = "scale"
)

type Action struct {
	Type     ActionType `json:"type"`
	Service  string     `json:"service"`
	Pod      string     `json:"pod"`
	Replicas int        `json:"replicas"`
	Reason   string     `json:"reason"`

	// Filled from the cluster by the tools layer, never by the model.
	PodService    string `json:"-"`
	PodReady      bool   `json:"-"`
	ReadyReplicas int    `json:"-"`
}

type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Policy struct {
	ProductionFreeze bool
	MinReplicas      map[string]int
	MaxReplicas      map[string]int
	KnownServices    map[string]bool
}

func Production() *Policy {
	return &Policy{
		ProductionFreeze: false,
		MinReplicas: map[string]int{
			"payments-svc":  2,
			"checkout-api":  2,
			"search-api":    2,
			"inventory-api": 1,
			"frontend":      2,
		},
		MaxReplicas: map[string]int{
			"payments-svc":  6,
			"checkout-api":  12,
			"search-api":    8,
			"inventory-api": 6,
			"frontend":      12,
		},
		KnownServices: map[string]bool{
			"payments-svc":  true,
			"checkout-api":  true,
			"search-api":    true,
			"inventory-api": true,
			"frontend":      true,
		},
	}
}

func (p *Policy) Check(a Action) []Violation {
	var v []Violation
	if a.Type == "" {
		v = append(v, Violation{Code: "missing_type", Message: "action type is required (restart|rollback|scale)"})
		return v
	}
	if p.ProductionFreeze {
		v = append(v, Violation{
			Code:    "production_freeze",
			Message: "production freeze is active; mutating actions are blocked",
		})
	}
	svc := a.Service
	if svc == "" {
		svc = a.PodService
	}
	if a.Type != ActionRestart && svc == "" {
		v = append(v, Violation{Code: "missing_service", Message: "service is required"})
	}
	if svc != "" && p.KnownServices != nil && !p.KnownServices[svc] {
		v = append(v, Violation{Code: "unknown_service", Message: fmt.Sprintf("service %q is not in the allowed catalog", svc)})
	}
	switch a.Type {
	case ActionRestart:
		if a.Pod == "" {
			v = append(v, Violation{Code: "missing_pod", Message: "restart requires a pod name"})
		}
		if a.Service != "" && a.PodService != "" && a.Service != a.PodService {
			v = append(v, Violation{Code: "pod_service_mismatch", Message: fmt.Sprintf("pod %s belongs to %s, not %s", a.Pod, a.PodService, a.Service)})
		}
		if min, ok := p.MinReplicas[svc]; ok && a.PodReady && a.ReadyReplicas-1 < min {
			v = append(v, Violation{Code: "below_min_ready", Message: fmt.Sprintf("restarting %s would leave %s with %d ready replicas (min %d)", a.Pod, svc, a.ReadyReplicas-1, min)})
		}
	case ActionRollback:
		// service already required
	case ActionScale:
		if min, ok := p.MinReplicas[svc]; ok && a.Replicas < min {
			v = append(v, Violation{Code: "below_min_replicas", Message: fmt.Sprintf("%s cannot scale below %d (requested %d)", svc, min, a.Replicas)})
		}
		if max, ok := p.MaxReplicas[svc]; ok && a.Replicas > max {
			v = append(v, Violation{Code: "above_max_replicas", Message: fmt.Sprintf("%s cannot scale above %d (requested %d)", svc, max, a.Replicas)})
		}
	default:
		v = append(v, Violation{Code: "unknown_action", Message: fmt.Sprintf("action %q is not in the catalog", a.Type)})
	}
	return v
}

func (p *Policy) Catalog() []string {
	return []string{
		"restart — recycle a single pod (does not change image)",
		"rollback — restore the previous image for a service",
		"scale — change desired replica count within min/max",
	}
}
