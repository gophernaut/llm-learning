package tools

import (
	"testing"

	"incident-response-swarm/cluster"
	"incident-response-swarm/policy"
)

func TestReadOnlyToolsetDoesNotIncludeMutators(t *testing.T) {
	k := New(cluster.NewDemo(), policy.Production())
	ro, err := k.ReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range ro {
		names[tool.Name()] = true
	}
	for _, forbidden := range []string{"execute_remediation", "page_oncall", "restart_pod", "rollback", "scale"} {
		if names[forbidden] {
			t.Fatalf("read-only toolset includes mutating tool %q", forbidden)
		}
	}
	if !names["get_logs"] || !names["cluster_overview"] {
		t.Fatalf("missing expected read-only tools: %v", names)
	}
}

func TestReviewerOwnsExecute(t *testing.T) {
	k := New(cluster.NewDemo(), policy.Production())
	rev, err := k.ReviewerOnly()
	if err != nil {
		t.Fatal(err)
	}
	var hasExec, hasPage bool
	for _, tool := range rev {
		if tool.Name() == "execute_remediation" {
			hasExec = true
		}
		if tool.Name() == "page_oncall" {
			hasPage = true
		}
	}
	if !hasExec || !hasPage {
		t.Fatal("reviewer toolset must include execute_remediation and page_oncall")
	}
}

func TestExecuteRespectsPolicy(t *testing.T) {
	c := cluster.NewDemo()
	p := policy.Production()
	p.ProductionFreeze = true
	k := New(c, p)
	res, err := k.executeRemediation(nil, executeArgs{Type: "rollback", Service: "payments-svc"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "blocked" {
		t.Fatalf("freeze should block execute, got %+v", res)
	}
	svc, _ := c.GetService("payments-svc")
	if svc.Image != "gcr.io/shop/payments:1.14.0" {
		t.Fatalf("cluster mutated during freeze: %s", svc.Image)
	}
}

func TestExecuteRollbackWhenAllowed(t *testing.T) {
	c := cluster.NewDemo()
	k := New(c, policy.Production())
	res, err := k.executeRemediation(nil, executeArgs{Type: "rollback", Service: "payments-svc", Reason: "OOM after 1.14.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "executed" {
		t.Fatalf("expected executed, got %+v", res)
	}
	svc, _ := c.GetService("payments-svc")
	if svc.Image != "gcr.io/shop/payments:1.13.2" {
		t.Fatalf("rollback did not apply: %s", svc.Image)
	}
}

func TestReadOnlyHandlersDoNotMutate(t *testing.T) {
	c := cluster.NewDemo()
	k := New(c, policy.Production())
	before := c.Overview()
	if _, err := k.getLogs(nil, getLogsArgs{Service: "payments-svc", Tail: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.clusterOverview(nil, emptyArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.listPods(nil, listPodsArgs{Service: "payments-svc"}); err != nil {
		t.Fatal(err)
	}
	if c.Overview() != before {
		t.Fatal("read-only handlers mutated cluster state")
	}
	if len(c.AuditLog()) != 0 {
		t.Fatalf("read-only path wrote audit entries: %+v", c.AuditLog())
	}
}

// The injected log asks for two changes. Policy stops one in Go; the other is
// a legal action, so only the human confirmation stands between it and prod.
func TestInjectedActionsAgainstPolicy(t *testing.T) {
	c := cluster.NewDemo()
	c.InjectHostileLog()
	k := New(c, policy.Production())

	res, err := k.executeRemediation(nil, executeArgs{Type: "scale", Service: "payments-svc", Replicas: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "blocked" {
		t.Fatalf("scale payments-svc to 0 should be blocked by the replica floor, got %+v", res)
	}

	check, err := k.checkPolicy(nil, policyArgs{Type: "rollback", Service: "checkout-api"})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Allowed {
		t.Fatalf("rollback checkout-api is a legal action; policy alone cannot catch it: %+v", check)
	}
}

func TestRestartResolvesPodFromCluster(t *testing.T) {
	k := New(cluster.NewDemo(), policy.Production())
	cases := []struct {
		args executeArgs
		code string
	}{
		{executeArgs{Type: "restart", Service: "payments-svc", Pod: "frontend-1"}, "pod_service_mismatch"},
		{executeArgs{Type: "restart", Pod: "search-api-1"}, "below_min_ready"},
		{executeArgs{Type: "restart", Pod: "payments-svc-9"}, "pod_not_found"},
	}
	for _, tc := range cases {
		res, err := k.executeRemediation(nil, tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "blocked" || !hasViolation(res.Violations, tc.code) {
			t.Fatalf("%+v: want blocked with %s, got %+v", tc.args, tc.code, res)
		}
	}
	if len(k.Cluster.AuditLog()) != 0 {
		t.Fatalf("blocked restarts reached the cluster: %+v", k.Cluster.AuditLog())
	}
}

func hasViolation(vs []policy.Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

// Seen in live runs: models put the service name in the pod field of a rollback.
func TestRollbackIgnoresPodField(t *testing.T) {
	k := New(cluster.NewDemo(), policy.Production())
	res, err := k.checkPolicy(nil, policyArgs{Type: "rollback", Service: "payments-svc", Pod: "payments-svc"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Allowed {
		t.Fatalf("rollback should not be blocked by an irrelevant pod field: %+v", res)
	}
}
