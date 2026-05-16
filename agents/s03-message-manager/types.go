package main

// Message is one conversational turn between the agent and the LLM.
// Copied verbatim from s01 / s02 (self-contained — we don't import across
// sessions). The shape mirrors what Anthropic / OpenAI Chat Completions
// accept and what `browser_use/llm/messages.py` defines.
type Message struct {
	Role    string         // "system" | "user" | "assistant" | "tool"
	Content []ContentBlock // a turn can have multiple blocks
}

// ContentBlock is a tagged union over Type. Only the fields relevant to
// the type are non-zero. In s03 we also use a synthesised block
// (Type == "text" / Role == "system") to carry the compaction summary.
type ContentBlock struct {
	Type   string // "text" | "tool_use" | "tool_result"
	Text   string // when Type == "text"
	Name   string // when Type == "tool_use" — action name
	Input  string // when Type == "tool_use" — JSON-encoded args
	Result string // when Type == "tool_result"
}
