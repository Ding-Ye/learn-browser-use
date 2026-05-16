package main

import "fmt"

// MessageManager owns the agent's conversation history and the policies
// that keep it bounded. The s01/s02 agents kept history as a bare
// []Message inside Agent.Run; here we promote that into its own type so
// that a compaction Strategy and sensitive-data redaction can be layered
// on without bloating the loop body.
//
// Two layered policies, applied in different places:
//
//  1. Compaction (Strategy) — applied lazily inside Get() so callers
//     always see a bounded history. We never mutate History on Add(),
//     because we want the raw transcript for debugging and tests.
//
//  2. Redaction (Sanitizer) — applied eagerly inside Add() because
//     leaking an API key into in-memory history is already a hazard
//     even if we strip it before sending to the LLM.
//
// Upstream parallel: `browser_use/agent/message_manager/service.py#L104`
// — same role, plus screenshots, file system, sensitive-data domain
// scoping, and a real LLM-driven summariser.
type MessageManager struct {
	History     []Message
	MaxMessages int  // soft cap; Get() applies Strategy when exceeded
	TokenBudget int  // hint only; we don't tokenise here

	// Strategy decides what Get() returns when History exceeds MaxMessages.
	// nil → no compaction (keep everything verbatim).
	Strategy Strategy

	// Sanitizer runs on every block.Text / block.Result inside Add().
	// nil → no redaction.
	Sanitizer func(string) string
}

// Strategy is a pure transform: given a history slice and a cap, return
// the slice the LLM should actually see. It MUST NOT mutate the input.
//
// Two concrete strategies live in compaction.go: KeepLastN and Summarize.
type Strategy interface {
	Apply(history []Message, maxMessages int) []Message
}

// NewMessageManager returns a manager with sensible defaults. The zero
// value of MessageManager also works — this constructor is just for
// callers who want to set everything at once.
func NewMessageManager(maxMessages, tokenBudget int, strat Strategy, sanitiser func(string) string) *MessageManager {
	return &MessageManager{
		MaxMessages: maxMessages,
		TokenBudget: tokenBudget,
		Strategy:    strat,
		Sanitizer:   sanitiser,
	}
}

// Add appends a message to the history. The sanitiser runs eagerly on
// every text-bearing block so that even an in-process inspection of
// History (e.g. via a debugger dump) cannot leak secrets.
//
// We deliberately copy the slice before mutating it — callers may keep
// references to the Message they passed in and we want them to remain
// pristine.
func (m *MessageManager) Add(msg Message) {
	if m.Sanitizer != nil && len(msg.Content) > 0 {
		clone := make([]ContentBlock, len(msg.Content))
		copy(clone, msg.Content)
		for i := range clone {
			clone[i].Text = m.Sanitizer(clone[i].Text)
			clone[i].Result = m.Sanitizer(clone[i].Result)
			clone[i].Input = m.Sanitizer(clone[i].Input)
		}
		msg.Content = clone
	}
	m.History = append(m.History, msg)
}

// Get returns the messages that should be sent to the LLM for the next
// turn. Compaction is applied here (not in Add) because:
//
//   - We want History (raw) and Get() (effective) to be different views.
//   - Compaction may need to call an LLM (in upstream); doing it lazily
//     means we only pay the cost when a turn is actually about to fire.
//   - Tests can inspect both views.
func (m *MessageManager) Get() []Message {
	if m.Strategy == nil || m.MaxMessages <= 0 || len(m.History) <= m.MaxMessages {
		// Nothing to compact — return a defensive copy so callers can't
		// mutate our backing array.
		out := make([]Message, len(m.History))
		copy(out, m.History)
		return out
	}
	return m.Strategy.Apply(m.History, m.MaxMessages)
}

// Len returns the *raw* history length, NOT the post-compaction length.
// Tests and logging want the truth; the agent loop wants Get().
func (m *MessageManager) Len() int {
	return len(m.History)
}

// String renders a debug summary. Keep this short — it's printed by main.go
// before/after compaction so the reader can see the policy bite.
func (m *MessageManager) String() string {
	return fmt.Sprintf("MessageManager{raw=%d, max=%d, budget=%d}", len(m.History), m.MaxMessages, m.TokenBudget)
}
