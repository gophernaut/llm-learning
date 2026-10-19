package policy

import "testing"

func TestScaleFloorsAndCeilings(t *testing.T) {
	p := Production()
	v := p.Check(Action{Type: ActionScale, Service: "payments-svc", Replicas: 0})
	if !hasCode(v, "below_min_replicas") {
		t.Fatalf("expected below_min_replicas, got %+v", v)
	}
	v = p.Check(Action{Type: ActionScale, Service: "payments-svc", Replicas: 99})
	if !hasCode(v, "above_max_replicas") {
		t.Fatalf("expected above_max_replicas, got %+v", v)
	}
	v = p.Check(Action{Type: ActionScale, Service: "payments-svc", Replicas: 4})
	if len(v) != 0 {
		t.Fatalf("legal scale flagged: %+v", v)
	}
}

func TestFreezeBlocksEverything(t *testing.T) {
	p := Production()
	p.ProductionFreeze = true
	v := p.Check(Action{Type: ActionRollback, Service: "payments-svc"})
	if !hasCode(v, "production_freeze") {
		t.Fatalf("freeze should block rollback: %+v", v)
	}
}

func TestUnknownServiceAndAction(t *testing.T) {
	p := Production()
	v := p.Check(Action{Type: ActionRollback, Service: "bitcoin-miner"})
	if !hasCode(v, "unknown_service") {
		t.Fatalf("expected unknown_service: %+v", v)
	}
	v = p.Check(Action{Type: "drop_database", Service: "payments-svc"})
	if !hasCode(v, "unknown_action") {
		t.Fatalf("expected unknown_action: %+v", v)
	}
}

func TestRestartRequiresPod(t *testing.T) {
	p := Production()
	v := p.Check(Action{Type: ActionRestart, Service: "payments-svc"})
	if !hasCode(v, "missing_pod") {
		t.Fatalf("expected missing_pod: %+v", v)
	}
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestRestartPodMustBelongToService(t *testing.T) {
	p := Production()
	v := p.Check(Action{Type: ActionRestart, Service: "payments-svc", Pod: "frontend-1", PodService: "frontend", PodReady: true, ReadyReplicas: 3})
	if !hasCode(v, "pod_service_mismatch") {
		t.Fatalf("expected pod_service_mismatch: %+v", v)
	}
}

func TestRestartRespectsReadyFloor(t *testing.T) {
	p := Production()
	v := p.Check(Action{Type: ActionRestart, Pod: "search-api-1", PodService: "search-api", PodReady: true, ReadyReplicas: 2})
	if !hasCode(v, "below_min_ready") {
		t.Fatalf("expected below_min_ready: %+v", v)
	}
	// A pod that is already not ready does not reduce capacity.
	v = p.Check(Action{Type: ActionRestart, Pod: "payments-svc-1", PodService: "payments-svc", PodReady: false, ReadyReplicas: 0})
	if len(v) != 0 {
		t.Fatalf("restart of a not-ready pod flagged: %+v", v)
	}
}
