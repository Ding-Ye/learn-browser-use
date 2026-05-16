package main

import (
	"context"
	"encoding/base64"
	"fmt"
)

// CDPClient is the abstraction every Element method talks to. The real
// upstream client opens a WebSocket to Chromium's `/devtools/browser`
// endpoint and sends JSON envelopes; ours is whatever you plug in.
//
// The single Send method is intentionally small: it matches the shape
// of `cdp_client.send.<Domain>.<method>(params)` in upstream Python
// when collapsed into one function. We don't model per-domain methods
// as separate Go methods because that would force a Go-side mirror of
// the entire CDP surface (hundreds of methods). One method + a string
// dispatch key is enough for teaching, and matches how real CDP looks
// on the wire anyway — every call is a JSON `{"method": "...", ...}`
// envelope regardless of which "domain" it belongs to.
//
// Send must be safe to call from a single goroutine; the recorder we
// ship below is not concurrent-safe by design (see comments).
type CDPClient interface {
	// Send invokes one CDP method by its dotted name (e.g.
	// "Input.dispatchMouseEvent") with the given params map. The
	// return is the parsed `result` object for that method, or an
	// error if the underlying transport / Chromium failed.
	//
	// We thread ctx through so callers can plug in real deadlines
	// once s07's BrowserSession arrives, even though the recorder
	// here ignores cancellation.
	Send(ctx context.Context, method string, params map[string]any) (map[string]any, error)
}

// RecordingCDPClient is a CDPClient that doesn't talk to any browser.
// Every Send call is appended to Frames; responses are looked up in
// the Responses table keyed by method name (with a small built-in
// default table for the methods s05 actually touches).
//
// The recorder pattern is the right shape for this chapter because
// it lets tests inspect *exactly* the CDP frames we would have sent
// to Chromium, without booting Chromium. We get fast deterministic
// tests and a printable demo, at the cost of obviously never
// validating the real protocol grammar. That trade-off is the whole
// point of the s05 → s07 boundary: s07 will swap this implementation
// for a real WebSocket without touching Element.
//
// Not safe for concurrent Send. The real CDP client funnels through
// a single WebSocket goroutine for ordering reasons; replicating that
// here would mean a goroutine + channel and is overkill for a stub.
type RecordingCDPClient struct {
	// Frames is the append-only log of recorded calls, in order.
	// Tests read it directly.
	Frames []Frame

	// Responses is an optional caller-supplied response table keyed
	// by CDP method name. If a method isn't in the table, the
	// recorder falls back to its built-in defaults (see
	// defaultResponses below). Empty map and nil map both mean
	// "use defaults".
	Responses map[string]map[string]any
}

// NewRecordingCDPClient returns a recorder ready to use. We expose
// a constructor so callers don't accidentally embed a nil Frames slice
// and then write to it through a non-pointer receiver — small thing,
// but new() vs &Struct{} confuses Go newcomers.
func NewRecordingCDPClient() *RecordingCDPClient {
	return &RecordingCDPClient{
		Frames:    make([]Frame, 0, 8),
		Responses: make(map[string]map[string]any),
	}
}

// Send records the call and returns either the caller-supplied
// response from Responses, or the default for that method.
//
// We deliberately don't copy `params` here — the caller is trusted
// not to mutate it after passing it in. The Element methods in this
// session always build the map fresh, so aliasing is not a real risk.
func (c *RecordingCDPClient) Send(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	c.Frames = append(c.Frames, Frame{Method: method, Params: params})

	if c.Responses != nil {
		if r, ok := c.Responses[method]; ok {
			return r, nil
		}
	}
	if r, ok := defaultResponses[method]; ok {
		return r, nil
	}
	// Unknown method: return an empty result rather than an error.
	// CDP itself returns `{}` for many side-effecting methods, so
	// "empty success" is the friendlier default. Tests that want to
	// assert a specific shape can pre-populate Responses.
	return map[string]any{}, nil
}

// defaultResponses is the built-in stub table covering exactly the
// methods Element calls in s05. Keep this list short: every entry
// here is a place where the real CDP shape would matter, and we want
// the diff vs reality to stay readable.
//
// captureScreenshot is the only entry that returns non-trivial bytes —
// see Screenshot in element.go for why we hand back an 8-byte PNG
// signature instead of a real image.
var defaultResponses = map[string]map[string]any{
	// Page.captureScreenshot returns `{"data": "<base64>"}` in real
	// CDP. We hand back the base64 of an 8-byte PNG signature so the
	// caller can verify "yes, this looks like a PNG header".
	"Page.captureScreenshot": {
		"data": base64.StdEncoding.EncodeToString([]byte{
			0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		}),
	},
	// DOM.pushNodesByBackendIdsToFrontend would normally return a
	// list of nodeIds correlated to the requested backendNodeIds.
	// Element doesn't read this in s05's reduced API, but we keep
	// the entry so callers extending Element can see the shape.
	"DOM.pushNodesByBackendIdsToFrontend": {
		"nodeIds": []any{1},
	},
}

// dumpFrame is a small helper main.go and tests can reuse to render
// a Frame for display. We don't bring in encoding/json here because
// main.go does its own pretty-print; this is the plain-text fallback.
func dumpFrame(f Frame) string {
	return fmt.Sprintf("%s %v", f.Method, f.Params)
}
