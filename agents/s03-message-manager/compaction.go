package main

import (
	"fmt"
	"strings"
)

// KeepLastN is the simplest compaction strategy: keep the very first
// message (which by convention carries the user's task / system prompt)
// and drop the oldest middle messages until we are at the cap. This is
// equivalent to upstream's `keep_last_items` when compaction is disabled
// but the soft limit kicks in.
//
// We always keep History[0] because it usually pins the task verbatim;
// dropping it would let the agent forget *what it was trying to do*.
type KeepLastN struct{}

// Apply returns a slice whose length is min(len(history), maxMessages).
// We try to preserve [first] + [most-recent (max-1)].
func (KeepLastN) Apply(history []Message, maxMessages int) []Message {
	if len(history) <= maxMessages || maxMessages <= 0 {
		out := make([]Message, len(history))
		copy(out, history)
		return out
	}

	// Reserve slot 0 for the original task, then take the tail.
	keep := maxMessages - 1
	if keep < 0 {
		keep = 0
	}
	out := make([]Message, 0, maxMessages)
	out = append(out, history[0])
	if keep > 0 {
		out = append(out, history[len(history)-keep:]...)
	}
	return out
}

// Summarize is the more interesting strategy: when history exceeds the
// cap, replace the dropped middle messages with one synthetic system
// message that summarises them. This mirrors the spirit of upstream's
// `maybe_compact_messages` (service.py#L213) but with a deterministic
// pseudo-summary instead of a real LLM call — perfect for teaching and
// for byte-stable tests.
//
// Layout after compaction:
//
//	[ History[0] ]                              -- pinned task
//	[ Message{Role:"system", "[compacted: ...]" } ] -- synthetic summary
//	[ History[-(max-2):] ]                      -- recent verbatim turns
//
// We keep History[0] verbatim because it's the user task: paraphrasing
// it would let the agent silently drift away from the original ask.
// Upstream takes the same stance (service.py#L289-L295).
type Summarize struct{}

func (Summarize) Apply(history []Message, maxMessages int) []Message {
	if len(history) <= maxMessages || maxMessages <= 0 {
		out := make([]Message, len(history))
		copy(out, history)
		return out
	}

	// We reserve 2 slots in the cap: 1 for the pinned task and 1 for
	// the synthetic summary message. The rest of the cap is filled
	// from the tail of the history.
	tailKeep := maxMessages - 2
	if tailKeep < 0 {
		tailKeep = 0
	}

	// The messages that will be folded into the summary:
	// everything between History[0] and the tail we want to keep.
	tailStart := len(history) - tailKeep
	if tailStart < 1 {
		tailStart = 1
	}
	dropped := history[1:tailStart]

	summary := summariseTurns(dropped)

	out := make([]Message, 0, maxMessages)
	out = append(out, history[0])
	out = append(out, Message{
		Role:    "system",
		Content: []ContentBlock{{Type: "text", Text: summary}},
	})
	if tailKeep > 0 {
		out = append(out, history[tailStart:]...)
	}
	return out
}

// summariseTurns produces a one-line pseudo-summary. Real upstream calls
// an LLM here. We use a histogram of tool names + a turn count so the
// output is deterministic and the tests can pin the exact string.
//
// Format mirrors the spec's example:
//
//	[compacted: 12 turns covering search, click, type actions]
func summariseTurns(dropped []Message) string {
	if len(dropped) == 0 {
		return "[compacted: 0 turns]"
	}

	// Collect tool names, preserving first-seen order so the output
	// is deterministic. (Go map iteration order is randomised, so a
	// naive `for k := range counts` would defeat test reproducibility.)
	type entry struct {
		name string
		hits int
	}
	var order []entry
	idx := map[string]int{}

	for _, msg := range dropped {
		for _, b := range msg.Content {
			if b.Type != "tool_use" || b.Name == "" {
				continue
			}
			if pos, ok := idx[b.Name]; ok {
				order[pos].hits++
			} else {
				idx[b.Name] = len(order)
				order = append(order, entry{name: b.Name, hits: 1})
			}
		}
	}

	if len(order) == 0 {
		return fmt.Sprintf("[compacted: %d turns of dialogue]", len(dropped))
	}

	names := make([]string, len(order))
	for i, e := range order {
		names[i] = e.name
	}
	return fmt.Sprintf("[compacted: %d turns covering %s actions]",
		len(dropped), strings.Join(names, ", "))
}
