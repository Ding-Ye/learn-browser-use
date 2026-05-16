package main

import (
	"context"
	"fmt"
)

// MockProvider returns the responses queued in Queue, in order. Once the
// queue is exhausted it returns an error — better than blocking forever or
// returning a confusing zero value.
//
// Used by both unit tests and the -mock CLI flag. The mock does NOT inspect
// the messages or tools; tests that need to verify what was sent should set
// CaptureRequests = true and read back from CalledWith.
type MockProvider struct {
	Queue       []Response
	cursor      int
	CalledWith  []mockCall // populated when CaptureRequests is true
	CaptureReqs bool
}

type mockCall struct {
	Msgs  []Message
	Tools []ToolSchema
}

// Invoke pops the next queued Response. After the queue runs out, returns
// an explicit error rather than panicking — this surfaces "test forgot to
// queue enough responses" instantly instead of as a nil pointer down the line.
func (m *MockProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
	if m.CaptureReqs {
		// Defensive copy so test mutations on later calls don't change history.
		msgsCopy := make([]Message, len(msgs))
		copy(msgsCopy, msgs)
		toolsCopy := make([]ToolSchema, len(tools))
		copy(toolsCopy, tools)
		m.CalledWith = append(m.CalledWith, mockCall{Msgs: msgsCopy, Tools: toolsCopy})
	}
	if m.cursor >= len(m.Queue) {
		return Response{}, fmt.Errorf("MockProvider: queue exhausted after %d calls", m.cursor)
	}
	resp := m.Queue[m.cursor]
	m.cursor++
	return resp, nil
}

// Reset rewinds the queue cursor so the same MockProvider can be reused
// in table-driven tests. CalledWith is also cleared.
func (m *MockProvider) Reset() {
	m.cursor = 0
	m.CalledWith = nil
}
