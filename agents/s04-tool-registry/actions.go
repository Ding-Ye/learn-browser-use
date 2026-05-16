package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Three example tools, all matching the Tool interface. Each tool:
//
//   1. Declares its input shape as a struct with `json` + `desc` tags
//      so SchemaFromStruct can produce a JSON Schema automatically.
//   2. Stores the auto-generated schema once at construction time
//      (via the Schema() method using a package-level helper) so
//      reflection only runs when callers ask for it.
//   3. Unmarshals into that struct inside Run.
//
// In a real browser-use port these would chain into the CDPClient
// from s05. Here we keep them pure-text so this session has zero
// dependencies and tests are deterministic.

// ---------- SearchTool ----------

type searchArgs struct {
	Query string `json:"query" desc:"the search query to run"`
}

type SearchTool struct{}

func (SearchTool) Name() string { return "search" }

func (SearchTool) Description() string {
	return "Search the web and return the top 3 result URLs."
}

func (SearchTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "search",
		Description: SearchTool{}.Description(),
		Parameters:  SchemaFromStruct(searchArgs{}),
	}
}

func (SearchTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return "", fmt.Errorf("search: query must be non-empty")
	}
	return fmt.Sprintf(
		"top 3 hits for %q: example.com/%s, en.wikipedia.org/wiki/%s, github.com/search?q=%s",
		q, q, q, q,
	), nil
}

// ---------- TypeTool ----------

type typeArgs struct {
	Text  string `json:"text" desc:"the text to type into the element"`
	Index int    `json:"index" desc:"the selector-map index of the target input element"`
}

type TypeTool struct{}

func (TypeTool) Name() string { return "type_text" }

func (TypeTool) Description() string {
	return "Type the given text into the input element identified by its selector-map index."
}

func (TypeTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "type_text",
		Description: TypeTool{}.Description(),
		Parameters:  SchemaFromStruct(typeArgs{}),
	}
}

func (TypeTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args typeArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("type_text: %w", err)
	}
	if args.Index < 0 {
		return "", fmt.Errorf("type_text: index must be >= 0, got %d", args.Index)
	}
	return fmt.Sprintf("typed %q into element [%d]", args.Text, args.Index), nil
}

// ---------- ScrollTool ----------

type scrollArgs struct {
	Direction string `json:"direction" desc:"scroll direction: up | down | top | bottom"`
}

type ScrollTool struct{}

func (ScrollTool) Name() string { return "scroll" }

func (ScrollTool) Description() string {
	return "Scroll the page in the given direction."
}

func (ScrollTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "scroll",
		Description: ScrollTool{}.Description(),
		Parameters:  SchemaFromStruct(scrollArgs{}),
	}
}

func (ScrollTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args scrollArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("scroll: %w", err)
	}
	d := strings.ToLower(strings.TrimSpace(args.Direction))
	switch d {
	case "up", "down", "top", "bottom":
		return fmt.Sprintf("scrolled %s", d), nil
	default:
		return "", fmt.Errorf("scroll: unknown direction %q (want up|down|top|bottom)", args.Direction)
	}
}
