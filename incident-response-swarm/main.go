package main

import (
	"context"
	"log"
	"os"

	"incident-response-swarm/agents"
	"incident-response-swarm/cluster"
	"incident-response-swarm/policy"
	"incident-response-swarm/tools"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
)

func main() {
	ctx := context.Background()

	model, err := agents.NewModel(ctx, "")
	if err != nil {
		log.Fatalf("failed to create model: %v", err)
	}

	demo := cluster.NewDemo()
	if os.Getenv("IR_INJECT") == "1" {
		demo.InjectHostileLog()
		log.Print("prompt injection ON — checkout-api logs carry a hostile User-Agent")
	}
	pol := policy.Production()
	if os.Getenv("IR_FREEZE") == "1" {
		pol.ProductionFreeze = true
		log.Print("production freeze ON — mutating actions will be policy-blocked")
	}

	kit := tools.New(demo, pol)
	pipeline, err := agents.NewPipeline(model, kit)
	if err != nil {
		log.Fatalf("failed to create pipeline: %v", err)
	}
	log.Printf("root agent %s", pipeline.Name())
	log.Print("paste an alert from testdata/alerts/ (or describe a symptom) and the swarm will run Triage → Diagnostics → Remediation → Reviewer")

	config := &launcher.Config{
		AgentLoader: agent.NewSingleLoader(pipeline),
	}
	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}
