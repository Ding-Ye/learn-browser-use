package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// s04-tool-registry demo binary.
//
// What it does:
//   1. Builds a Registry and registers three tools.
//   2. Prints each tool's auto-generated JSON Schema (pretty-printed).
//   3. Dispatches one example ActionCall ({"name":"search",
//      "input":"{\"query\":\"hacker news\"}"}) through the Dispatcher
//      and prints the resulting ContentBlock.
//
// Run: go run .
//
// The point is to make schema introspection visible: in s02 the LLM
// got an empty toolbox; here it would see three fully-typed tools.
// In s05+ the same Registry holds CDP-backed tools and the rest of
// the loop never has to change.
func main() {
	reg := NewRegistry()
	reg.MustRegister(SearchTool{})
	reg.MustRegister(TypeTool{})
	reg.MustRegister(ScrollTool{})

	fmt.Println("# registered tools and their auto-generated schemas")
	fmt.Println()
	for _, schema := range reg.Schemas() {
		pretty, err := prettyJSON(schema)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to pretty-print schema for %s: %v\n", schema.Name, err)
			os.Exit(1)
		}
		fmt.Println(pretty)
		fmt.Println()
	}

	// Dispatch one example call.
	d := &Dispatcher{Registry: reg, Timeout: DefaultTimeout}
	call := ActionCall{
		Name:  "search",
		Input: `{"query":"hacker news"}`,
	}
	fmt.Println("# example dispatch")
	fmt.Printf("call: %s(%s)\n", call.Name, call.Input)

	block, err := d.Act(context.Background(), call)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("result: %s\n", block.Result)
}

// prettyJSON renders a ToolSchema with two-space indentation. The
// Parameters field is already JSON-encoded so we re-marshal the whole
// thing to keep nesting consistent.
func prettyJSON(s ToolSchema) (string, error) {
	type out struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	b, err := json.MarshalIndent(out{
		Name:        s.Name,
		Description: s.Description,
		Parameters:  s.Parameters,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
