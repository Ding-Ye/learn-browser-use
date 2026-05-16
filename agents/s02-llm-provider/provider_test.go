package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestMockProviderQueue verifies that MockProvider returns responses in
// FIFO order and errors after the queue is exhausted (rather than
// blocking or returning a zero Response).
func TestMockProviderQueue(t *testing.T) {
	t.Parallel()

	mock := &MockProvider{
		Queue: []Response{
			{Text: "first", StopReason: "tool_use"},
			{Text: "second", StopReason: "end_turn"},
		},
	}
	ctx := context.Background()

	r1, err := mock.Invoke(ctx, nil, nil)
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	if r1.Text != "first" {
		t.Errorf("first response Text = %q, want %q", r1.Text, "first")
	}

	r2, err := mock.Invoke(ctx, nil, nil)
	if err != nil {
		t.Fatalf("second invoke: %v", err)
	}
	if r2.Text != "second" {
		t.Errorf("second response Text = %q, want %q", r2.Text, "second")
	}

	// Queue exhausted: must error, must not block.
	if _, err := mock.Invoke(ctx, nil, nil); err == nil {
		t.Fatal("third invoke: want exhaustion error, got nil")
	}
}

// TestOpenAIProviderHTTPTest stubs out the OpenAI API with httptest and
// asserts the *outgoing* request shape — Authorization header, JSON body
// fields, role mapping. This is the test you'd run before pointing the
// provider at the real API.
func TestOpenAIProviderHTTPTest(t *testing.T) {
	t.Parallel()

	var capturedBody []byte
	var capturedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedBody, _ = io.ReadAll(r.Body)

		_, _ = w.Write([]byte(`{
			"model": "gpt-4o-mini-2024-07-18",
			"choices": [{
				"message": {"role": "assistant", "content": "hello"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
		}`))
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{
		APIKey:  "sk-test-fake",
		Model:   "gpt-4o-mini",
		BaseURL: srv.URL,
	}

	msgs := []Message{
		{Role: "system", Content: []ContentBlock{{Type: "text", Text: "you are helpful"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
	}
	tools := []ToolSchema{
		{Name: "noop", Description: "no-op", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	resp, err := p.Invoke(context.Background(), msgs, tools)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	// Validate the request the provider sent ----------------------------------
	if capturedAuth != "Bearer sk-test-fake" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "Bearer sk-test-fake")
	}

	var sent openAIRequest
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("captured body is not JSON: %v\nbody: %s", err, capturedBody)
	}
	if sent.Model != "gpt-4o-mini" {
		t.Errorf("sent model = %q, want %q", sent.Model, "gpt-4o-mini")
	}
	if len(sent.Messages) != 2 {
		t.Fatalf("sent %d messages, want 2", len(sent.Messages))
	}
	if sent.Messages[0].Role != "system" || sent.Messages[0].Content != "you are helpful" {
		t.Errorf("first message = %+v, want system/'you are helpful'", sent.Messages[0])
	}
	if sent.Messages[1].Role != "user" || sent.Messages[1].Content != "hi" {
		t.Errorf("second message = %+v, want user/'hi'", sent.Messages[1])
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Function.Name != "noop" {
		t.Errorf("sent tools = %+v, want one tool named noop", sent.Tools)
	}

	// Validate the parsed response --------------------------------------------
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q (mapped from stop)", resp.StopReason, "end_turn")
	}
	if resp.InputTokens != 5 || resp.OutputTokens != 1 {
		t.Errorf("tokens = (in=%d, out=%d), want (5, 1)", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "gpt-4o-mini-2024-07-18" {
		t.Errorf("Model = %q, want server-resolved %q", resp.Model, "gpt-4o-mini-2024-07-18")
	}
}

// TestOpenAIProviderHandlesToolCalls verifies that when the OpenAI API
// returns finish_reason=tool_calls with a tool_calls array, the provider
// turns each call into an ActionCall and maps StopReason to "tool_use".
func TestOpenAIProviderHandlesToolCalls(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"model": "gpt-4o-mini",
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "I will search.",
					"tool_calls": [
						{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"query\":\"foo\"}"}},
						{"id":"call_2","type":"function","function":{"name":"navigate","arguments":"{\"url\":\"https://x.test\"}"}}
					]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
		}`))
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{APIKey: "sk", Model: "gpt-4o-mini", BaseURL: srv.URL}

	resp, err := p.Invoke(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if len(resp.Actions) != 2 {
		t.Fatalf("Actions = %d, want 2", len(resp.Actions))
	}
	if resp.Actions[0].Name != "search" || !strings.Contains(resp.Actions[0].Input, `"foo"`) {
		t.Errorf("Actions[0] = %+v, want search/{query:foo}", resp.Actions[0])
	}
	if resp.Actions[1].Name != "navigate" || !strings.Contains(resp.Actions[1].Input, `https://x.test`) {
		t.Errorf("Actions[1] = %+v, want navigate/{url:https://x.test}", resp.Actions[1])
	}
	if resp.Text != "I will search." {
		t.Errorf("Text = %q, want %q", resp.Text, "I will search.")
	}
}

// TestOpenAIProviderRetriesOn429 starts a server that returns 429 on the
// first hit and 200 on the second. The provider must retry once
// (MaxRetries default = 1) and surface the eventual success.
func TestOpenAIProviderRetriesOn429(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"model": "gpt-4o-mini",
			"choices": [{
				"message": {"role":"assistant","content":"ok"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{
		APIKey:     "sk",
		Model:      "gpt-4o-mini",
		BaseURL:    srv.URL,
		MaxRetries: 1,
		RetryDelay: 10 * time.Millisecond, // keep test fast
	}

	resp, err := p.Invoke(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d calls, want 2 (one retry)", got)
	}
	if resp.Text != "ok" {
		t.Errorf("Text = %q, want %q", resp.Text, "ok")
	}
}

// TestProviderInterfaceCompatibility is a compile-time + runtime check
// that both MockProvider and OpenAIProvider satisfy the Provider
// interface. If a future refactor breaks the contract, this test fails
// at compile time with a useful pointer.
func TestProviderInterfaceCompatibility(t *testing.T) {
	t.Parallel()

	var providers = []Provider{
		&MockProvider{Queue: []Response{{Text: "x", StopReason: "end_turn"}}},
		&OpenAIProvider{APIKey: "x", Model: "m", BaseURL: "http://127.0.0.1:0"},
	}

	// Exercise only the mock (the OpenAI one would try to dial). The point
	// is the slice literal itself — that is the interface-conformance check.
	resp, err := providers[0].Invoke(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("mock as Provider: %v", err)
	}
	if resp.Text != "x" {
		t.Errorf("Text = %q, want %q", resp.Text, "x")
	}
}

// TestOpenAIProviderToolResultRoundTrip exercises convertMessage with a
// realistic conversation: system → user → assistant w/ tool_use → tool
// result. The captured body must contain `tool_calls` on the assistant
// message and a `tool` role message with the matching tool_call_id.
func TestOpenAIProviderToolResultRoundTrip(t *testing.T) {
	t.Parallel()

	var captured openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{
			"model": "gpt-4o-mini",
			"choices": [{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage": {"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{APIKey: "sk", Model: "gpt-4o-mini", BaseURL: srv.URL}
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "search foo"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: "text", Text: "ok"},
			{Type: "tool_use", Name: "search", Input: `{"query":"foo"}`},
		}},
		{Role: "tool", Content: []ContentBlock{
			{Type: "tool_result", Result: "got 3 hits"},
		}},
	}
	if _, err := p.Invoke(context.Background(), msgs, nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	// We expect 3 wire messages: user, assistant (w/ tool_calls), tool.
	if len(captured.Messages) != 3 {
		t.Fatalf("captured %d wire messages, want 3: %+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[1].Role != "assistant" || len(captured.Messages[1].ToolCalls) != 1 {
		t.Errorf("assistant message missing tool_calls: %+v", captured.Messages[1])
	}
	if captured.Messages[2].Role != "tool" || captured.Messages[2].Content != "got 3 hits" {
		t.Errorf("tool result wire message wrong: %+v", captured.Messages[2])
	}
}
