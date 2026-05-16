package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
)

// main.go is the end-to-end demo. It wires every previous chapter's
// pieces together against an httptest.Server (so there's no real
// network), runs a 5-step task, and prints:
//
//   - the per-step Verbose log,
//   - the final answer,
//   - the recorded CDP frames,
//   - the cost summary,
//   - the DOM snapshot the agent saw at the end.
//
// Running `go run .` produces deterministic output (the testdata
// expected.txt is captured from this run). Running the tests with
// `go test -v` runs the five named tests on smaller wiring.
//
// The httptest.Server here serves a stub HTML page; the agent doesn't
// actually parse it (the DOMService returns the same fixture
// regardless), but having a real http listener proves that the
// integration code doesn't accidentally short-circuit on "is there a
// server?" The point is to demonstrate: the wiring is correct enough
// that you could swap the stub DOMService for a chromedp one and the
// rest of the code would not move.

func main() {
	ctx := context.Background()

	// Stand up a real HTTP server. We don't actually fetch from it —
	// the DOMService returns a fixture — but having it running proves
	// the integration is "demo-runnable" against a real net listener.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><input name=q><button>Search</button></body></html>")
	}))
	defer ts.Close()

	// Build the pieces.
	cdp := NewRecordingCDPClient()
	nav := NewNavigationWatchdog()
	sess := NewBrowserSession(cdp, nav)
	if err := sess.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "session start: %v\n", err)
		os.Exit(1)
	}
	defer sess.Stop(ctx)

	dom := NewDOMService(sess.Bus)
	dom.SetCurrentURL("https://search.example/")

	reg := NewRegistry()
	RegisterDefaultTools(reg, sess, dom)

	mock := scriptedDemoProvider()
	cost := NewTokenCost()

	sandbox, err := NewLocalFileSystem(filepath.Join(os.TempDir(), "s12-sandbox"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "filesystem init: %v\n", err)
		os.Exit(1)
	}

	agent := &Agent{
		Provider:   mock,
		Fallback:   &OpenAIProvider{BaseURL: ts.URL, Model: "stub"},
		Tools:      reg,
		Session:    sess,
		DOM:        dom,
		Messages:   NewMessageManager(8),
		Cost:       cost,
		FS:         sandbox,
		MaxSteps:   12,
		PlanEvery:  5,
		LLMTimeout: 2 * 1_000_000_000, // 2s, plenty for in-process mock
		Verbose:    os.Stdout,
	}

	// Run.
	fmt.Printf("=== s12 agent run ===\n")
	fmt.Printf("backend URL: %s\n", ts.URL)
	answer, err := agent.Run(ctx, "find the first article on the example search engine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent run: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nFinal answer: %s\n", answer)

	fmt.Printf("\n--- CDP frames ---\n%s", cdp.FrameLog())

	fmt.Printf("\n--- Navigations seen by watchdog ---\n")
	for i, u := range nav.Visited() {
		fmt.Printf("  [%d] %s\n", i, u)
	}

	finalDOM, _ := dom.Get(ctx)
	fmt.Printf("\n--- DOM at end ---\nURL: %s\n%s\n", dom.CurrentURL(), finalDOM.LLMText)

	fmt.Printf("\n--- %s\n", cost.Summary())
}

// scriptedDemoProvider builds the MockProvider used by the demo. The
// scripted sequence drives the agent through:
//
//   step 0 : type "browser-use" into the search input
//   step 1 : search() with the query (navigation → results page)
//   step 2 : click the first result (navigation → article)
//   step 3 : call done() with the final answer.
//
// Token counts are chosen so the cost summary in the demo looks
// realistic without being zero.
func scriptedDemoProvider() *MockProvider {
	return &MockProvider{
		Queue: []Response{
			{
				Text:       "I'll type the query first.",
				Actions:    []ActionCall{{Name: "type", Input: jsonObj(map[string]any{"index": 0, "text": "browser-use"})}},
				StopReason: "tool_use",
				InputTokens: 320, OutputTokens: 40, Model: "gpt-4o-mini",
			},
			{
				Text:       "Now submit search.",
				Actions:    []ActionCall{{Name: "search", Input: jsonObj(map[string]any{"query": "browser-use"})}},
				StopReason: "tool_use",
				InputTokens: 360, OutputTokens: 30, Model: "gpt-4o-mini",
			},
			{
				Text:       "Opening the first result.",
				Actions:    []ActionCall{{Name: "click", Input: jsonObj(map[string]any{"index": 0})}},
				StopReason: "tool_use",
				InputTokens: 420, OutputTokens: 30, Model: "gpt-4o-mini",
			},
			{
				Text:       "Task complete.",
				Actions:    []ActionCall{{Name: "done", Input: jsonObj(map[string]any{"answer": "First article on browser-use"})}},
				StopReason: "tool_use",
				InputTokens: 480, OutputTokens: 50, Model: "gpt-4o-mini",
			},
		},
	}
}

// jsonObj is a tiny helper that marshals a map to a compact JSON
// string. Used by mock setups so the test wiring reads like Python
// dict literals instead of raw `json.RawMessage("{...}")`.
func jsonObj(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}
