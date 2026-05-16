package main

import (
	"fmt"
	"sort"
)

// Registry is the s04 replacement for s01's hard-coded `byName` map
// inside Agent.Run. The job is small but load-bearing: hold the tools,
// look them up by name, hand out their schemas so the Provider can
// show them to the LLM.
//
// In upstream this is browser_use/tools/registry/service.py:Registry —
// a Pydantic-flavoured class that decorates functions, normalizes
// their signatures, and validates calls via pydantic models. We keep
// just the registry part; schema generation lives in schema_gen.go,
// and execution lives in dispatcher.go. Splitting them keeps each
// file under 100 lines and the responsibilities obvious.
//
// Registry is not safe for concurrent Register calls — we assume
// registration is a single-threaded startup step, the same assumption
// upstream makes (the @action decorator runs at import time).
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry. We expose a constructor so
// the map is never nil — Register would otherwise panic on first use.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a Tool. Re-registering the same name is rejected
// loudly: silent overwrite would let two competing implementations
// of "click" coexist and only the last-loaded would win. Upstream's
// exclude_actions list is the inverse of this — both want
// "registration is explicit, not accidental".
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("registry: tool returned empty Name()")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("registry: tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// MustRegister is the panicking variant — convenient in main() and
// tests where a registration error is a programmer bug, not a runtime
// condition worth surfacing.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Lookup returns the tool matching name and whether it exists. We
// follow Go's "comma ok" idiom rather than returning a sentinel error
// because the dispatcher wants the typed "not found" branch.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns the tools in deterministic (name-sorted) order. Maps
// in Go iterate randomly; tests and golden outputs would flake if we
// returned that random order directly.
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

// Schemas returns the schema of every registered tool, in the same
// sorted order as All(). This is the slice the Provider will hand to
// the LLM verbatim ("here are the tools you may call").
func (r *Registry) Schemas() []ToolSchema {
	tools := r.All()
	out := make([]ToolSchema, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Schema())
	}
	return out
}
