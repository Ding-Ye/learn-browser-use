package main

import (
	"context"
	"fmt"
	"strings"
)

// FakeProvider is a deterministic, zero-network "LLM" used to teach the loop
// shape without touching real model APIs. It scans the latest user/tool
// message for keywords and emits a hard-coded action.
//
// Rules (in priority order):
//  1. If the conversation already has a tool_result containing "RESULT:",
//     emit a final assistant text + end_turn.
//  2. If the user task contains "search ...", emit `search` with the query.
//  3. If the user task contains "navigate https://...", emit `navigate`.
//  4. Otherwise: emit `done` immediately (nothing to do).
//
// This is intentionally dumb. The whole point of s01 is to make the LOOP
// shape obvious: observe → think → act → observe. The "think" step's
// implementation is a stand-in for what s02 replaces with a real LLM call.
type FakeProvider struct {
	// Step counter so tests can verify we don't infinite-loop.
	calls int
}

func (p *FakeProvider) Invoke(ctx context.Context, msgs []Message) (Response, error) {
	p.calls++

	// Find the most recent tool_result (browser state observation).
	// If we have one and it carries "RESULT:" we declare success.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "tool" && m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_result" && strings.Contains(b.Result, "RESULT:") {
				return Response{
					Text:       "Task complete. " + extractResult(b.Result),
					Actions:    []ActionCall{{Name: "done", Input: ""}},
					StopReason: "end_turn",
				}, nil
			}
		}
	}

	// No tool_result yet: look at the user task and pick an action.
	task := latestUserText(msgs)

	switch {
	case strings.Contains(strings.ToLower(task), "search"):
		query := strings.TrimSpace(strings.SplitN(strings.ToLower(task), "search", 2)[1])
		if query == "" {
			query = "default-query"
		}
		return Response{
			Text:       fmt.Sprintf("I will search for %q.", query),
			Actions:    []ActionCall{{Name: "search", Input: query}},
			StopReason: "tool_use",
		}, nil
	case strings.Contains(strings.ToLower(task), "navigate"):
		idx := strings.Index(strings.ToLower(task), "navigate")
		url := strings.TrimSpace(task[idx+len("navigate"):])
		if url == "" {
			url = "https://example.com"
		}
		return Response{
			Text:       fmt.Sprintf("Navigating to %s.", url),
			Actions:    []ActionCall{{Name: "navigate", Input: url}},
			StopReason: "tool_use",
		}, nil
	default:
		// Nothing to do — close immediately.
		return Response{
			Text:       "I see no action to take. Marking task done.",
			Actions:    []ActionCall{{Name: "done", Input: ""}},
			StopReason: "end_turn",
		}, nil
	}
}

func latestUserText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		for _, b := range msgs[i].Content {
			if b.Type == "text" {
				return b.Text
			}
		}
	}
	return ""
}

func extractResult(s string) string {
	idx := strings.Index(s, "RESULT:")
	if idx < 0 {
		return s
	}
	return strings.TrimSpace(s[idx+len("RESULT:"):])
}
