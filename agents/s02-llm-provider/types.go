package main

// Message is one conversational turn between the agent and the LLM.
// Identical shape to s01: a turn has a role and a list of typed content
// blocks. We keep this in our own package so this session is self-contained
// (no import from sibling sessions).
type Message struct {
	Role    string         // "system" | "user" | "assistant" | "tool"
	Content []ContentBlock // text blocks, tool_use blocks, tool_result blocks
}

// ContentBlock is a tagged union over Type. Only the fields relevant to
// the type are non-zero. Mirrors what real LLM APIs accept.
type ContentBlock struct {
	Type  string // "text" | "tool_use" | "tool_result"
	Text  string // when Type == "text"
	Name  string // when Type == "tool_use" — the action name
	Input string // when Type == "tool_use" — JSON-encoded args
	// Result fields, when Type == "tool_result"
	Result string
}

// Response is what a Provider returns each turn. s02 extends the s01 shape
// with token accounting and the resolved model name — fields you cannot get
// from a FakeProvider but become useful once a real LLM is on the wire.
//
//	InputTokens  — prompt tokens consumed by the request
//	OutputTokens — completion tokens generated (includes reasoning tokens
//	               for o-series models per OpenAI docs)
//	Model        — server-resolved model id (e.g. "gpt-4o-mini-2024-07-18"
//	               when client requested "gpt-4o-mini")
type Response struct {
	Text         string
	Actions      []ActionCall
	StopReason   string // "end_turn" | "tool_use" | "length"
	InputTokens  int
	OutputTokens int
	Model        string
}

// ActionCall is one tool/action dispatch decided by the LLM. The Input
// field is a JSON string (we keep it stringly-typed so the Provider
// interface stays free of generics).
type ActionCall struct {
	Name  string
	Input string
}
