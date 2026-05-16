package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadSnapshot is a tiny helper used by most tests to read the
// hand-crafted Chromium-shape fixture. We re-parse it per test rather
// than share a global because tests mutate the tree via the
// filter pipeline — sharing would cross-pollute drop flags.
func loadSnapshot(t *testing.T) *DOMNode {
	t.Helper()
	raw, err := os.ReadFile("testdata/snapshot.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var root DOMNode
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	return &root
}

// TestSerializeMatchesGolden pins the LLM-facing text to
// testdata/expected.txt. If you intentionally change the serializer's
// output format, copy the new output back into that file and commit
// both — the diff is the documentation of the format change.
func TestSerializeMatchesGolden(t *testing.T) {
	root := loadSnapshot(t)
	want, err := os.ReadFile("testdata/expected.txt")
	if err != nil {
		t.Fatalf("read expected: %v", err)
	}
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	got := ser.Serialize(root).LLMText
	wantStr := strings.TrimRight(string(want), "\n")
	if got != wantStr {
		t.Fatalf("LLMText mismatch\n--- want ---\n%s\n--- got ---\n%s\n", wantStr, got)
	}
}

// TestSelectorMapCoversClickable verifies the SelectorMap has one entry
// per top-level clickable element (the outer <a>, the standalone
// <button>, and the <input>). The inner <button> nested inside the <a>
// is deliberately suppressed (TestNestedInteractiveMerged) and the
// hidden/off-viewport buttons must not appear either.
func TestSelectorMapCoversClickable(t *testing.T) {
	root := loadSnapshot(t)
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(root)
	if got := len(out.SelectorMap); got != 3 {
		t.Fatalf("SelectorMap size: want 3, got %d (%v)", got, out.SelectorMap)
	}
	// Indices must be 1-based contiguous.
	for i := 1; i <= 3; i++ {
		if _, ok := out.SelectorMap[i]; !ok {
			t.Fatalf("missing selector index %d in %v", i, out.SelectorMap)
		}
	}
	// Every rect must have nonzero area — defends against the
	// "interactive=true but BBox forgot to be set" failure mode.
	for k, r := range out.SelectorMap {
		if r.W <= 0 || r.H <= 0 {
			t.Errorf("selector [%d] has zero-area rect %+v", k, r)
		}
	}
}

// TestHiddenElementsDropped verifies that a <button> inside a
// display:none div never reaches the LLM — neither as a tag nor as the
// text label inside it. Hidden propagation is the rule the browser
// itself implements, so the serializer matching that is what makes the
// LLM see "the page" instead of "the source code".
func TestHiddenElementsDropped(t *testing.T) {
	root := loadSnapshot(t)
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(root)
	if strings.Contains(out.LLMText, "Hidden Button") {
		t.Errorf("hidden button leaked into output: %q", out.LLMText)
	}
	if strings.Contains(out.LLMText, `id="secret"`) {
		t.Errorf("hidden button attributes leaked: %q", out.LLMText)
	}
}

// TestNestedInteractiveMerged verifies the "button inside <a>" rule:
// when an interactive element is a descendant of another interactive
// element, only the OUTER one gets an index. Otherwise the LLM would
// have two clickable handles for one logical button and could choose
// the inner one and trigger duplicate events.
func TestNestedInteractiveMerged(t *testing.T) {
	root := loadSnapshot(t)
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(root)
	// Exactly one [N]<a ... > line should appear (the outer anchor).
	if strings.Count(out.LLMText, `[1]<a`) != 1 {
		t.Errorf("expected exactly one indexed anchor, got: %s", out.LLMText)
	}
	// The inner button must be rendered WITHOUT an index — we look for
	// the unindexed form. (A `[N]<button type="button"` would be a fail.)
	if strings.Contains(out.LLMText, `[`) && strings.Contains(out.LLMText, `]<button type="button"`) {
		t.Errorf("inner button was double-indexed: %s", out.LLMText)
	}
	// Sanity: the inner button's text label still appears.
	if !strings.Contains(out.LLMText, "Go") {
		t.Errorf("inner button text 'Go' should still render: %s", out.LLMText)
	}
}

// TestBBoxFilterRemovesOffscreen verifies the <a> with bbox below the
// viewport (y=1900, viewport ends at 800) is excluded from BOTH the
// LLMText and the SelectorMap. Off-viewport elements can't be clicked
// without scrolling first; surfacing them invites the LLM to issue
// invalid actions.
func TestBBoxFilterRemovesOffscreen(t *testing.T) {
	root := loadSnapshot(t)
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(root)
	if strings.Contains(out.LLMText, "Below the fold") {
		t.Errorf("off-viewport text leaked: %q", out.LLMText)
	}
	if strings.Contains(out.LLMText, `href="/faraway"`) {
		t.Errorf("off-viewport anchor leaked: %q", out.LLMText)
	}
	// Cross-check: no rect in the SelectorMap matches the off-viewport
	// y-coordinate (1900). If one does, we accidentally indexed it.
	for k, r := range out.SelectorMap {
		if r.Y >= 1000 {
			t.Errorf("selector [%d] points off-viewport: %+v", k, r)
		}
	}
}

// TestPaintOrderOcclusionDropsBehind is a bonus test that pins the
// paint-order behavior: the modal at zIndex=10 fully covers div#behind
// at zIndex=0, so #behind must not appear in the output. This test is
// not in the original 5-required list but the algorithm is one of the
// two non-trivial filters, so it earns its own check.
func TestPaintOrderOcclusionDropsBehind(t *testing.T) {
	root := loadSnapshot(t)
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(root)
	if strings.Contains(out.LLMText, "Behind modal") {
		t.Errorf("occluded text leaked: %q", out.LLMText)
	}
	if strings.Contains(out.LLMText, `id="behind"`) {
		t.Errorf("occluded div attributes leaked: %q", out.LLMText)
	}
	// The modal itself should still print (it's the occluder, not the
	// occluded), and its content too.
	if !strings.Contains(out.LLMText, "MODAL CONTENT") {
		t.Errorf("modal content missing — paint filter over-pruned: %q", out.LLMText)
	}
}

// TestEmptyTreeReturnsEmpty defends the nil-root branch. Tiny but real:
// upstream's DOMService can return None on a captureSnapshot timeout,
// and the agent loop in s12 will call into our serializer for that case.
func TestEmptyTreeReturnsEmpty(t *testing.T) {
	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(nil)
	if out.LLMText != "" {
		t.Errorf("expected empty LLMText, got %q", out.LLMText)
	}
	if len(out.SelectorMap) != 0 {
		t.Errorf("expected empty SelectorMap, got %v", out.SelectorMap)
	}
}
