package main

import (
	"fmt"
	"sync"
)

// cdp_client.go is the recording stub CDP client. Same shape and
// purpose as s05/s07: every Send call is appended to an in-memory
// frame log so demos and tests can show "the agent issued these CDP
// methods". Real chromedp / cdp-use wiring is a different, larger
// chapter we deliberately omit here — see the s_full omission table.

// CDPClient is the one-method interface a BrowserSession depends on.
// Send dispatches a CDP method by name with a params map and returns
// a result map.
type CDPClient interface {
	Send(method string, params map[string]any) (map[string]any, error)
}

// CDPFrame is one entry in the recorder. Method + Params are the
// out-going call; Result is the response we synthesized.
type CDPFrame struct {
	Method string
	Params map[string]any
	Result map[string]any
}

// RecordingCDPClient is the test/demo implementation. Frames is the
// in-memory log; SendHook lets a test stub a specific method (used by
// the Navigation tool to fake "the page loaded").
type RecordingCDPClient struct {
	mu       sync.Mutex
	Frames   []CDPFrame
	SendHook func(method string, params map[string]any) (map[string]any, error)
}

// NewRecordingCDPClient returns a zero-state recorder.
func NewRecordingCDPClient() *RecordingCDPClient {
	return &RecordingCDPClient{}
}

// Send records the call into Frames and either dispatches through
// SendHook or returns a canned {"ok": true} result.
func (c *RecordingCDPClient) Send(method string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result map[string]any
	var err error
	if c.SendHook != nil {
		result, err = c.SendHook(method, params)
	} else {
		result = map[string]any{"ok": true}
	}

	c.Frames = append(c.Frames, CDPFrame{
		Method: method,
		Params: params,
		Result: result,
	})
	return result, err
}

// FrameLog returns a printable summary of the recorded frames. Used
// by main.go to show "the agent issued these CDP calls" at the end of
// a demo run.
func (c *RecordingCDPClient) FrameLog() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Frames) == 0 {
		return "(no CDP frames sent)"
	}
	out := ""
	for i, f := range c.Frames {
		out += fmt.Sprintf("  [%d] %s %v\n", i, f.Method, f.Params)
	}
	return out
}
