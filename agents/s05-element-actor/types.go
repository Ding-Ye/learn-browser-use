package main

// BackendNodeID is Chromium's stable identifier for a DOM node, sourced
// from CDP's `DOM.Node.backendNodeId`. It survives across CSS rebuilds
// and most reflows, which is exactly why the upstream agent passes it
// around instead of CSS selectors or even DOM node ids — the latter
// get re-assigned every time the frontend reattaches.
//
// We type-alias rather than redefine so the literal `42` in tests reads
// the same as `BackendNodeID(42)`; the JSON we hand to the CDP layer is
// just an int either way.
//
// Mapping to upstream: `browser_use/actor/element.py`'s
// `self._backend_node_id: int` field — see lines 65–74 of the upstream
// excerpt.
type BackendNodeID int

// ClickOptions mirrors the three kwargs of upstream's `Element.click`:
// the mouse button (left/middle/right), the click count (single vs
// double), and optional modifier keys. We expose modifiers as a slice
// of strings — "Alt" / "Control" / "Meta" / "Shift" — instead of an
// enum, so callers can construct the same set Chromium accepts without
// us inventing a Go enum that needs maintenance.
//
// Zero value is a valid single left-click with no modifiers. That makes
// `el.Click(ctx, ClickOptions{})` the shortest correct call.
type ClickOptions struct {
	// Button is "left", "middle", or "right". Empty string is treated
	// as "left" by the dispatch path — see element.go::Click.
	Button string
	// ClickCount is 1 for single-click, 2 for double, etc. Zero is
	// normalised to 1 at dispatch time.
	ClickCount int
	// Modifiers is the subset of {Alt, Control, Meta, Shift} held down
	// during the click. Order doesn't matter; duplicates are harmless
	// because Chromium bitmasks them.
	Modifiers []string
}

// Frame is one recorded CDP message. The real CDP wire format is a
// JSON envelope `{"id": N, "method": "...", "params": {...}}` going
// out over a WebSocket; the response is `{"id": N, "result": {...}}`.
// We strip the `id` because the recorder is single-shot and order-
// preserving — the index in `Frames` is identity enough.
//
// Method is the dotted CDP method name ("Input.dispatchMouseEvent",
// "Page.captureScreenshot", ...). Params is the exact payload that
// would be JSON-encoded onto the wire. We keep Params as a generic
// `map[string]any` rather than typed per-method structs so this whole
// session stays under ~400 lines and stdlib-only.
type Frame struct {
	Method string
	Params map[string]any
}
