package main

import (
	"context"
	"fmt"
	"io"
)

// Provider abstracts "the brain". s01 uses FakeProvider; s02 replaces it
// with a real LLM (OpenAI HTTP). Note that Provider doesn't care which
// language model is behind it — it just maps a list of messages to a
// Response with optional Actions.
type Provider interface {
	Invoke(ctx context.Context, msgs []Message) (Response, error)
}

// Agent is the smallest browser-agent loop that compiles and runs.
// It has no browser, no real LLM, no DOM. The point is to make the
// observe → think → act cycle visible in ~60 lines of code.
//
// Fields:
//   Provider — picks the next action (LLM stand-in)
//   Actions  — what the agent can do (search, navigate, done)
//   MaxSteps — safety cap so a bad provider doesn't loop forever
//   Verbose  — if non-nil, write a one-line trace per step
type Agent struct {
	Provider Provider
	Actions  []Action
	MaxSteps int
	Verbose  io.Writer
}

// Run executes the loop. It returns the final assistant text and any error.
//
// The loop is structured around StopReason so all later sessions can extend
// it: tool_use → run actions → observe → repeat; end_turn → exit; max_tokens
// → error.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	if a.MaxSteps <= 0 {
		a.MaxSteps = 10
	}

	// Build action lookup. (In s04 this becomes a Registry with reflection.)
	byName := map[string]Action{}
	for _, act := range a.Actions {
		byName[act.Name()] = act
	}

	// Seed the conversation with the user task.
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: task}}},
	}

	for step := 0; step < a.MaxSteps; step++ {
		resp, err := a.Provider.Invoke(ctx, msgs)
		if err != nil {
			return "", fmt.Errorf("step %d invoke: %w", step, err)
		}

		// Append the assistant turn (text + planned actions).
		assistantContent := []ContentBlock{{Type: "text", Text: resp.Text}}
		for _, act := range resp.Actions {
			assistantContent = append(assistantContent, ContentBlock{
				Type:  "tool_use",
				Name:  act.Name,
				Input: act.Input,
			})
		}
		msgs = append(msgs, Message{Role: "assistant", Content: assistantContent})

		if a.Verbose != nil {
			fmt.Fprintf(a.Verbose, "[step %d] assistant: %s\n", step, resp.Text)
		}

		switch resp.StopReason {
		case "end_turn":
			return resp.Text, nil

		case "tool_use":
			// Run each action and feed results back as a tool message.
			var results []ContentBlock
			for _, ac := range resp.Actions {
				tool, ok := byName[ac.Name]
				if !ok {
					results = append(results, ContentBlock{
						Type:   "tool_result",
						Result: fmt.Sprintf("unknown action %q", ac.Name),
					})
					if a.Verbose != nil {
						fmt.Fprintf(a.Verbose, "[step %d] ! unknown action %s\n", step, ac.Name)
					}
					continue
				}
				out, err := tool.Run(ctx, ac.Input)
				if err != nil {
					out = fmt.Sprintf("tool error: %v", err)
				}
				if a.Verbose != nil {
					fmt.Fprintf(a.Verbose, "[step %d] %s -> %s\n", step, ac.Name, truncate(out, 80))
				}
				results = append(results, ContentBlock{
					Type:   "tool_result",
					Result: out,
				})
			}
			msgs = append(msgs, Message{Role: "tool", Content: results})

		case "max_tokens":
			return "", fmt.Errorf("step %d: provider returned max_tokens (response truncated)", step)

		default:
			return "", fmt.Errorf("step %d: unknown stop_reason %q", step, resp.StopReason)
		}
	}
	return "", fmt.Errorf("MaxSteps=%d exceeded without end_turn (likely the provider is stuck)", a.MaxSteps)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
