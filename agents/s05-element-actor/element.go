package main

import (
	"context"
	"encoding/base64"
	"fmt"
)

// Element is the s05 stand-in for upstream's `Element` class
// (`browser_use/actor/element.py`). It carries the two things every
// CDP element operation needs: a stable BackendNodeID and a CDPClient
// to dispatch through.
//
// Anything we don't strictly need lives elsewhere. Upstream's Element
// also holds a back-reference to BrowserSession (for layout metrics,
// scroll-into-view, session_id, etc.); we cut all of that to keep the
// chapter focused on "the CDP boundary" rather than session lifecycle.
// s07's BrowserSession chapter will reintroduce the surrounding context.
//
// Element is a small value type, copied by value cheaply. We don't make
// it a *struct because the natural-feeling call site is
// `el := Element{Client: c, NodeID: 42}` followed by `el.Click(...)`.
type Element struct {
	// Client is the CDP transport. Almost always a RecordingCDPClient
	// in s05, but the interface is what matters: s07 will hand in a
	// real WebSocket-backed implementation without touching Element.
	Client CDPClient

	// NodeID identifies the DOM element on the Chromium side. Stable
	// across CSS reflows; survives until the node is detached from
	// the document.
	NodeID BackendNodeID
}

// Click dispatches the mouse-event triplet that Chromium expects for a
// click: mouseMoved → mousePressed → mouseReleased. Upstream's element
// also runs scroll-into-view + viewport-clamping + a JS fallback path;
// we trim all of that because s05's job is "show that CDP is a frame
// stream", not "ship a production click". s06+ will layer those concerns
// back via watchdogs.
//
// Empty / zero ClickOptions is a valid left single-click with no
// modifiers — that's what the upstream defaults `button='left'`,
// `click_count=1`, `modifiers=None` work out to in our model.
func (e Element) Click(ctx context.Context, opts ClickOptions) error {
	button := opts.Button
	if button == "" {
		button = "left"
	}
	clickCount := opts.ClickCount
	if clickCount <= 0 {
		clickCount = 1
	}
	modifierMask := modifiersToBitmask(opts.Modifiers)

	// In real CDP the coordinates would come from DOM.getContentQuads
	// or getBoxModel. We pretend the element is at (0, 0) because s05
	// doesn't model layout — s07/s08 do. Tests inspect the structural
	// fields (button / clickCount / modifiers / backendNodeId), not
	// the geometry.
	pressParams := func(kind string) map[string]any {
		p := map[string]any{
			"type":          kind,
			"x":             0.0,
			"y":             0.0,
			"backendNodeId": int(e.NodeID),
		}
		if kind != "mouseMoved" {
			p["button"] = button
			p["clickCount"] = clickCount
			p["modifiers"] = modifierMask
		}
		return p
	}

	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		if _, err := e.Client.Send(ctx, "Input.dispatchMouseEvent", pressParams(kind)); err != nil {
			return fmt.Errorf("element click (%s): %w", kind, err)
		}
	}
	return nil
}

// Type sends `text` to the focused input. We model this with a single
// `Input.insertText` call rather than the per-character keyDown/char/
// keyUp triplets upstream uses in `Element.fill`. Two reasons:
//
//  1. `Input.insertText` is the right CDP call for **inserting** text
//     (it bypasses keyboard event simulation entirely and is the one
//     upstream uses in `skill_cli/python_session.py` for fast paste).
//     The per-character keyboard path exists upstream because real
//     pages sometimes listen for keydown to trigger autocomplete; for
//     s05's teaching scope that's a watchdog concern, not an Element
//     concern.
//  2. It makes the Unicode story cleaner: emoji and combining marks
//     are passed through verbatim as UTF-8 bytes in one frame, which
//     makes the recording readable and the tests trivial.
//
// s07's BrowserSession will reintroduce the keyboard-event path via a
// "TypeTextWatchdog".
func (e Element) Type(ctx context.Context, text string) error {
	params := map[string]any{
		"text":          text,
		"backendNodeId": int(e.NodeID),
	}
	if _, err := e.Client.Send(ctx, "Input.insertText", params); err != nil {
		return fmt.Errorf("element type: %w", err)
	}
	return nil
}

// Focus dispatches DOM.focus on the element. Upstream's focus(...)
// (line 521 in element.py) does the same thing, just routed through a
// nodeId lookup first; the public-facing CDP shape is identical.
func (e Element) Focus(ctx context.Context) error {
	params := map[string]any{
		"backendNodeId": int(e.NodeID),
	}
	if _, err := e.Client.Send(ctx, "DOM.focus", params); err != nil {
		return fmt.Errorf("element focus: %w", err)
	}
	return nil
}

// Screenshot calls Page.captureScreenshot and returns the raw image
// bytes. Real CDP returns base64; we decode here so the caller gets
// `[]byte` ready to write to disk.
//
// In s05 we don't pass a clip rectangle — the stub returns a fixed
// 8-byte PNG signature regardless. The real Element.screenshot
// computes a viewport clip from getBoundingClientRect; that lands in
// s09 (DOM service) where we have layout data.
func (e Element) Screenshot(ctx context.Context) ([]byte, error) {
	params := map[string]any{
		"format":        "png",
		"backendNodeId": int(e.NodeID),
	}
	result, err := e.Client.Send(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, fmt.Errorf("element screenshot: %w", err)
	}
	encoded, ok := result["data"].(string)
	if !ok {
		return nil, fmt.Errorf("element screenshot: missing data field in CDP response")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("element screenshot: decode base64: %w", err)
	}
	return raw, nil
}

// modifiersToBitmask is the CDP packing rule documented in
// upstream's click(): {Alt: 1, Control: 2, Meta: 4, Shift: 8}, OR'd
// together. We accept arbitrary unknown modifier strings by ignoring
// them — the resulting frame will simply have a smaller mask.
//
// Why a bitmask at all? CDP inherits this from the underlying OS-level
// input event types (Windows/X11/Cocoa all encode modifier state as a
// bitfield). It's a tiny piece of irreducible weirdness that's worth
// surfacing in the recorder so learners can see it on the wire.
func modifiersToBitmask(mods []string) int {
	mask := 0
	for _, m := range mods {
		switch m {
		case "Alt":
			mask |= 1
		case "Control":
			mask |= 2
		case "Meta":
			mask |= 4
		case "Shift":
			mask |= 8
		}
	}
	return mask
}
