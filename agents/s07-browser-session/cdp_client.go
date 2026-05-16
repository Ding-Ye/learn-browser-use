package main

import (
	"fmt"
	"sync"
)

// cdp_client.go is the s07-local re-declaration of the recording stub
// CDPClient first introduced in s05. Three notes about why we re-declare
// instead of importing s05:
//
//  1. Curriculum invariant — no cross-session Go imports. Each sNN
//     stands on its own.
//  2. The s07 use-case is *smaller* than s05's — we only need Send and
//     a Frames recorder; no Click/Type/Screenshot helpers, no error
//     injection. Keeping the smaller surface here makes it obvious
//     which capabilities a session lifecycle actually needs.
//  3. Pedagogically: the same stub appearing twice with the same shape
//     teaches that "this is the same idea" without requiring readers
//     to jump back and re-read s05.
//
// What's a stub recorder? Instead of sending JSON-RPC frames over a
// WebSocket to a real Chromium, we append them to an in-memory slice.
// Tests look at the slice. Demos print it. Real CDP integration would
// drop in via a one-method interface swap.

// CDPClient is the contract a BrowserSession depends on. The single
// method, Send, takes the JSON-RPC method name plus its params and
// returns the (params, result) of the simulated response.
//
// Why method strings instead of typed methods like `Target.AttachToTarget`?
// Two reasons: (1) keeps the stub surface tiny (one method instead of
// 200), (2) matches what cdp-use does internally — `cdp_client.send.X.Y`
// is just sugar over a string method + dict params.
type CDPClient interface {
	Send(method string, params map[string]any) (map[string]any, error)
}

// CDPFrame is one entry in the recorder. Keeping params + result on
// the same struct lets a test inspect either side of the round-trip
// without joining two slices.
type CDPFrame struct {
	Method string
	Params map[string]any
	Result map[string]any
}

// RecordingCDPClient is the test/demo implementation. Every call to
// Send appends a CDPFrame to Frames. The default Result is an empty
// map so tools and watchdogs that try to read a field get an empty
// value rather than a nil deref.
//
// A SendHook lets tests inject custom return values for specific
// methods — e.g. the lifecycle tests use it to assert
// "Target.attachToTarget was called and we returned a sessionId".
//
// The mutex makes concurrent emits safe — watchdogs running in
// parallel goroutines (s08+) will need this. For s07 single-threaded
// usage it's defensive cost only.
type RecordingCDPClient struct {
	mu       sync.Mutex
	Frames   []CDPFrame
	SendHook func(method string, params map[string]any) (map[string]any, error)
}

// NewRecordingCDPClient returns a zero-state recorder. Always go
// through this rather than `&RecordingCDPClient{}` so future bookkeeping
// (timestamps, sequence IDs) lands in one place.
func NewRecordingCDPClient() *RecordingCDPClient {
	return &RecordingCDPClient{}
}

// Send records the call into Frames and either dispatches through
// SendHook (when set) or returns a canned `{"ok": true}` result. The
// canned result is what makes the stub "good enough": a watchdog that
// peeks at the response sees a non-nil map; a test that doesn't care
// about the body just inspects Frames.
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

// FrameLog returns a human-readable listing of every Send call so far.
// Used by main() to demonstrate "here's what the session would have
// sent over the wire" and by tests for diff-friendly assertion errors.
func (c *RecordingCDPClient) FrameLog() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Frames) == 0 {
		return "(no CDP frames sent)"
	}
	out := ""
	for i, f := range c.Frames {
		out += fmt.Sprintf("[%d] %s %v\n", i, f.Method, f.Params)
	}
	return out
}
