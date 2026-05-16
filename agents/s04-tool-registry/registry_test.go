package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRegisterAndLookup — round-trip: a registered tool can be found
// by name, an unregistered name returns ok=false, and double-register
// of the same name fails.
func TestRegisterAndLookup(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(SearchTool{}); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	got, ok := reg.Lookup("search")
	if !ok {
		t.Fatalf("Lookup(\"search\") missing after Register")
	}
	if got.Name() != "search" {
		t.Errorf("Lookup returned wrong tool: got Name()=%q want %q", got.Name(), "search")
	}

	if _, ok := reg.Lookup("does_not_exist"); ok {
		t.Errorf("Lookup of unknown tool returned ok=true")
	}

	if err := reg.Register(SearchTool{}); err == nil {
		t.Errorf("second Register of same tool should fail, got nil error")
	}

	// All() / Schemas() are deterministic and sorted.
	reg.MustRegister(TypeTool{})
	reg.MustRegister(ScrollTool{})
	names := []string{}
	for _, tt := range reg.All() {
		names = append(names, tt.Name())
	}
	want := []string{"scroll", "search", "type_text"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("All() order = %v, want %v", names, want)
	}
}

// TestSchemaGenerationFromStruct — a struct with one string, one int,
// one bool field must produce the right JSON Schema primitive types
// and list every non-omitempty field in required.
func TestSchemaGenerationFromStruct(t *testing.T) {
	type sample struct {
		Query  string `json:"query"`
		Index  int    `json:"index"`
		Strict bool   `json:"strict"`
	}
	raw := SchemaFromStruct(sample{})

	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("top-level type = %v, want object", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing or wrong shape: %T", schema["properties"])
	}
	cases := map[string]string{
		"query":  "string",
		"index":  "integer",
		"strict": "boolean",
	}
	for name, wantType := range cases {
		p, ok := props[name].(map[string]interface{})
		if !ok {
			t.Errorf("property %q missing", name)
			continue
		}
		if p["type"] != wantType {
			t.Errorf("property %q type = %v, want %v", name, p["type"], wantType)
		}
	}

	required, ok := schema["required"].([]interface{})
	if !ok {
		t.Fatalf("required missing or wrong shape")
	}
	if len(required) != 3 {
		t.Errorf("required length = %d, want 3 (no omitempty fields)", len(required))
	}
}

// TestSchemaGenerationWithTags — a `desc:"..."` tag must appear in
// the per-property "description" key. The `json:"name,omitempty"`
// tag must keep the field out of required.
func TestSchemaGenerationWithTags(t *testing.T) {
	type sample struct {
		Q    string `json:"q" desc:"the query string"`
		Skip string `json:"skip,omitempty"`
	}
	raw := SchemaFromStruct(sample{})

	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	props := schema["properties"].(map[string]interface{})

	qProp := props["q"].(map[string]interface{})
	if qProp["description"] != "the query string" {
		t.Errorf("q.description = %v, want %q", qProp["description"], "the query string")
	}

	// omitempty → not required.
	required, _ := schema["required"].([]interface{})
	for _, r := range required {
		if r == "skip" {
			t.Errorf("omitempty field 'skip' should not be required, got %v", required)
		}
	}
}

// TestDispatchTimeoutFires — a tool that sleeps 1s with a 100ms
// dispatcher timeout must come back as an error mentioning timeout,
// AND it must come back fast (not block the whole 1s).
func TestDispatchTimeoutFires(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(sleepyTool{d: 1 * time.Second})

	d := &Dispatcher{Registry: reg, Timeout: 100 * time.Millisecond}

	start := time.Now()
	block, err := d.Act(context.Background(), ActionCall{Name: "sleepy"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil; block = %+v", block)
	}
	if !strings.Contains(err.Error(), "timed out") && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected timeout error message, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("dispatcher waited %s — should have cancelled around 100ms", elapsed)
	}
	if block.Type != "tool_result" {
		t.Errorf("block.Type = %q, want tool_result even on timeout", block.Type)
	}
}

// TestDispatchUnknownActionReturnsError — a call referencing a name
// that was never registered must return a clear error and a
// tool_result block (so the caller can still feed something back to
// the LLM if it wants to).
func TestDispatchUnknownActionReturnsError(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(SearchTool{})

	d := &Dispatcher{Registry: reg}
	block, err := d.Act(context.Background(), ActionCall{Name: "totally_made_up"})
	if err == nil {
		t.Fatalf("expected error for unknown action, got nil")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error should mention unknown action, got: %v", err)
	}
	if block.Result == "" {
		t.Errorf("block.Result should describe the missing tool, was empty")
	}
}

// TestDispatchHappyPath — a registered tool with valid input returns
// a tool_result block whose Result matches the tool's output.
func TestDispatchHappyPath(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(SearchTool{})
	d := &Dispatcher{Registry: reg, Timeout: DefaultTimeout}

	block, err := d.Act(context.Background(), ActionCall{
		Name:  "search",
		Input: `{"query":"go testing"}`,
	})
	if err != nil {
		t.Fatalf("Act returned unexpected error: %v", err)
	}
	if block.Type != "tool_result" {
		t.Errorf("block.Type = %q, want tool_result", block.Type)
	}
	if !strings.Contains(block.Result, "go testing") {
		t.Errorf("block.Result missing query echo: %q", block.Result)
	}
}

// --- helpers ---

// sleepyTool blocks for d. Crucially, it respects ctx.Done() so the
// dispatcher's timeout actually wins. A tool that ignored ctx would
// pin the goroutine for the full duration.
type sleepyTool struct {
	d time.Duration
}

func (sleepyTool) Name() string        { return "sleepy" }
func (sleepyTool) Description() string { return "sleeps" }
func (sleepyTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "sleepy",
		Description: "sleeps",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
}
func (s sleepyTool) Run(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-time.After(s.d):
		return "slept", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
