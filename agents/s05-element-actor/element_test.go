package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// pngSignature is the canonical 8-byte PNG magic header. Every valid
// PNG file starts with these bytes; the stub Page.captureScreenshot
// returns exactly this and nothing else.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// findFrame returns the first frame whose Method matches `method`,
// or nil if none. We use it everywhere we want a friendlier error
// message than "Frames[0].Method = ...".
func findFrame(frames []Frame, method string) *Frame {
	for i := range frames {
		if frames[i].Method == method {
			return &frames[i]
		}
	}
	return nil
}

// TestClickRecordsMouseEvent — a click must emit the three-step
// mouseMoved → mousePressed → mouseReleased sequence, each carrying
// our BackendNodeID. This is the load-bearing structural test: it
// confirms the CDP frame stream's shape, not just that Send was
// called.
func TestClickRecordsMouseEvent(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 42}

	if err := el.Click(context.Background(), ClickOptions{}); err != nil {
		t.Fatalf("Click returned error: %v", err)
	}

	if got := len(client.Frames); got != 3 {
		t.Fatalf("expected 3 frames (move/press/release), got %d", got)
	}

	wantTypes := []string{"mouseMoved", "mousePressed", "mouseReleased"}
	for i, want := range wantTypes {
		f := client.Frames[i]
		if f.Method != "Input.dispatchMouseEvent" {
			t.Errorf("frame[%d].Method = %q, want Input.dispatchMouseEvent", i, f.Method)
		}
		if f.Params["type"] != want {
			t.Errorf("frame[%d].params.type = %v, want %q", i, f.Params["type"], want)
		}
		if f.Params["backendNodeId"] != 42 {
			t.Errorf("frame[%d].params.backendNodeId = %v, want 42", i, f.Params["backendNodeId"])
		}
	}

	// Press/release frames must include button + clickCount (default
	// normalised), while mouseMoved must not (matching real CDP).
	press := client.Frames[1]
	if press.Params["button"] != "left" {
		t.Errorf("default button = %v, want \"left\"", press.Params["button"])
	}
	if press.Params["clickCount"] != 1 {
		t.Errorf("default clickCount = %v, want 1", press.Params["clickCount"])
	}
	if _, ok := client.Frames[0].Params["button"]; ok {
		t.Errorf("mouseMoved frame should not carry button field")
	}
}

// TestTypeEncodesUnicode — passing a string with non-ASCII characters
// and an emoji must put the exact UTF-8 bytes into the recorded
// `Input.insertText` frame. This is the test that proves we didn't
// accidentally re-encode through latin-1 or strip surrogates.
func TestTypeEncodesUnicode(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 7}

	const payload = "café 你好 👋"
	if err := el.Type(context.Background(), payload); err != nil {
		t.Fatalf("Type returned error: %v", err)
	}

	f := findFrame(client.Frames, "Input.insertText")
	if f == nil {
		t.Fatalf("no Input.insertText frame recorded; got %d frames", len(client.Frames))
	}

	gotText, ok := f.Params["text"].(string)
	if !ok {
		t.Fatalf("frame.params.text not a string: %T", f.Params["text"])
	}
	if gotText != payload {
		t.Errorf("frame text = %q, want %q", gotText, payload)
	}

	// Round-trip check: the in-memory string should equal the same
	// bytes we'd see on the wire when JSON-encoded. We assert UTF-8
	// validity by re-decoding; if anything mangled the codepoints
	// (e.g. NFC normalisation), the byte count would differ.
	wantBytes := []byte(payload)
	if !bytes.Equal([]byte(gotText), wantBytes) {
		t.Errorf("UTF-8 bytes diverged: got % x want % x", []byte(gotText), wantBytes)
	}
	if !strings.Contains(gotText, "👋") {
		t.Errorf("emoji round-trip lost the 👋 codepoint: got %q", gotText)
	}

	if f.Params["backendNodeId"] != 7 {
		t.Errorf("frame backendNodeId = %v, want 7", f.Params["backendNodeId"])
	}
}

// TestModifierKeysPassed — passing ClickOptions.Modifiers must reach
// the pressed/released frames as a CDP-shape bitmask. Shift=8,
// Control=2, so Shift+Control = 10.
func TestModifierKeysPassed(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 99}

	opts := ClickOptions{
		Button:     "left",
		ClickCount: 2, // double-click; carries through unchanged
		Modifiers:  []string{"Shift", "Control"},
	}
	if err := el.Click(context.Background(), opts); err != nil {
		t.Fatalf("Click error: %v", err)
	}

	// Find the mousePressed frame — that's where the modifier mask
	// actually matters for a click. Both pressed and released should
	// carry the same mask in real CDP, but the assertion belongs on
	// pressed because that's the event handlers will react to.
	var pressed *Frame
	for i := range client.Frames {
		if client.Frames[i].Params["type"] == "mousePressed" {
			pressed = &client.Frames[i]
			break
		}
	}
	if pressed == nil {
		t.Fatalf("no mousePressed frame found")
	}

	gotMask, ok := pressed.Params["modifiers"].(int)
	if !ok {
		t.Fatalf("modifiers not int: %T", pressed.Params["modifiers"])
	}
	// Shift=8 | Control=2 = 10.
	if gotMask != 10 {
		t.Errorf("modifier mask = %d, want 10 (Shift|Control)", gotMask)
	}

	if pressed.Params["clickCount"] != 2 {
		t.Errorf("clickCount = %v, want 2", pressed.Params["clickCount"])
	}
}

// TestScreenshotReturnsDummyPNG — Screenshot must return >=8 bytes
// starting with the PNG signature. We don't assert exact length
// because future stub tweaks may pad bytes; the contract is "the
// header is real".
func TestScreenshotReturnsDummyPNG(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 17}

	img, err := el.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot returned error: %v", err)
	}
	if len(img) < 8 {
		t.Fatalf("Screenshot returned %d bytes, want >= 8", len(img))
	}
	if !bytes.Equal(img[:8], pngSignature) {
		t.Errorf("PNG header mismatch: got % x want % x", img[:8], pngSignature)
	}

	// And the recorder should have seen the capture call.
	f := findFrame(client.Frames, "Page.captureScreenshot")
	if f == nil {
		t.Fatalf("no Page.captureScreenshot frame recorded")
	}
	if f.Params["format"] != "png" {
		t.Errorf("capture format = %v, want \"png\"", f.Params["format"])
	}
	if f.Params["backendNodeId"] != 17 {
		t.Errorf("capture backendNodeId = %v, want 17", f.Params["backendNodeId"])
	}
}

// TestFocusDispatchesDOMFocus — Focus must record exactly one
// DOM.focus frame carrying the backendNodeId.
func TestFocusDispatchesDOMFocus(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 1234}

	if err := el.Focus(context.Background()); err != nil {
		t.Fatalf("Focus error: %v", err)
	}
	if got := len(client.Frames); got != 1 {
		t.Fatalf("expected 1 frame, got %d", got)
	}
	f := client.Frames[0]
	if f.Method != "DOM.focus" {
		t.Errorf("method = %q, want DOM.focus", f.Method)
	}
	if f.Params["backendNodeId"] != 1234 {
		t.Errorf("backendNodeId = %v, want 1234", f.Params["backendNodeId"])
	}
}

// TestEmptyClickOptionsNormalises — an unset ClickOptions{} must
// behave as a left single-click with no modifiers, so the shortest
// call site `el.Click(ctx, ClickOptions{})` doesn't surprise anyone.
// This is also documentation: the contract of zero-value defaults
// lives here so future refactors can't silently change it.
func TestEmptyClickOptionsNormalises(t *testing.T) {
	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 5}

	if err := el.Click(context.Background(), ClickOptions{}); err != nil {
		t.Fatalf("Click error: %v", err)
	}

	pressed := client.Frames[1]
	if pressed.Params["button"] != "left" {
		t.Errorf("zero-value button = %v, want \"left\"", pressed.Params["button"])
	}
	if pressed.Params["clickCount"] != 1 {
		t.Errorf("zero-value clickCount = %v, want 1", pressed.Params["clickCount"])
	}
	if pressed.Params["modifiers"] != 0 {
		t.Errorf("zero-value modifiers mask = %v, want 0", pressed.Params["modifiers"])
	}
}
