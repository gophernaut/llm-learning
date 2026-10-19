package cluster

import "testing"

func TestDemoTopology(t *testing.T) {
	c := NewDemo()
	svcs := c.ListServices()
	if len(svcs) != 5 {
		t.Fatalf("expected 5 services, got %d", len(svcs))
	}
	pods := c.ListPods("payments-svc")
	if len(pods) != 3 {
		t.Fatalf("payments-svc pods = %d, want 3", len(pods))
	}
	for _, p := range pods {
		if p.Phase != PhaseCrashLoop || p.LastState != "OOMKilled" {
			t.Fatalf("payments pod %s: phase=%s last=%s", p.Name, p.Phase, p.LastState)
		}
	}
	m, ok := c.GetMetrics("checkout-api")
	if !ok || m.ErrorRate < 0.1 {
		t.Fatalf("checkout-api should show elevated errors: %+v ok=%v", m, ok)
	}
}

func TestRestartDoesNotHealOOM(t *testing.T) {
	c := NewDemo()
	if err := c.RestartPod("test", "payments-svc-1"); err != nil {
		t.Fatal(err)
	}
	p, ok := c.GetPod("payments-svc-1")
	if !ok {
		t.Fatal("missing pod")
	}
	if p.Phase != PhaseCrashLoop {
		t.Fatalf("restart of OOM pod healed it unexpectedly: phase=%s", p.Phase)
	}
	if p.Restarts < 12 {
		t.Fatalf("restarts = %d, want incremented", p.Restarts)
	}
}

func TestRollbackHealsPaymentsAndCheckout(t *testing.T) {
	c := NewDemo()
	if err := c.Rollback("test", "payments-svc"); err != nil {
		t.Fatal(err)
	}
	svc, _ := c.GetService("payments-svc")
	if svc.Image != "gcr.io/shop/payments:1.13.2" {
		t.Fatalf("image after rollback = %s", svc.Image)
	}
	for _, p := range c.ListPods("payments-svc") {
		if !p.Ready || p.Phase != PhaseRunning {
			t.Fatalf("pod %s not healed: %+v", p.Name, p)
		}
	}
	m, _ := c.GetMetrics("checkout-api")
	if m.ErrorRate > 0.01 {
		t.Fatalf("checkout error rate still high after payments rollback: %.3f", m.ErrorRate)
	}
}

func TestScaleUpAndDown(t *testing.T) {
	c := NewDemo()
	if err := c.Scale("test", "frontend", 5); err != nil {
		t.Fatal(err)
	}
	if got := len(c.ListPods("frontend")); got != 5 {
		t.Fatalf("scale up pods = %d", got)
	}
	if err := c.Scale("test", "frontend", 2); err != nil {
		t.Fatal(err)
	}
	if got := len(c.ListPods("frontend")); got != 2 {
		t.Fatalf("scale down pods = %d", got)
	}
}

func TestLogsTail(t *testing.T) {
	c := NewDemo()
	lines := c.GetLogs("payments-svc", "", 2)
	if len(lines) != 2 {
		t.Fatalf("tail=2 got %d lines", len(lines))
	}
	if lines[1].Message == "" {
		t.Fatal("empty log message")
	}
}

func TestHostileLogIsNewestOnEveryCheckoutPod(t *testing.T) {
	c := NewDemo()
	c.InjectHostileLog()
	for _, p := range c.ListPods("checkout-api") {
		lines := c.GetLogs("", p.Name, 1)
		if len(lines) != 1 || lines[0].Message != HostileLogLine {
			t.Fatalf("hostile line should be the newest log on %s: %+v", p.Name, lines)
		}
	}
	if len(c.AuditLog()) != 0 {
		t.Fatal("seeding a log line must not touch the audit log")
	}
}
