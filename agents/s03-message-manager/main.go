package main

import (
	"fmt"
	"strings"
)

// main is a tiny CLI demo. We:
//   1. Build a MessageManager with MaxMessages=5 and the Summarize strategy.
//   2. Add 20 fake messages that mix tool calls, API keys, and emails.
//   3. Print the before/after stats and a couple of sample messages so
//      the reader can see compaction + redaction biting.
//
// There are no flags — this is the deterministic "tour the moving parts"
// runner, not a real tool. For exploring the policies, use the tests.
func main() {
	mm := NewMessageManager(5, 4000, Summarize{}, RedactSensitive)

	// Pin the original task (History[0]) — both KeepLastN and Summarize
	// guarantee this survives compaction.
	mm.Add(Message{
		Role: "user",
		Content: []ContentBlock{{Type: "text",
			Text: "Find the trending repos on hacker news and email summary to alice@example.com"}},
	})

	// Add 19 more messages so total = 20. Alternate assistant /
	// tool turns; every fourth result simulates a leak so we can
	// demonstrate redaction.
	tools := []string{"search", "click", "type"}
	for i := 1; i <= 19; i++ {
		toolName := tools[(i-1)%len(tools)]
		if i%2 == 1 {
			// odd → assistant turn with text + tool_use
			mm.Add(Message{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: fmt.Sprintf("Step %d: invoking %s.", i, toolName)},
					{Type: "tool_use", Name: toolName, Input: fmt.Sprintf("{\"step\": %d}", i)},
				},
			})
		} else {
			// even → tool result, sometimes carrying a "leak"
			result := fmt.Sprintf("step %d result ok", i)
			switch i % 6 {
			case 0:
				result = "leaked authorization: Bearer abcDEF0123_secret_token_xyz"
			case 2:
				result = "log line emailed to alice@example.com"
			case 4:
				result = "config snapshot api_key=sk-prod-AbCdEfGhIjKlMnOpQrSt rest=ok"
			}
			mm.Add(Message{
				Role:    "tool",
				Content: []ContentBlock{{Type: "tool_result", Result: result}},
			})
		}
	}

	fmt.Println("=== before compaction ===")
	fmt.Println(mm)
	fmt.Printf("raw history length: %d\n", mm.Len())

	view := mm.Get()
	fmt.Println()
	fmt.Println("=== after Get() (Summarize strategy, max=5) ===")
	fmt.Printf("effective length:   %d\n", len(view))
	for i, msg := range view {
		fmt.Printf("[%d] %-9s %s\n", i, msg.Role, summarise(msg))
	}

	fmt.Println()
	fmt.Println("=== redaction in action ===")
	// Walk raw history backwards and print the first redacted text
	// (post-sanitisation) and the first non-redacted, non-empty text
	// — so the reader sees both sides of the policy.
	var firstRedacted, firstClean string
	for i := len(mm.History) - 1; i >= 0; i-- {
		for _, b := range mm.History[i].Content {
			payload := b.Result
			if payload == "" {
				payload = b.Text
			}
			if payload == "" {
				continue
			}
			if firstRedacted == "" && strings.Contains(payload, "REDACTED") {
				firstRedacted = payload
			}
			if firstClean == "" && !strings.Contains(payload, "REDACTED") {
				firstClean = payload
			}
		}
	}
	fmt.Printf("redacted sample: %q\n", firstRedacted)
	fmt.Printf("clean sample:    %q\n", firstClean)
}

// summarise is a tiny helper purely for the demo's printed table.
func summarise(msg Message) string {
	if len(msg.Content) == 0 {
		return "(empty)"
	}
	b := msg.Content[0]
	text := b.Text
	if text == "" {
		text = b.Result
	}
	if text == "" && b.Type == "tool_use" {
		text = fmt.Sprintf("tool_use:%s", b.Name)
	}
	if len(text) > 72 {
		text = text[:69] + "..."
	}
	return text
}
