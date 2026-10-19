// Command trace runs the pipeline once on an alert file and prints every
// tool call, tool result, confirmation request, and the cluster state after.
// The console launcher only prints model text; this shows what the agents did.
//
//	go run ./cmd/trace -alert testdata/alerts/error-rate.txt -confirm yes
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"incident-response-swarm/agents"
	"incident-response-swarm/cluster"
	"incident-response-swarm/policy"
	"incident-response-swarm/tools"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

func main() {
	alertPath := flag.String("alert", "testdata/alerts/error-rate.txt", "alert file to send as the user message")
	confirm := flag.String("confirm", "no", "answer to execute_remediation confirmation (yes|no)")
	freeze := flag.Bool("freeze", false, "production freeze")
	inject := flag.Bool("inject", false, "plant a hostile User-Agent in checkout-api logs")
	modelName := flag.String("model", "", "model name (default: OPENAI_MODEL or GEMINI_MODEL, see agents.NewModel)")
	full := flag.Bool("full", false, "print tool results and agent text untruncated")
	flag.Parse()

	ctx := context.Background()
	alert, err := os.ReadFile(*alertPath)
	if err != nil {
		log.Fatal(err)
	}
	m, err := agents.NewModel(ctx, *modelName)
	if err != nil {
		log.Fatal(err)
	}

	demo := cluster.NewDemo()
	if *inject {
		demo.InjectHostileLog()
	}
	pol := policy.Production()
	pol.ProductionFreeze = *freeze
	kit := tools.New(demo, pol)
	pipeline, err := agents.NewPipeline(m, kit)
	if err != nil {
		log.Fatal(err)
	}

	sessions := session.InMemoryService()
	sess, err := sessions.Create(ctx, &session.CreateRequest{AppName: "trace", UserID: "u"})
	if err != nil {
		log.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "trace", Agent: pipeline, SessionService: sessions})
	if err != nil {
		log.Fatal(err)
	}

	limit := 240
	if *full {
		limit = 0
	}
	fmt.Printf("model=%s freeze=%v inject=%v alert=%s confirm=%s\n\n", m.Name(), *freeze, *inject, *alertPath, *confirm)

	start := time.Now()
	var tokens, calls int
	msg := genai.NewContentFromText(string(alert), genai.RoleUser)
	for msg != nil {
		var pending []*genai.FunctionCall
		for ev, err := range r.Run(ctx, "u", sess.Session.ID(), msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
				continue
			}
			// Streaming chunks are repeated in the final event; count and print only that.
			if ev.LLMResponse.Partial {
				continue
			}
			if u := ev.LLMResponse.UsageMetadata; u != nil {
				tokens += int(u.TotalTokenCount)
				calls++
			}
			if ev.LLMResponse.Content == nil {
				continue
			}
			for _, p := range ev.LLMResponse.Content.Parts {
				switch {
				case p.FunctionCall != nil && p.FunctionCall.Name == toolconfirmation.FunctionCallName:
					if isLongRunning(ev, p.FunctionCall.ID) {
						pending = append(pending, p.FunctionCall)
						orig, _ := toolconfirmation.OriginalCallFrom(p.FunctionCall)
						if orig != nil {
							fmt.Printf("[%s] ?? CONFIRM %s %s\n", ev.Author, orig.Name, compact(orig.Args, 0))
						}
					}
				case p.FunctionCall != nil:
					fmt.Printf("[%s] -> %s %s\n", ev.Author, p.FunctionCall.Name, compact(p.FunctionCall.Args, 0))
				case p.FunctionResponse != nil:
					fmt.Printf("[%s] <- %s %s\n", ev.Author, p.FunctionResponse.Name, compact(p.FunctionResponse.Response, limit))
				case p.Text != "" && !p.Thought:
					fmt.Printf("[%s] says:\n%s\n\n", ev.Author, indent(truncate(p.Text, limit*8)))
				}
			}
		}
		msg = nil
		if len(pending) > 0 {
			answer := strings.EqualFold(*confirm, "yes")
			fmt.Printf("[human] confirmed=%v\n", answer)
			var parts []*genai.Part
			for _, fc := range pending {
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
					ID: fc.ID, Name: fc.Name, Response: map[string]any{"confirmed": answer},
				}})
			}
			msg = &genai.Content{Role: string(genai.RoleUser), Parts: parts}
		}
	}

	fmt.Printf("\n--- after run (%s, %d model calls, %d tokens) ---\n", time.Since(start).Round(time.Second), calls, tokens)
	for _, svc := range demo.ListServices() {
		fmt.Printf("%s image=%s\n", svc.Name, svc.Image)
	}
	for _, a := range demo.AuditLog() {
		fmt.Printf("audit: %s %s %s ok=%v %s\n", a.Actor, a.Action, a.Target, a.OK, a.Detail)
	}
	for _, p := range kit.Pages() {
		fmt.Printf("page: %s approved=%v %q\n", p.Oncall, p.Approved, p.Summary)
	}
}

func isLongRunning(ev *session.Event, id string) bool {
	for _, x := range ev.LongRunningToolIDs {
		if x == id {
			return true
		}
	}
	return false
}

func compact(v any, limit int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return truncate(string(b), limit)
}

func truncate(s string, limit int) string {
	if limit > 0 && len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}
