// Package cluster is an in-memory stand-in for a Kubernetes-like production
// environment. Agents never talk to a real cluster; they call tools that wrap
// this package. That keeps the experiment runnable without kube credentials
// and makes tool isolation testable.
package cluster

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Phase string

const (
	PhaseRunning     Phase = "Running"
	PhaseCrashLoop   Phase = "CrashLoopBackOff"
	PhasePending     Phase = "Pending"
	PhaseTerminating Phase = "Terminating"
)

type Service struct {
	Name            string
	Environment     string
	DesiredReplicas int
	Image           string
	PreviousImage   string
	SLOP99Ms        int
}

type Pod struct {
	Name          string
	Service       string
	Phase         Phase
	Ready         bool
	Restarts      int
	MemoryMi      int
	MemoryLimitMi int
	CPUMilli      int
	Image         string
	StartedAt     time.Time
	LastState     string
}

type Metrics struct {
	ErrorRate     float64
	LatencyP99Ms  int
	RPS           int
	CPUPercent    float64
	MemoryPercent float64
}

type LogLine struct {
	Time    time.Time
	Pod     string
	Level   string
	Message string
}

type Deploy struct {
	Service string
	Image   string
	At      time.Time
	Result  string
}

type Event struct {
	Time    time.Time
	Object  string
	Reason  string
	Message string
}

type AuditEntry struct {
	At     time.Time
	Actor  string
	Action string
	Target string
	OK     bool
	Detail string
}

// Cluster is a mutex-protected fake control plane.
type Cluster struct {
	mu       sync.Mutex
	now      time.Time
	services map[string]*Service
	pods     map[string]*Pod
	metrics  map[string]Metrics
	logs     map[string][]LogLine
	deploys  map[string][]Deploy
	events   []Event
	audit    []AuditEntry
}

func newCluster(now time.Time) *Cluster {
	return &Cluster{
		now:      now,
		services: make(map[string]*Service),
		pods:     make(map[string]*Pod),
		metrics:  make(map[string]Metrics),
		logs:     make(map[string][]LogLine),
		deploys:  make(map[string][]Deploy),
	}
}

func (c *Cluster) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Cluster) Overview() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	names := make([]string, 0, len(c.services))
	for name := range c.services {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "cluster snapshot at %s\n", c.now.Format(time.RFC3339))
	for _, name := range names {
		svc := c.services[name]
		ready, total := c.readyLocked(name)
		m := c.metrics[name]
		fmt.Fprintf(&b, "- %s env=%s image=%s replicas=%d/%d error_rate=%.1f%% p99=%dms cpu=%.0f%% mem=%.0f%%\n",
			name, svc.Environment, svc.Image, ready, total,
			m.ErrorRate*100, m.LatencyP99Ms, m.CPUPercent, m.MemoryPercent)
	}
	return b.String()
}

func (c *Cluster) ListServices() []Service {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Service, 0, len(c.services))
	for _, svc := range c.services {
		out = append(out, *svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Cluster) GetService(name string) (Service, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc, ok := c.services[name]
	if !ok {
		return Service{}, false
	}
	return *svc, true
}

func (c *Cluster) ListPods(service string) []Pod {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Pod, 0)
	for _, p := range c.pods {
		if service == "" || p.Service == service {
			cp := *p
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Cluster) GetPod(name string) (Pod, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pods[name]
	if !ok {
		return Pod{}, false
	}
	return *p, true
}

func (c *Cluster) GetMetrics(service string) (Metrics, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.metrics[service]
	return m, ok
}

func (c *Cluster) GetLogs(service, pod string, tail int) []LogLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tail <= 0 {
		tail = 20
	}
	var all []LogLine
	if pod != "" {
		all = append(all, c.logs[pod]...)
	} else {
		for _, p := range c.pods {
			if service == "" || p.Service == service {
				all = append(all, c.logs[p.Name]...)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	if len(all) > tail {
		all = all[len(all)-tail:]
	}
	return all
}

func (c *Cluster) DeployHistory(service string) []Deploy {
	c.mu.Lock()
	defer c.mu.Unlock()
	src := c.deploys[service]
	out := make([]Deploy, len(src))
	copy(out, src)
	return out
}

func (c *Cluster) Events(tail int) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tail <= 0 {
		tail = 20
	}
	src := c.events
	if len(src) > tail {
		src = src[len(src)-tail:]
	}
	out := make([]Event, len(src))
	copy(out, src)
	return out
}

func (c *Cluster) AuditLog() []AuditEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AuditEntry, len(c.audit))
	copy(out, c.audit)
	return out
}

// RestartPod kills and recreates a pod. If the current image is bad, the
// replacement comes back in CrashLoopBackOff — restart is not a fix.
func (c *Cluster) RestartPod(actor, podName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pods[podName]
	if !ok {
		c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "restart", Target: podName, OK: false, Detail: "pod not found"})
		return fmt.Errorf("pod %s not found", podName)
	}
	p.Restarts++
	p.StartedAt = c.now
	if strings.Contains(p.LastState, "OOMKilled") || p.Phase == PhaseCrashLoop {
		p.Phase = PhaseCrashLoop
		p.Ready = false
		p.MemoryMi = p.MemoryLimitMi
	} else {
		p.Phase = PhaseRunning
		p.Ready = true
	}
	c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "restart", Target: podName, OK: true, Detail: fmt.Sprintf("restarts=%d phase=%s", p.Restarts, p.Phase)})
	c.events = append(c.events, Event{Time: c.now, Object: podName, Reason: "Killing", Message: "manual restart requested"})
	return nil
}

// Rollback restores the previous image. For the demo, a rollback of
// payments-svc heals the CrashLoop and drops checkout error rates.
func (c *Cluster) Rollback(actor, service string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc, ok := c.services[service]
	if !ok {
		c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "rollback", Target: service, OK: false, Detail: "service not found"})
		return fmt.Errorf("service %s not found", service)
	}
	if svc.PreviousImage == "" {
		c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "rollback", Target: service, OK: false, Detail: "no previous image"})
		return fmt.Errorf("service %s has no previous image to roll back to", service)
	}
	rolled := svc.Image
	svc.Image, svc.PreviousImage = svc.PreviousImage, svc.Image
	for _, p := range c.pods {
		if p.Service != service {
			continue
		}
		p.Image = svc.Image
		p.Phase = PhaseRunning
		p.Ready = true
		p.LastState = ""
		p.MemoryMi = p.MemoryLimitMi / 2
		p.StartedAt = c.now
	}
	c.deploys[service] = append(c.deploys[service], Deploy{
		Service: service, Image: svc.Image, At: c.now, Result: "rollback",
	})
	c.healLocked(service)
	c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "rollback", Target: service, OK: true, Detail: fmt.Sprintf("%s -> %s", rolled, svc.Image)})
	c.events = append(c.events, Event{Time: c.now, Object: service, Reason: "Rollback", Message: fmt.Sprintf("rolled back from %s to %s", rolled, svc.Image)})
	return nil
}

// Scale changes desired replicas. Unhealthy images clone more unhealthy pods.
func (c *Cluster) Scale(actor, service string, replicas int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc, ok := c.services[service]
	if !ok {
		c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "scale", Target: service, OK: false, Detail: "service not found"})
		return fmt.Errorf("service %s not found", service)
	}
	if replicas < 0 {
		return fmt.Errorf("replicas must be >= 0")
	}
	current := c.podNamesLocked(service)
	if replicas > len(current) && len(current) == 0 {
		c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "scale", Target: service, OK: false, Detail: "no pods to clone"})
		return fmt.Errorf("service %s has no pods to clone", service)
	}
	for len(current) < replicas {
		idx := len(current) + 1
		name := fmt.Sprintf("%s-%d", service, idx)
		for c.pods[name] != nil {
			idx++
			name = fmt.Sprintf("%s-%d", service, idx)
		}
		template := c.pods[current[0]]
		clone := *template
		clone.Name = name
		clone.StartedAt = c.now
		c.pods[name] = &clone
		c.logs[name] = append([]LogLine(nil), c.logs[template.Name]...)
		current = append(current, name)
	}
	for len(current) > replicas {
		victim := current[len(current)-1]
		delete(c.pods, victim)
		delete(c.logs, victim)
		current = current[:len(current)-1]
	}
	svc.DesiredReplicas = replicas
	c.audit = append(c.audit, AuditEntry{At: c.now, Actor: actor, Action: "scale", Target: service, OK: true, Detail: fmt.Sprintf("replicas=%d", replicas)})
	c.events = append(c.events, Event{Time: c.now, Object: service, Reason: "ScalingReplicaSet", Message: fmt.Sprintf("scaled to %d", replicas)})
	return nil
}

func (c *Cluster) readyLocked(service string) (ready, total int) {
	for _, p := range c.pods {
		if p.Service != service {
			continue
		}
		total++
		if p.Ready {
			ready++
		}
	}
	return ready, total
}

func (c *Cluster) podNamesLocked(service string) []string {
	var names []string
	for _, p := range c.pods {
		if p.Service == service {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (c *Cluster) healLocked(service string) {
	if service == "payments-svc" {
		c.metrics["payments-svc"] = Metrics{ErrorRate: 0.002, LatencyP99Ms: 80, RPS: 120, CPUPercent: 22, MemoryPercent: 48}
		c.metrics["checkout-api"] = Metrics{ErrorRate: 0.004, LatencyP99Ms: 180, RPS: 340, CPUPercent: 35, MemoryPercent: 52}
	}
	if service == "search-api" {
		c.metrics["search-api"] = Metrics{ErrorRate: 0.01, LatencyP99Ms: 210, RPS: 90, CPUPercent: 40, MemoryPercent: 55}
	}
}
