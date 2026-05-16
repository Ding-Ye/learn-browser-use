package main

import (
	"context"
	"encoding/json"
)

// Tool is the action interface for s04 onward. The four methods cover
// the whole life of a tool in a browser-use-shaped agent:
//
//   Name        — the identifier the LLM emits in tool_use blocks
//   Description — the one-line nudge the LLM sees in the schema list
//   Schema      — JSON Schema describing the input arguments
//   Run         — execute the tool against typed input, return text
//
// Why a Go interface here instead of registering bare functions like
// browser-use's `@registry.action(...)` decorator? Decorators in Python
// inspect signatures at registration time; in Go the closest equivalent
// is a small interface plus a code-gen step (Schema()) on each tool.
// Each tool stays a struct so tests can construct it directly, and the
// Dispatcher stays a plain function over Tool values.
//
// Run takes a context so the Dispatcher can attach a deadline (see
// dispatcher.go). The input is the raw JSON the LLM produced — each
// tool unmarshals into its own struct, which keeps schema and decode
// logic next to each other in the same file.
type Tool interface {
	Name() string
	Description() string
	Schema() ToolSchema
	Run(ctx context.Context, input json.RawMessage) (string, error)
}
