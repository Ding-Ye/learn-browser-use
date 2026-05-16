package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// agent.go is the heart of s12: the integrated Agent struct that
// composes every previous chapter's contribution into one runnable
// loop. There is one file's worth of code here that you should read
// closely — Run() — because everything else in this module is
// scaffolding for it.
//
// What this Agent does, end-to-end, per step:
//
//   1. Optionally invoke the planner (every PlanEvery steps after step 0).
//   2. Take a DOM snapshot from DOMService.
//   3. Build a user message: "[DOM snapshot] " + serialized text.
//   4. Call Provider.Invoke with the message history + tool schemas.
//      - If primary times out (LLMTimeout) and Fallback is set, call
//        Fallback.Invoke against the SAME history.
//   5. Register cost on TokenCost.
//   6. Append the assistant message (text + tool_use blocks).
//   7. If StopReason == "end_turn", return the assistant's text.
//   8. Otherwise, dispatch each ActionCall through the Registry's
//      Dispatcher, append tool_result blocks.
//   9. If any tool_result starts with DoneResultPrefix, return the
//      suffix as the final answer.
//  10. Loop until MaxSteps exhausted.
//
// The integration is intentionally explicit; reading this file in
// order should be enough to see where each previous session
// contributes:
//
//   - Provider           → s02
//   - MessageManager     → s03
//   - Registry           → s04
//   - BrowserSession     → s07
//   - DOMService         → s09
//   - TokenCost          → s10
//   - FileSystem         → s11
//   - Planner+Fallback   → new in s12

//go:embed system_prompt.txt
var systemPromptText string

// Default loop tuning knobs. The agent struct lets a caller override
// any of these explicitly.
const (
	DefaultMaxSteps    = 12
	DefaultPlanEvery   = 5
	DefaultLLMTimeout  = 30 * time.Second
)

// Agent is the integrated browser-using agent. Fields are public so
// the demo and tests can wire things explicitly; in production code
// you'd probably hide most of this behind a NewAgent constructor.
type Agent struct {
	// Provider is the primary LLM. Required.
	Provider Provider

	// Fallback is invoked if the primary times out (LLMTimeout). When
	// nil, a primary timeout is fatal — the loop returns the error.
	Fallback Provider

	// Tools is the dispatcher for the registered tools. Required.
	Tools *Registry

	// Session owns the (stub) CDP client + EventBus + watchdogs.
	// Required, must be Started before Run().
	Session *BrowserSession

	// DOM provides per-turn page snapshots.
	DOM *DOMService

	// Messages is the conversation history. Required.
	Messages *MessageManager

	// Cost tracks per-invocation tokens + $.
	Cost *TokenCost

	// FS is the sandbox filesystem (currently unused by built-in
	// tools — kept on Agent so a custom tool that does fs.ReadFile
	// can access it without an additional plumbing wire).
	FS FileSystem

	// MaxSteps caps the loop. Zero ⇒ DefaultMaxSteps.
	MaxSteps int

	// PlanEvery: every N steps (starting at step PlanEvery, then
	// 2*PlanEvery, ...) the planner is invoked. Zero ⇒ no planning.
	PlanEvery int

	// LLMTimeout is the per-Provider.Invoke deadline. If the primary
	// returns ctx.DeadlineExceeded, the fallback (if any) gets a turn.
	// Zero ⇒ DefaultLLMTimeout.
	LLMTimeout time.Duration

	// Verbose, when non-nil, receives one or more lines per step
	// describing the loop's progress. Used by main.go for the demo;
	// tests use a *bytes.Buffer for assertion.
	Verbose io.Writer
}

// Run is the integration loop. It returns (final answer, error). The
// final answer comes from either:
//
//   - The most recent assistant Response.Text when StopReason == "end_turn".
//   - The suffix of a DoneTool tool_result when StopReason == "tool_use"
//     and the LLM called done().
//
// MaxSteps exhaustion returns an error. A primary-timeout-and-no-fallback
// returns the timeout error.
//
// The body intentionally fits on one screen. Each phase is a tiny
// helper (planner call, observe, invoke-with-fallback, dispatch). The
// glue here is the StopReason switch + the done()-detector.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	a.applyDefaults()
	if err := a.validate(); err != nil {
		return "", err
	}

	// Seed history: system prompt + user task. We add the system
	// prompt to History[0] so KeepLastN compaction always preserves
	// it across the entire run.
	a.Messages.Add(Message{
		Role:    "system",
		Content: []ContentBlock{{Type: "text", Text: systemPromptText}},
	})
	a.Messages.Add(Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: "Task: " + task}},
	})

	for step := 0; step < a.MaxSteps; step++ {
		// Phase 1: maybe call the planner. Plan at step=PlanEvery,
		// 2*PlanEvery, etc. — never at step 0 because we don't have
		// any history to reflect on yet.
		if a.PlanEvery > 0 && step > 0 && step%a.PlanEvery == 0 {
			plan, err := Plan(ctx, a.Provider, a.Messages.Get())
			if err == nil {
				a.Messages.Add(Message{
					Role: "system",
					Content: []ContentBlock{{
						Type: "text",
						Text: "[plan @ step " + itoa(step) + "] " + plan,
					}},
				})
				a.logf("[step %d] planner: %s\n", step, truncate(plan, 80))
			} else {
				a.logf("[step %d] planner failed (non-fatal): %v\n", step, err)
			}
		}

		// Phase 2: observe — take a DOM snapshot and append it as a
		// user message. The DOM text already comes with [N]-indexed
		// elements so the LLM can refer to them by index in its
		// tool_use blocks.
		dom, err := a.DOM.Get(ctx)
		if err != nil {
			return "", fmt.Errorf("step %d: dom: %w", step, err)
		}
		a.Messages.Add(Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "[browser_state]\nURL: " + a.DOM.CurrentURL() + "\n" + dom.LLMText,
			}},
		})

		// Phase 3: invoke (with fallback).
		resp, err := a.invokeWithFallback(ctx, step)
		if err != nil {
			return "", err
		}

		// Phase 4: ledger.
		a.Cost.RegisterInvocation(resp.Model, resp.InputTokens, resp.OutputTokens)

		// Phase 5: append assistant message.
		assistantBlocks := []ContentBlock{{Type: "text", Text: resp.Text}}
		for _, ac := range resp.Actions {
			assistantBlocks = append(assistantBlocks, ContentBlock{
				Type:  "tool_use",
				Name:  ac.Name,
				Input: ac.Input,
			})
		}
		a.Messages.Add(Message{Role: "assistant", Content: assistantBlocks})
		a.logf("[step %d] assistant: %s (stop=%s)\n", step, truncate(resp.Text, 60), resp.StopReason)

		// Phase 6: stop?
		switch resp.StopReason {
		case "end_turn":
			return resp.Text, nil
		case "max_tokens":
			return "", fmt.Errorf("step %d: provider truncated response", step)
		case "tool_use":
			// fall through to dispatch
		default:
			return "", fmt.Errorf("step %d: unknown stop_reason %q", step, resp.StopReason)
		}

		// Phase 7: dispatch tools.
		disp := &Dispatcher{Registry: a.Tools, Timeout: a.LLMTimeout}
		var toolResults []ContentBlock
		var finalAnswer string
		for _, ac := range resp.Actions {
			block, _ := disp.Act(ctx, ac)
			toolResults = append(toolResults, block)
			a.logf("[step %d]   %s → %s\n", step, ac.Name, truncate(block.Result, 60))

			if strings.HasPrefix(block.Result, DoneResultPrefix) {
				finalAnswer = strings.TrimPrefix(block.Result, DoneResultPrefix)
			}
		}
		a.Messages.Add(Message{Role: "tool", Content: toolResults})

		// Phase 8: if done() was called, return its answer.
		if finalAnswer != "" {
			return finalAnswer, nil
		}
	}
	return "", fmt.Errorf("MaxSteps=%d exceeded without end_turn or done()", a.MaxSteps)
}

// invokeWithFallback applies the LLMTimeout deadline and, on timeout,
// reissues against Fallback. The fallback uses a FRESH context (still
// derived from the outer ctx) so a primary's wedge doesn't leak into
// the fallback budget.
//
// Why is fallback "fresh context, not retry-in-place"? Because if the
// primary stalled holding internal state, retrying with the SAME ctx
// gives the fallback no time to succeed. Issuing a new
// context.WithTimeout(parent, LLMTimeout) gives the fallback a clean
// budget. This is one of the non-obvious points the docs call out.
func (a *Agent) invokeWithFallback(ctx context.Context, step int) (Response, error) {
	tools := a.Tools.Schemas()
	msgs := a.Messages.Get()

	primaryCtx, cancel := context.WithTimeout(ctx, a.LLMTimeout)
	resp, err := a.Provider.Invoke(primaryCtx, msgs, tools)
	cancel()

	if err == nil {
		return resp, nil
	}

	// Did the primary timeout? If we have a fallback, give it a turn.
	if a.Fallback == nil {
		return Response{}, fmt.Errorf("step %d: primary invoke: %w", step, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(primaryCtx.Err(), context.DeadlineExceeded) {
		return Response{}, fmt.Errorf("step %d: primary invoke (non-timeout, not falling back): %w", step, err)
	}

	a.logf("[step %d] primary timed out after %s, falling back\n", step, a.LLMTimeout)
	fbCtx, fbCancel := context.WithTimeout(ctx, a.LLMTimeout)
	defer fbCancel()
	resp, err = a.Fallback.Invoke(fbCtx, msgs, tools)
	if err != nil {
		return Response{}, fmt.Errorf("step %d: fallback also failed: %w", step, err)
	}
	return resp, nil
}

// applyDefaults fills zero-valued tuning knobs with their defaults so
// a caller who only sets Provider + Tools + Session still gets a
// working loop.
func (a *Agent) applyDefaults() {
	if a.MaxSteps <= 0 {
		a.MaxSteps = DefaultMaxSteps
	}
	if a.PlanEvery < 0 {
		a.PlanEvery = 0
	} else if a.PlanEvery == 0 {
		// PlanEvery == 0 means "disabled"; we keep it that way so
		// tests that don't want planning can leave the field zero.
	}
	if a.LLMTimeout <= 0 {
		a.LLMTimeout = DefaultLLMTimeout
	}
}

// validate returns an error if a required field is missing. Fail-fast
// so the loop doesn't dereference nil deep inside a step.
func (a *Agent) validate() error {
	if a.Provider == nil {
		return errors.New("agent: Provider is required")
	}
	if a.Tools == nil {
		return errors.New("agent: Tools is required")
	}
	if a.Session == nil {
		return errors.New("agent: Session is required")
	}
	if a.DOM == nil {
		return errors.New("agent: DOM is required")
	}
	if a.Messages == nil {
		return errors.New("agent: Messages is required")
	}
	if a.Cost == nil {
		return errors.New("agent: Cost is required")
	}
	return nil
}

func (a *Agent) logf(format string, args ...any) {
	if a.Verbose == nil {
		return
	}
	fmt.Fprintf(a.Verbose, format, args...)
}

// truncate is a tiny helper — keeps Verbose lines from blowing up
// when a tool result is a few KB of text. Same shape as s01's.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// itoa avoids dragging strconv in for one int→string conversion.
func itoa(n int) string { return fmt.Sprintf("%d", n) }
