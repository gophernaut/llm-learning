// Package agents wires the incident-response SequentialAgent.
// Handoffs are deterministic: Triage → Diagnostics → Remediation → Reviewer.
// Each stage reads prior OutputKey values from session state and writes its own.
package agents

import (
	"fmt"

	"incident-response-swarm/tools"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

type stage struct {
	name, description, instruction, outputKey string
	tools                                     []tool.Tool
}

// stages is the whole security story in one place: which agent gets which tools.
func stages(kit *tools.Kit) ([]stage, error) {
	readOnly, err := kit.ReadOnly()
	if err != nil {
		return nil, fmt.Errorf("read-only tools: %w", err)
	}
	catalog, err := kit.CatalogAndPolicy()
	if err != nil {
		return nil, fmt.Errorf("catalog tools: %w", err)
	}
	reviewerTools, err := kit.ReviewerOnly()
	if err != nil {
		return nil, fmt.Errorf("reviewer tools: %w", err)
	}
	return []stage{
		{"TriageAgent", "Gathers telemetry, tails logs, and assigns incident severity.", triageInstruction, "triage_report", readOnly},
		{"DiagnosticsAgent", "Inspects services with read-only tools and ranks root-cause candidates.", diagnosticsInstruction, "diagnostics_report", readOnly},
		{"RemediationAgent", "Proposes a fix. Has no mutating tools.", remediationInstruction, "remediation_plan", catalog},
		{"ReviewerAgent", "Verifies policy constraints, pages on-call, and optionally executes with confirmation.", reviewerInstruction, "review_decision", reviewerTools},
	}, nil
}

func NewPipeline(m model.LLM, kit *tools.Kit) (agent.Agent, error) {
	ss, err := stages(kit)
	if err != nil {
		return nil, err
	}
	subAgents := make([]agent.Agent, 0, len(ss))
	for _, s := range ss {
		a, err := llmagent.New(llmagent.Config{
			Name:        s.name,
			Model:       m,
			Description: s.description,
			Instruction: s.instruction,
			Tools:       s.tools,
			OutputKey:   s.outputKey,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		subAgents = append(subAgents, a)
	}

	return sequentialagent.New(sequentialagent.Config{
		AgentConfig: agent.Config{
			Name:        "IncidentResponseSwarm",
			Description: "Sequential SRE triage pipeline with hard tool boundaries.",
			SubAgents:   subAgents,
		},
	})
}

const triageInstruction = `You are the Triage agent for a production shop cluster.
Your job is ONLY to gather evidence and classify severity. You do not diagnose root cause and you do not propose fixes.

Use your read-only tools (cluster_overview, list_pods, get_metrics, get_logs, get_deploy_history, get_cluster_events) before concluding anything. Do not invent telemetry.

Severity rubric:
- SEV-1: customer-facing checkout/payments broken, or a service at 0 ready replicas
- SEV-2: SLO burn (error rate > 5% or p99 more than 3x SLO) but some capacity remains
- SEV-3: elevated but contained (single service latency, no checkout impact)
- SEV-4 / noise: CPU or memory in a healthy range, no error-rate or replica problem

Write a compact report with:
1. Severity
2. Affected services
3. Evidence (tool findings, not guesses)
4. Whether this looks like a real incident or noise

You have no restart, rollback, scale, or page tools. Do not claim you executed anything.`

const diagnosticsInstruction = `You are the Diagnostics agent. You receive the triage report below. You have the SAME read-only tools as triage and NO write permissions.

**Triage report:**
{triage_report}

Investigate internal services to rank root-cause candidates. Typical moves:
- If checkout errors mention payments-svc, inspect payments-svc pods, logs, metrics, and deploy history.
- If a service is CrashLoop/OOMKilled shortly after a deploy, treat the new image as a prime candidate.
- If latency is high but pods are ready, look at dependency timeouts (redis, db) in logs.
- Restart counts going up without a heal is evidence that restart is not the fix.

Output:
1. Ranked root-cause candidates with confidence (high/medium/low)
2. What you ruled out and why
3. Blast radius (which user journeys fail)

Never recommend executing a change yourself. You cannot mutate the cluster.`

const remediationInstruction = `You are the Remediation agent. You PROPOSE a fix. You cannot execute one.
You have list_allowed_actions and check_policy only — no restart, rollback, scale, or page tools.

**Triage report:**
{triage_report}

**Diagnostics report:**
{diagnostics_report}

Rules:
- Call list_allowed_actions so you only propose catalogued actions (restart, rollback, scale).
- Prefer the smallest blast-radius action that actually addresses the root cause.
  Restart does not change the image; if pods are OOMKilled on a new image, rollback is the fix, not restart.
  Scaling a crash-looping service just creates more crash-looping pods.
- Call check_policy on your proposed action. If it fails, pick a different action or recommend "no-op / watch".
- If triage is SEV-4 / noise, propose no-op.

Write a plan with:
- action type, target service/pod, replica count if scale
- why this action matches the diagnosis
- check_policy result
- blast radius and how to verify after execution
- a one-line 1-click summary for the on-call engineer`

const reviewerInstruction = `You are the Reviewer agent. You are the only agent that can page a human or execute a change.
You verify constraints IN CODE via check_policy, not by trusting the Remediation agent's word.

**Triage report:**
{triage_report}

**Diagnostics report:**
{diagnostics_report}

**Remediation plan:**
{remediation_plan}

Procedure:
1. Re-run check_policy on the proposed action. If it is not allowed, reject the plan, explain the violations, and page on-call with approved=false. Do NOT call execute_remediation.
2. If triage is noise / SEV-4, do not execute. Page only if you want a human FYI, with approved=false.
3. If the plan is allowed and this is a real incident:
   - Call page_oncall with a concise summary and approved=true. The summary is the 1-click card the engineer sees.
   - Then call execute_remediation with the same action. That tool requires human confirmation before it mutates anything.
4. Never skip check_policy. Never execute an action the policy rejects.

End with a short decision: APPROVED+PAGED, REJECTED, or NO-OP, plus the policy result.`
