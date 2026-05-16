package main

import "encoding/json"

// ActionCall is one tool/action the LLM wants the agent to run.
// Name picks the tool out of the Registry; Input is the JSON-encoded
// arguments matching the tool's schema. The shape is identical to what
// real Anthropic / OpenAI tool_use blocks carry, just narrowed to the
// two fields the dispatcher actually needs.
//
// s01 used a plain string for Input. Once we have a schema-driven
// Registry we promote Input to JSON because each tool now decodes
// into its own typed struct. Treat the raw string as opaque from the
// caller's perspective.
type ActionCall struct {
	Name  string
	Input string // JSON object matching the tool's parameter schema
}

// ContentBlock is the same tagged-union shape s01 introduced. We
// re-declare it locally so this session has zero cross-module imports.
// The dispatcher only ever produces blocks of Type "tool_result", but
// keeping the union complete lets downstream sessions (s12) merge
// dispatcher output directly into their message history without
// renaming fields.
type ContentBlock struct {
	Type   string // "text" | "tool_use" | "tool_result"
	Text   string // when Type == "text"
	Name   string // when Type == "tool_use" — the action name
	Input  string // when Type == "tool_use" — JSON arguments
	Result string // when Type == "tool_result" — stringified output
}

// ToolSchema is what the LLM eventually sees: a name, a one-line
// description, and a JSON Schema for the input arguments. Parameters
// is intentionally json.RawMessage rather than a typed struct so the
// schema generator (schema_gen.go) can hand back any well-formed JSON
// without forcing us to mirror the full JSON Schema vocabulary in Go.
//
// Mapping to the upstream Pydantic flow:
//   - Name        ↔ func.__name__
//   - Description ↔ the @action(description="...") string
//   - Parameters  ↔ pydantic model_json_schema()
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
