package agents

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"incident-response-swarm/cluster"
	"incident-response-swarm/policy"
	"incident-response-swarm/tools"

	"google.golang.org/adk/v2/model"
)

func newKit() *tools.Kit { return tools.New(cluster.NewDemo(), policy.Production()) }

func TestStageToolAssignment(t *testing.T) {
	ss, err := stages(newKit())
	if err != nil {
		t.Fatal(err)
	}
	readOnly := []string{"cluster_overview", "list_pods", "get_metrics", "get_logs", "get_deploy_history", "get_cluster_events"}
	want := map[string][]string{
		"TriageAgent":      readOnly,
		"DiagnosticsAgent": readOnly,
		"RemediationAgent": {"list_allowed_actions", "check_policy"},
		"ReviewerAgent":    {"list_allowed_actions", "check_policy", "page_oncall", "execute_remediation"},
	}
	for _, s := range ss {
		var got []string
		for _, tl := range s.tools {
			got = append(got, tl.Name())
		}
		if !slices.Equal(got, want[s.name]) {
			t.Errorf("%s tools = %v, want %v", s.name, got, want[s.name])
		}
	}
}

func TestPipelineOrder(t *testing.T) {
	p, err := NewPipeline(stubLLM{}, newKit())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range p.SubAgents() {
		got = append(got, a.Name())
	}
	want := []string{"TriageAgent", "DiagnosticsAgent", "RemediationAgent", "ReviewerAgent"}
	if !slices.Equal(got, want) {
		t.Fatalf("stage order = %v, want %v", got, want)
	}
}

// stubLLM lets the pipeline be built without an API key. It is never called.
type stubLLM struct{}

func (stubLLM) Name() string { return "stub" }

func (stubLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, errors.New("stub model"))
	}
}
