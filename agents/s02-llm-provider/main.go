package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// s02-llm-provider — Provider interface + 2 concrete impls.
//
// Run in mock mode (no API key needed):
//
//	go run . -mock "search hacker news"
//
// Run for real (OpenAI):
//
//	OPENAI_API_KEY=sk-... go run . "what is the capital of France?"
//	OPENAI_API_KEY=sk-... go run . -model gpt-4o-mini "navigate https://example.com"
//
// What this binary does NOT do:
//   - No real agent loop (s01 has that; s02 just exercises the Provider)
//   - No tool dispatch (we print tool_calls; we don't execute them)
//   - No streaming, no structured output schema
func main() {
	useMock := flag.Bool("mock", false, "use MockProvider instead of real OpenAI")
	model := flag.String("model", "gpt-4o-mini", "OpenAI model name")
	baseURL := flag.String("base-url", "", "override base URL (default: https://api.openai.com/v1)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s02 [-mock] [-model NAME] [-base-url URL] <task>\n\n"+
				"  -mock           use MockProvider (no API key needed)\n"+
				"  -model NAME     OpenAI model (default: gpt-4o-mini)\n"+
				"  -base-url URL   override OpenAI base URL\n\n"+
				"  Examples:\n"+
				"    s02 -mock \"search hacker news\"\n"+
				"    OPENAI_API_KEY=sk-... s02 \"what is the capital of France?\"\n")
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	task := strings.Join(flag.Args(), " ")

	var provider Provider
	if *useMock {
		provider = &MockProvider{Queue: hardcodedMockResponses(task)}
	} else {
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "error: OPENAI_API_KEY not set. Use -mock to try without an API key.")
			os.Exit(1)
		}
		provider = &OpenAIProvider{
			APIKey:  apiKey,
			Model:   *model,
			BaseURL: *baseURL,
		}
	}

	tools := []ToolSchema{
		{
			Name:        "search",
			Description: "search the web for a query",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		},
		{
			Name:        "done",
			Description: "finish the task with a final answer",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
		},
	}

	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: task}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Invoke(ctx, msgs, tools)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("model: %s\n", resp.Model)
	fmt.Printf("stop_reason: %s\n", resp.StopReason)
	fmt.Printf("tokens: in=%d out=%d\n", resp.InputTokens, resp.OutputTokens)
	if resp.Text != "" {
		fmt.Printf("text: %s\n", resp.Text)
	}
	for i, a := range resp.Actions {
		fmt.Printf("action[%d]: %s(%s)\n", i, a.Name, a.Input)
	}
}

// hardcodedMockResponses returns a deterministic queue for the -mock flag.
// Two responses so the caller can call Invoke twice if needed; main only
// reads the first.
func hardcodedMockResponses(task string) []Response {
	return []Response{
		{
			Text: fmt.Sprintf("I will search for %q.", task),
			Actions: []ActionCall{
				{Name: "search", Input: fmt.Sprintf(`{"query":%q}`, task)},
			},
			StopReason:   "tool_use",
			InputTokens:  12,
			OutputTokens: 8,
			Model:        "mock-llm",
		},
		{
			Text:         fmt.Sprintf("Task complete: %s", task),
			StopReason:   "end_turn",
			InputTokens:  24,
			OutputTokens: 6,
			Model:        "mock-llm",
		},
	}
}
