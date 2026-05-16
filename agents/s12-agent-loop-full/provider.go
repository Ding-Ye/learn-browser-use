package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// provider.go re-declares the LLM Provider seam, plus the two concrete
// implementations we ship: MockProvider (the test/demo driver) and
// OpenAIProvider (the deliberate stub that documents the real shape).
//
// Two non-obvious choices in this file:
//
//   1. MockProvider supports per-response Delay. That lets the
//      TestFallbackOnTimeout case build a provider that hangs for
//      longer than Agent.LLMTimeout — no real HTTP, no sleeping
//      goroutines, just a select on ctx.Done() vs a timer.
//
//   2. OpenAIProvider returns ErrNotImplemented on Invoke. We expose the
//      struct (with BaseURL field) so the README and demo can show
//      "here's what you'd plug in", without requiring a network or an
//      API key to compile the chapter. Real wiring lives in s02; here
//      the stub's job is documentation.

// Provider is the single seam between the agent loop and any LLM
// backend. Same signature as s02; we kept it stable across all 11
// downstream chapters so this s12 integration just consumes it.
type Provider interface {
	Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error)
}

// ---------------------------------------------------------------
//  MockProvider
// ---------------------------------------------------------------

// MockProvider returns the responses queued in Queue, in order. Once
// the queue is exhausted it returns an error — better than blocking
// forever or returning a confusing zero value.
//
// The Delay slice (one entry per response, or empty) lets a test
// simulate slow turns. Index i in Delay matches Queue[i]. The
// per-response delay is honored via a ctx-cancellable timer, so the
// caller's deadline propagates cleanly — that's the mechanism the
// fallback test uses to detect "primary is hung".
type MockProvider struct {
	Queue       []Response
	Delay       []time.Duration

	mu          sync.Mutex
	cursor      int
	CalledWith  []MockCall
	CaptureReqs bool
}

// MockCall captures one invocation when CaptureReqs is true. Useful
// for assertions like "the planner saw 5 messages by step 5".
type MockCall struct {
	Msgs  []Message
	Tools []ToolSchema
}

// Invoke pops the next queued Response. If Delay[cursor] is set, the
// method sleeps that long honoring ctx — if ctx fires first, Invoke
// returns ctx.Err() so the loop's fallback path activates.
func (m *MockProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
	m.mu.Lock()
	cur := m.cursor
	if m.CaptureReqs {
		msgsCopy := append([]Message(nil), msgs...)
		toolsCopy := append([]ToolSchema(nil), tools...)
		m.CalledWith = append(m.CalledWith, MockCall{Msgs: msgsCopy, Tools: toolsCopy})
	}
	if cur >= len(m.Queue) {
		m.mu.Unlock()
		return Response{}, fmt.Errorf("MockProvider: queue exhausted after %d calls", cur)
	}
	resp := m.Queue[cur]
	m.cursor++
	var delay time.Duration
	if cur < len(m.Delay) {
		delay = m.Delay[cur]
	}
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	return resp, nil
}

// Reset rewinds the queue cursor so the same MockProvider can be
// reused in table-driven tests.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = 0
	m.CalledWith = nil
}

// CallCount returns the number of Invoke calls so far.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor
}

// ---------------------------------------------------------------
//  OpenAIProvider (intentional stub)
// ---------------------------------------------------------------

// ErrNotImplemented is returned by OpenAIProvider.Invoke. The s12 demo
// never hits this path; it exists so the type compiles and the README
// can point at "this is where you'd wire chat.completions".
var ErrNotImplemented = errors.New("openai provider: not implemented in s12 demo; see s02 for real HTTP wiring")

// OpenAIProvider is a tombstone struct: the README points readers at
// s02 (which has a working OpenAI HTTP client). We keep BaseURL +
// APIKey here so the field list reads "this is what you'd configure",
// and Invoke returns ErrNotImplemented so the type satisfies Provider
// without dragging an httptest into the binary.
//
// Why have it at all? Two reasons:
//   - The Agent struct's Provider/Fallback fields are typed Provider,
//     so demonstrating "swap in a real one here" needs a type to
//     point at.
//   - Tests can build an Agent with `Fallback: &OpenAIProvider{}` to
//     prove the fallback wiring picks the secondary up even when the
//     secondary itself errors (a third-tier "max out and give up"
//     behavior is left to future work; see TestFallbackOnTimeout).
type OpenAIProvider struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Invoke always returns ErrNotImplemented. A real implementation would
// POST to BaseURL+"/chat/completions" — see s02/openai_provider.go for
// a working version of that function.
func (p *OpenAIProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
	return Response{}, ErrNotImplemented
}
