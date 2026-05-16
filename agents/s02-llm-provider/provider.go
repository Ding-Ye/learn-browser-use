package main

import (
	"context"
	"encoding/json"
)

// Provider is the single seam between the agent loop and any LLM backend.
// s01 had a hidden FakeProvider concrete type; s02 promotes it into a real
// interface so swapping OpenAI <-> Anthropic <-> mock costs zero loop code.
//
// Why pass tools separately instead of merging them into msgs?
//
//	Most chat APIs require a top-level `tools` field — they will reject
//	tool_use blocks if no tool catalog was declared. Keeping tools out
//	of the message stream matches that wire shape (and matches upstream
//	browser_use/llm/openai/chat.py which passes tools via SDK params).
type Provider interface {
	// Invoke runs one round-trip with the LLM. It must:
	//   - serialize msgs into the provider's wire format,
	//   - declare the tools as callable,
	//   - parse the response into Response (Text, Actions, StopReason),
	//   - fill InputTokens / OutputTokens / Model when the API reports them.
	//
	// Errors should be propagated; the loop decides retry policy. This
	// interface deliberately has no Stream() method — agent-style usage
	// is request/response, not token-by-token streaming.
	Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error)
}

// ToolSchema is the JSON-Schema description of one tool/action the LLM
// may choose. We deliberately keep Parameters as raw JSON so callers can
// hand-write strict schemas without bouncing through Go reflection — s04
// is where we automate this conversion.
//
//	Name        — unique tool identifier (e.g. "search", "navigate")
//	Description — one-sentence description shown to the model
//	Parameters  — JSON Schema for the tool's input args
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
