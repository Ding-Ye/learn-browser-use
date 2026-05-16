package main

import "fmt"

// message_manager.go is a deliberately tiny re-declaration of s03's
// MessageManager — just KeepLastN compaction, no Sanitizer, no
// Summarize. The s12 chapter is about integration, not about adding
// new compaction strategies; we ship the simplest policy that still
// proves the agent loop calls Get() (not History) before every LLM
// turn.
//
// Upstream analog: `MessageManager` in
// browser_use/agent/message_manager/service.py — ~1,500 LOC covering
// screenshot embedding, page-specific filtering, sensitive-data
// masking, summarization-via-LLM. Our 70 lines preserve the
// History/Get split, which is the load-bearing piece.

// MessageManager owns the agent's conversation history and the cap
// policy that keeps it bounded. The Agent.Run loop adds messages via
// Add, and reads the effective slice via Get — keeping the raw
// History readable for tests + diagnostics.
type MessageManager struct {
	History     []Message
	MaxMessages int

	// KeepLastN is the only compaction strategy we ship. When
	// len(History) > MaxMessages, Get returns
	// [History[0]] ++ History[-(MaxMessages-1):]. The first message
	// (the system prompt or pinned task) is always preserved.
	//
	// When false, Get returns History verbatim regardless of length —
	// useful for tests that want to inspect the unbounded transcript.
	KeepLastN bool
}

// NewMessageManager returns a manager configured for KeepLastN
// compaction at the given cap. The Agent will Add the system prompt
// as History[0]; from then on every turn appends a user / assistant /
// tool message and Get() respects the cap.
func NewMessageManager(maxMessages int) *MessageManager {
	return &MessageManager{
		MaxMessages: maxMessages,
		KeepLastN:   true,
	}
}

// Add appends a message to History. We do NOT compact here — the agent
// loop wants the raw transcript for printf-style debug + the cost
// ledger reconciliation. Compaction happens lazily in Get.
func (m *MessageManager) Add(msg Message) {
	m.History = append(m.History, msg)
}

// Get returns the slice the Provider should see for the next turn.
// When KeepLastN is on and we're over the cap, [first] + [last
// MaxMessages-1] is returned. The slice is a fresh copy so callers
// can mutate it without affecting History.
func (m *MessageManager) Get() []Message {
	if !m.KeepLastN || m.MaxMessages <= 0 || len(m.History) <= m.MaxMessages {
		out := make([]Message, len(m.History))
		copy(out, m.History)
		return out
	}

	// Over cap: keep History[0] + tail.
	keep := m.MaxMessages - 1
	if keep < 0 {
		keep = 0
	}
	out := make([]Message, 0, m.MaxMessages)
	out = append(out, m.History[0])
	if keep > 0 {
		out = append(out, m.History[len(m.History)-keep:]...)
	}
	return out
}

// Len returns the raw history length, NOT the post-compaction length.
// Tests want the truth; the loop wants Get().
func (m *MessageManager) Len() int {
	return len(m.History)
}

// String renders a debug summary used by main.go for printing the
// before/after counts as the loop progresses.
func (m *MessageManager) String() string {
	return fmt.Sprintf("MessageManager{raw=%d, max=%d}", len(m.History), m.MaxMessages)
}
