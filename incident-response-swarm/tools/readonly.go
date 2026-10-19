package tools

import (
	"fmt"
	"time"

	"google.golang.org/adk/v2/agent"
)

type emptyArgs struct{}

type overviewResult struct {
	Snapshot string `json:"snapshot"`
}

func (k *Kit) clusterOverview(_ agent.Context, _ emptyArgs) (overviewResult, error) {
	return overviewResult{Snapshot: k.Cluster.Overview()}, nil
}

type listPodsArgs struct {
	Service string `json:"service"`
}

type podView struct {
	Name          string `json:"name"`
	Service       string `json:"service"`
	Phase         string `json:"phase"`
	Ready         bool   `json:"ready"`
	Restarts      int    `json:"restarts"`
	Image         string `json:"image"`
	LastState     string `json:"last_state"`
	MemoryMi      int    `json:"memory_mi"`
	MemoryLimitMi int    `json:"memory_limit_mi"`
}

type listPodsResult struct {
	Pods []podView `json:"pods"`
}

func (k *Kit) listPods(_ agent.Context, in listPodsArgs) (listPodsResult, error) {
	pods := k.Cluster.ListPods(in.Service)
	out := make([]podView, 0, len(pods))
	for _, p := range pods {
		out = append(out, podView{
			Name: p.Name, Service: p.Service, Phase: string(p.Phase), Ready: p.Ready,
			Restarts: p.Restarts, Image: p.Image, LastState: p.LastState,
			MemoryMi: p.MemoryMi, MemoryLimitMi: p.MemoryLimitMi,
		})
	}
	return listPodsResult{Pods: out}, nil
}

type serviceArgs struct {
	Service string `json:"service"`
}

type metricsResult struct {
	Found         bool    `json:"found"`
	ErrorRate     float64 `json:"error_rate"`
	LatencyP99Ms  int     `json:"latency_p99_ms"`
	RPS           int     `json:"rps"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
}

func (k *Kit) getMetrics(_ agent.Context, in serviceArgs) (metricsResult, error) {
	m, ok := k.Cluster.GetMetrics(in.Service)
	if !ok {
		return metricsResult{Found: false}, nil
	}
	return metricsResult{
		Found: true, ErrorRate: m.ErrorRate, LatencyP99Ms: m.LatencyP99Ms,
		RPS: m.RPS, CPUPercent: m.CPUPercent, MemoryPercent: m.MemoryPercent,
	}, nil
}

type getLogsArgs struct {
	Service string `json:"service"`
	Pod     string `json:"pod"`
	Tail    int    `json:"tail"`
}

type logView struct {
	Time    string `json:"time"`
	Pod     string `json:"pod"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type getLogsResult struct {
	Lines []logView `json:"lines"`
}

func (k *Kit) getLogs(_ agent.Context, in getLogsArgs) (getLogsResult, error) {
	lines := k.Cluster.GetLogs(in.Service, in.Pod, in.Tail)
	out := make([]logView, 0, len(lines))
	for _, l := range lines {
		out = append(out, logView{
			Time: l.Time.Format(time.RFC3339), Pod: l.Pod, Level: l.Level, Message: l.Message,
		})
	}
	return getLogsResult{Lines: out}, nil
}

type getDeployResult struct {
	History []string `json:"history"`
}

func (k *Kit) getDeployHistory(_ agent.Context, in serviceArgs) (getDeployResult, error) {
	hist := k.Cluster.DeployHistory(in.Service)
	out := make([]string, 0, len(hist))
	for _, d := range hist {
		out = append(out, fmt.Sprintf("%s image=%s result=%s", d.At.Format(time.RFC3339), d.Image, d.Result))
	}
	return getDeployResult{History: out}, nil
}

type tailArgs struct {
	Tail int `json:"tail"`
}

type getEventsResult struct {
	Events []string `json:"events"`
}

func (k *Kit) getEvents(_ agent.Context, in tailArgs) (getEventsResult, error) {
	events := k.Cluster.Events(in.Tail)
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s %s %s: %s", e.Time.Format(time.RFC3339), e.Object, e.Reason, e.Message))
	}
	return getEventsResult{Events: out}, nil
}
