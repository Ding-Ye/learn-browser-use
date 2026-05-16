package main

import (
	"strings"
	"testing"
)

// makeManager builds a MessageManager pre-loaded with `total` messages,
// the first being a stable "user task" message and the rest synthetic
// assistant turns with a tool_use named `tool`.
func makeManager(total, maxMessages int, strat Strategy) *MessageManager {
	mm := NewMessageManager(maxMessages, 0, strat, nil)
	mm.Add(Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: "do the thing"}},
	})
	for i := 1; i < total; i++ {
		name := []string{"search", "click", "type"}[i%3]
		mm.Add(Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "text", Text: "step text"},
				{Type: "tool_use", Name: name, Input: "{}"},
			},
		})
	}
	return mm
}

// TestKeepsLastN verifies the KeepLastN strategy:
//   - Get() returns at most MaxMessages
//   - History[0] is preserved (the user task)
//   - The dropped messages are the OLDEST middle ones
//   - History (raw) is untouched
func TestKeepsLastN(t *testing.T) {
	mm := makeManager(10, 4, KeepLastN{})
	got := mm.Get()

	if len(got) != 4 {
		t.Fatalf("Get() len = %d, want 4", len(got))
	}
	if got[0].Role != "user" || got[0].Content[0].Text != "do the thing" {
		t.Errorf("user task at slot 0 not preserved, got %+v", got[0])
	}
	// Last message of Get() must equal last message of raw history.
	if got[len(got)-1].Content[1].Name != mm.History[len(mm.History)-1].Content[1].Name {
		t.Errorf("tail not preserved: Get tail = %q, raw tail = %q",
			got[len(got)-1].Content[1].Name,
			mm.History[len(mm.History)-1].Content[1].Name)
	}
	// Raw history must NOT shrink — compaction is a view, not a mutation.
	if mm.Len() != 10 {
		t.Errorf("raw history mutated: Len = %d, want 10", mm.Len())
	}
}

// TestSummarizeReplacesOldTurns verifies the Summarize strategy:
//   - Get() returns at most MaxMessages
//   - Slot 0 = user task (verbatim)
//   - Slot 1 = a synthetic system message whose text starts with "[compacted:"
//   - Trailing messages = most-recent raw turns
func TestSummarizeReplacesOldTurns(t *testing.T) {
	mm := makeManager(20, 5, Summarize{})
	got := mm.Get()

	if len(got) != 5 {
		t.Fatalf("Get() len = %d, want 5", len(got))
	}
	if got[0].Role != "user" || got[0].Content[0].Text != "do the thing" {
		t.Errorf("slot 0 should be user task, got %+v", got[0])
	}
	if got[1].Role != "system" {
		t.Errorf("slot 1 should be the synthetic system summary, got role %q", got[1].Role)
	}
	if len(got[1].Content) == 0 || !strings.HasPrefix(got[1].Content[0].Text, "[compacted:") {
		t.Errorf("slot 1 text should start with [compacted:, got %q",
			func() string {
				if len(got[1].Content) > 0 {
					return got[1].Content[0].Text
				}
				return ""
			}())
	}
	// Summary must mention at least one tool name.
	if !strings.Contains(got[1].Content[0].Text, "search") &&
		!strings.Contains(got[1].Content[0].Text, "click") &&
		!strings.Contains(got[1].Content[0].Text, "type") {
		t.Errorf("summary should mention tool names, got %q", got[1].Content[0].Text)
	}
	// Raw history still has all 20.
	if mm.Len() != 20 {
		t.Errorf("raw history mutated: Len = %d, want 20", mm.Len())
	}
}

// TestRedactionMasksAPIKeys verifies the sk-* pattern in RedactSensitive.
// We test both standalone usage and via MessageManager.Add().
func TestRedactionMasksAPIKeys(t *testing.T) {
	raw := "the config has api_key=sk-abc123DEFghi456JKLmnoPQRstu and works"
	got := RedactSensitive(raw)

	if strings.Contains(got, "sk-abc123") {
		t.Errorf("API key not masked, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker, got %q", got)
	}
	// Idempotence — running twice yields the same result.
	if RedactSensitive(got) != got {
		t.Errorf("RedactSensitive is not idempotent")
	}

	// Via MessageManager.Add: the sanitiser should fire on Add and
	// the History should never carry the raw key.
	mm := NewMessageManager(0, 0, nil, RedactSensitive)
	mm.Add(Message{
		Role:    "tool",
		Content: []ContentBlock{{Type: "tool_result", Result: raw}},
	})
	storedResult := mm.History[0].Content[0].Result
	if strings.Contains(storedResult, "sk-abc123") {
		t.Errorf("manager stored unredacted key: %q", storedResult)
	}
	if !strings.Contains(storedResult, "[REDACTED]") {
		t.Errorf("manager output missing [REDACTED]: %q", storedResult)
	}
}

// TestRedactionMasksEmails verifies the email pattern. Three cases:
//   - bare email
//   - email surrounded by text
//   - multiple emails on one line
func TestRedactionMasksEmails(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alice@example.com", "[REDACTED]"},
		{"contact alice@example.com please", "contact [REDACTED] please"},
		{"both a@b.io and c.d+x@long-domain.co.uk", "both [REDACTED] and [REDACTED]"},
	}
	for _, c := range cases {
		got := RedactSensitive(c.in)
		if got != c.want {
			t.Errorf("RedactSensitive(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestImageContentPreserved guards a regression: redaction must not
// corrupt a tool_result block whose Result happens to contain bytes
// that look base64-ish (which is what an embedded image arrives as
// from a real provider). We model this by stuffing a long opaque
// base64-shaped string into the Result and asserting it is preserved
// verbatim (no character mangling) AND the message survives compaction.
func TestImageContentPreserved(t *testing.T) {
	// A made-up PNG header followed by random base64-ish bytes; no
	// embedded patterns that our regex would catch.
	imageBlob := "iVBORw0KGgoAAAANSUhEUgAAA" + strings.Repeat("XyZ012", 30)

	mm := NewMessageManager(3, 0, KeepLastN{}, RedactSensitive)
	// 4 messages so KeepLastN actually drops something.
	mm.Add(Message{Role: "user",
		Content: []ContentBlock{{Type: "text", Text: "task"}}})
	mm.Add(Message{Role: "assistant",
		Content: []ContentBlock{{Type: "text", Text: "ok"}}})
	mm.Add(Message{Role: "tool",
		Content: []ContentBlock{{Type: "tool_result", Result: imageBlob}}})
	mm.Add(Message{Role: "assistant",
		Content: []ContentBlock{{Type: "text", Text: "done"}}})

	// 1. The raw blob round-trips through Add unchanged. The redaction
	//    regexes must not match this base64-ish stream.
	stored := mm.History[2].Content[0].Result
	if stored != imageBlob {
		t.Errorf("image blob mutated by sanitiser:\n  got = %q\n  want = %q", stored, imageBlob)
	}

	// 2. After Get() with cap=3, slot 0 is the user task, and the
	//    image-bearing message survives if it's in the tail window.
	view := mm.Get()
	if len(view) != 3 {
		t.Fatalf("Get() len = %d, want 3", len(view))
	}
	// The last two views must contain the tool-result blob and the
	// final assistant message verbatim — KeepLastN takes the tail.
	var found bool
	for _, m := range view {
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.Result == imageBlob {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("image content not present in compacted view")
	}
}
