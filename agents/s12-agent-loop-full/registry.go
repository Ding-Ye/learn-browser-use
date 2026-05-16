package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// registry.go is a self-contained re-declaration of s04's
// Registry + Dispatcher. The shape is the same: a map of Tool by name,
// a Dispatcher that wraps Run in a ctx-deadline, and a Schemas()
// method that hands the per-tool JSON-Schema list to the Provider.
//
// What's new in s12: nothing here. The interesting integration code
// lives in agent.go. Registry is included so the chapter is buildable
// from scratch; the tool implementations themselves are in tools.go.

// DefaultActionTimeout matches s04's value (180s) which itself mirrors
// upstream's _ACTION_TIMEOUT_FALLBACK_S. Long enough for chained CDP
// calls or an extraction LLM; short enough that a wedged handler
// surfaces as an error inside one minute of dev-iteration time.
const DefaultActionTimeout = 180 * time.Second

// Tool is the action interface. Same four methods as s04.
type Tool interface {
	Name() string
	Description() string
	Schema() ToolSchema
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry holds the tools keyed by name. Lookup is O(1); All() and
// Schemas() return name-sorted slices so the LLM sees a deterministic
// order regardless of insertion order.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry with the internal map
// allocated so Register doesn't panic on first use.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a Tool. Re-registering the same name is an error so
// programmer mistakes surface loudly rather than silently letting the
// second tool shadow the first.
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return errors.New("registry: tool returned empty Name()")
	}
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("registry: tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// MustRegister is the panic-on-error variant for main()/tests.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Lookup returns the Tool and a presence bool.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns tools in name-sorted order.
func (r *Registry) All() []Tool {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// Schemas returns the schema list in the same sorted order as All().
// This is what the Provider hands to the LLM verbatim.
func (r *Registry) Schemas() []ToolSchema {
	tools := r.All()
	out := make([]ToolSchema, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Schema())
	}
	return out
}

// Dispatcher wraps Run with a per-call timeout. The timeout policy
// lives outside individual Tools so a buggy / slow tool can't wedge
// the agent loop forever.
type Dispatcher struct {
	Registry *Registry
	Timeout  time.Duration // 0 ⇒ DefaultActionTimeout
}

// Act runs one ActionCall and returns a tool_result ContentBlock the
// caller can append straight into the message history. The block is
// always returned (even on tool error) so the LLM gets to see the
// failure message on the next turn.
func (d *Dispatcher) Act(ctx context.Context, call ActionCall) (ContentBlock, error) {
	if d.Registry == nil {
		return ContentBlock{}, errors.New("dispatcher: nil Registry")
	}

	tool, ok := d.Registry.Lookup(call.Name)
	if !ok {
		msg := fmt.Sprintf("unknown action %q", call.Name)
		return ContentBlock{Type: "tool_result", Name: call.Name, Result: msg},
			fmt.Errorf("%s", msg)
	}

	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultActionTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw := json.RawMessage(call.Input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}

	out, err := tool.Run(runCtx, raw)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("action %q timed out after %s", call.Name, timeout)
		}
		return ContentBlock{
			Type:   "tool_result",
			Name:   call.Name,
			Input:  call.Input,
			Result: fmt.Sprintf("tool error: %v", err),
		}, err
	}

	return ContentBlock{
		Type:   "tool_result",
		Name:   call.Name,
		Input:  call.Input,
		Result: out,
	}, nil
}
