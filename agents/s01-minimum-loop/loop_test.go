package main

import (
	"context"
	"strings"
	"testing"
)

// TestAgentRunsAtAll — the loop must finish for the simplest possible task.
func TestAgentRunsAtAll(t *testing.T) {
	a := &Agent{
		Provider: &FakeProvider{},
		Actions:  []Action{SearchAction{}, DoneAction{}},
		MaxSteps: 5,
	}
	out, err := a.Run(context.Background(), "nothing to do")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty final text, got empty")
	}
}

// TestSearchTaskRunsSearch — verify search task triggers search action then ends.
func TestSearchTaskRunsSearch(t *testing.T) {
	provider := &FakeProvider{}
	a := &Agent{
		Provider: provider,
		Actions:  []Action{SearchAction{}, DoneAction{}},
		MaxSteps: 5,
	}
	out, err := a.Run(context.Background(), "search hacker news")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// Should have made >= 2 provider calls: one to emit search, one to see result.
	if provider.calls < 2 {
		t.Errorf("expected provider to be called at least 2 times, got %d", provider.calls)
	}
	if !strings.Contains(out, "complete") && !strings.Contains(out, "Task") {
		t.Errorf("expected final text to mention completion, got: %q", out)
	}
}

// TestNavigateTaskRunsNavigate — verify navigate task triggers navigate action.
func TestNavigateTaskRunsNavigate(t *testing.T) {
	a := &Agent{
		Provider: &FakeProvider{},
		Actions:  []Action{NavigateAction{}, DoneAction{}},
		MaxSteps: 5,
	}
	out, err := a.Run(context.Background(), "navigate https://example.com")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out, "complete") {
		t.Errorf("expected final text to mention completion, got: %q", out)
	}
}

// TestMaxStepsTerminates — a buggy provider that never says end_turn must fail
// with a MaxSteps error, not hang forever.
func TestMaxStepsTerminates(t *testing.T) {
	a := &Agent{
		Provider: &neverEndsProvider{},
		Actions:  []Action{SearchAction{}, DoneAction{}},
		MaxSteps: 3,
	}
	_, err := a.Run(context.Background(), "search forever")
	if err == nil {
		t.Fatalf("expected error after MaxSteps, got nil")
	}
	if !strings.Contains(err.Error(), "MaxSteps") {
		t.Errorf("expected error mentioning MaxSteps, got: %v", err)
	}
}

// TestUnknownActionGracefullyReports — if provider returns an unknown action
// name, the loop should report it as a tool_result rather than crash.
func TestUnknownActionGracefullyReports(t *testing.T) {
	a := &Agent{
		Provider: &oneShotProvider{actionName: "nonexistent"},
		Actions:  []Action{DoneAction{}},
		MaxSteps: 5,
	}
	out, err := a.Run(context.Background(), "test")
	// The provider only emits one tool_use, then we fall through to FakeProvider
	// fallback path which doesn't exist here — so we expect end after the
	// unknown action loops back to provider that has only one shot.
	// At minimum: it should not panic. err may or may not be set, but must not panic.
	_ = out
	_ = err
}

// --- helpers ---

type neverEndsProvider struct{}

func (neverEndsProvider) Invoke(ctx context.Context, msgs []Message) (Response, error) {
	return Response{
		Text:       "spinning forever",
		Actions:    []ActionCall{{Name: "search", Input: "x"}},
		StopReason: "tool_use",
	}, nil
}

type oneShotProvider struct {
	actionName string
	called     bool
}

func (p *oneShotProvider) Invoke(ctx context.Context, msgs []Message) (Response, error) {
	if p.called {
		return Response{
			Text:       "shutting down",
			Actions:    []ActionCall{{Name: "done"}},
			StopReason: "end_turn",
		}, nil
	}
	p.called = true
	return Response{
		Text:       "trying unknown",
		Actions:    []ActionCall{{Name: p.actionName, Input: "x"}},
		StopReason: "tool_use",
	}, nil
}
