package main

import "encoding/json"

// types.go re-declares the canonical shapes every other s12 file needs.
// The curriculum invariant is hard: each `agents/sNN-*` module compiles
// from scratch with zero cross-session imports. By s12 there are 11
// previous sessions' worth of types we COULD have imported; instead we
// inline the merged superset here so the integration chapter still
// builds in isolation.
//
// The shapes match s01-s11 exactly. If you ran a diff against the
// types.go in any earlier session you'd see this file as a strict
// superset — same field names, same JSON tags, with the small additions
// pulled in from s09 (DOM) and s10 (Pricing).
//
// Why bother with the duplication? Because the s12 reader should not
// have to chase 11 imports to learn what `Response.StopReason` is.
// Every interesting type lives in this one file; the rest of the
// module just uses them.

// ---------------------------------------------------------------
//  LLM-side: Message / ContentBlock / Response / ActionCall
// ---------------------------------------------------------------

// Message is one conversational turn between the agent and the LLM.
// Same shape as s01-s10. Role is "system" | "user" | "assistant" | "tool".
type Message struct {
	Role    string
	Content []ContentBlock
}

// ContentBlock is a tagged union over Type. Only the fields relevant to
// the type are non-zero. The four types in play here:
//
//   - "text"        : a plain-text block. Use Text.
//   - "tool_use"    : the assistant wants to call a tool. Use Name + Input.
//   - "tool_result" : we ran the tool; here is its output. Use Name + Result.
//   - "system"      : pre-prompt scaffolding (used by message_manager).
type ContentBlock struct {
	Type   string
	Text   string
	Name   string
	Input  string
	Result string
}

// ActionCall is one tool invocation the LLM emitted. Input is a JSON
// object whose schema matches the tool's ToolSchema.Parameters.
type ActionCall struct {
	Name  string
	Input string
}

// Response is what a Provider returns each turn. The same struct works
// for MockProvider (tests/demos), OpenAIProvider (the stub), and any
// future real backend.
//
// Fields:
//   - Text         : the assistant's prose part of the turn.
//   - Actions      : tool_use blocks the assistant emitted.
//   - StopReason   : "end_turn" | "tool_use" | "max_tokens".
//   - InputTokens  : prompt tokens (for cost tracking).
//   - OutputTokens : completion tokens.
//   - Model        : server-resolved model id.
type Response struct {
	Text         string
	Actions      []ActionCall
	StopReason   string
	InputTokens  int
	OutputTokens int
	Model        string
}

// ToolSchema is the JSON-Schema description a Provider hands to the LLM.
// Parameters stays json.RawMessage so each Tool's Schema() method can
// hand-write strict JSON without going through reflection.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---------------------------------------------------------------
//  DOM-side: DOMNode / SerializedDOM / DOMRect
// ---------------------------------------------------------------

// DOMNode is the minimal tree node carried by the DOMService snapshot.
// Same shape as s08/s09; we keep the six fields that are load-bearing
// for "give the LLM something to click".
type DOMNode struct {
	BackendNodeID int
	Tag           string
	Text          string
	Children      []*DOMNode
	BBox          DOMRect
	Visible       bool
}

// DOMRect is [x, y, w, h] in CSS pixels relative to the viewport.
// In s09 we used a [4]int alias; here we promote to a typed struct
// because the s12 demo prints it and a struct's field names are kinder
// to a human reader than positional indexes.
type DOMRect struct {
	X, Y, W, H int
}

// SerializedDOM is the cached, LLM-friendly view of the DOM. LLMText
// goes into the system/user prompt; SelectorMap lets the actor convert
// "[3]" back into a BackendNodeID + BBox without re-walking the tree.
type SerializedDOM struct {
	LLMText     string
	SelectorMap map[int]SelectorEntry
}

// SelectorEntry is one row of the SelectorMap.
type SelectorEntry struct {
	BackendNodeID int
	BBox          DOMRect
}

// ---------------------------------------------------------------
//  Pricing / Cost (s10 shapes)
// ---------------------------------------------------------------

// Pricing is the per-1k-token rate for one model. Same shape as s10.
type Pricing struct {
	InputPer1k  float64
	OutputPer1k float64
}

// ---------------------------------------------------------------
//  Event-bus (s06/s07 shapes)
// ---------------------------------------------------------------

// Event is the marker interface every value dispatched through the
// EventBus must satisfy. The single method exists so subscriptions can
// route by string without forcing every consumer to use reflection.
type Event interface {
	EventName() string
}
