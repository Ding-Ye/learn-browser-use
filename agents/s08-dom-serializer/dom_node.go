package main

// dom_node.go — minimal Chromium-shape DOM data model for the serializer.
//
// We do NOT try to mirror every field on upstream's `EnhancedDOMTreeNode`
// (browser_use/dom/views.py is ~700 lines on its own). Instead we keep just
// the fields the serializer actually reads, plus what learners can produce
// by hand in a JSON fixture:
//
//   - BackendNodeID : the integer the LLM will eventually emit back at us
//                     (upstream calls it backend_node_id; it survives across
//                     DOM mutations whereas a CSS selector would not).
//   - Tag, Text     : `<tag>` for elements, raw text content for #text nodes
//                     (Tag == "" means "text node"; mutually exclusive).
//   - Attributes    : the HTML attribute bag. Used both for the serialized
//                     output AND for one signal — `style: display:none` —
//                     that drops the node before it ever reaches the LLM.
//   - Children      : the tree.
//   - BBox          : [x, y, w, h] in CSS pixels. Used by the viewport
//                     filter and by the paint-order overlap check.
//   - Visible       : the layout-engine signal — false means CSS hid it
//                     (display:none, visibility:hidden, opacity:0, …).
//                     We keep this an explicit boolean so the fixture can
//                     test "interactive but invisible" cases without us
//                     having to parse a real CSS cascade.
//   - Interactive   : "the LLM should be allowed to click this". We do
//                     NOT compute this from tag alone — upstream's
//                     ClickableElementDetector is 250 lines and looks at
//                     onclick handlers, ARIA roles, search-class heuristics,
//                     etc. A fixture-authored boolean lets the test name
//                     the contract instead of re-implementing 250 lines.
//   - ZIndex        : stacking context. Higher wins. Paint order is the
//                     *order in which Chromium painted things*, and z-index
//                     is the cleanest user-facing proxy in our fixture.
//                     Tiebreaker: equal Z falls back to document order
//                     (later wins), which is what the CSS spec says when
//                     stacking-context positions tie.
//
// What we deliberately omit vs upstream:
//   - paint_order field per node (we derive it from ZIndex + doc order)
//   - shadow DOM (no DOCUMENT_FRAGMENT node type)
//   - iframes (no content_document)
//   - the accessibility tree (no ax_node)
//   - is_actually_scrollable, has_js_click_listener, snapshot.computed_styles, …
// All of those are explicit deliberate omissions called out in the doc.

// DOMNode is one node in the in-memory DOM tree. The shape matches our
// `testdata/snapshot.json` fixture 1:1 — encoding/json populates this
// struct directly. See snapshot.json for an annotated example.
type DOMNode struct {
	BackendNodeID int               `json:"backendNodeId"`
	Tag           string            `json:"tag,omitempty"`
	Text          string            `json:"text,omitempty"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Children      []*DOMNode        `json:"children,omitempty"`
	BBox          [4]int            `json:"bbox"` // x, y, w, h
	Visible       bool              `json:"visible"`
	Interactive   bool              `json:"interactive,omitempty"`
	ZIndex        int               `json:"zIndex,omitempty"`

	// Internal flags set by the filter pipeline. Not persisted in JSON;
	// these are how the filters communicate with the writer.
	excludedByOcclusion bool // set by FilterByPaintOrder
	dropped             bool // set by FilterByViewport / hidden filter
}

// DOMRect is x/y/w/h in CSS pixels. We use this as the selector-map value
// type so the LLM-side caller can later turn an index back into "where on
// the page do I click". Upstream calls the same thing `DOMRect`; we keep
// the name aligned so cross-session readers don't have to translate.
type DOMRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// rectFromBBox is the canonical [x,y,w,h] → DOMRect conversion. Centralized
// so changes to the BBox layout (e.g. if someone wants x1/y1/x2/y2 later)
// stay in one place.
func rectFromBBox(b [4]int) DOMRect {
	return DOMRect{X: b[0], Y: b[1], W: b[2], H: b[3]}
}

// isHidden reports whether the node is hidden by CSS in a way that means
// the LLM should never see it. We treat three signals as equivalent:
//   - !Visible              (the layout-engine signal already computed)
//   - style: display: none  (most common; the most decisive)
//   - hidden="true" attr    (HTML5 boolean attribute)
//
// We do NOT treat opacity:0 as hidden here — upstream keeps opacity:0
// nodes for file-input edge cases. For our fixture-driven teaching that
// distinction doesn't matter, but the door is left open.
func (n *DOMNode) isHidden() bool {
	if !n.Visible {
		return true
	}
	if style, ok := n.Attributes["style"]; ok {
		// Cheap substring check is enough for the fixture. Real upstream
		// parses computed_styles from the snapshot, but our fixture authors
		// the style string by hand so "display:none" / "display: none" both
		// appear verbatim.
		if containsCI(style, "display:none") || containsCI(style, "display: none") {
			return true
		}
	}
	if h, ok := n.Attributes["hidden"]; ok && h != "" && h != "false" {
		return true
	}
	return false
}

// containsCI is a case-insensitive substring check that avoids pulling in
// `strings` just for ToLower (we'd allocate a copy). It's a 6-line trick
// that keeps the file dependency-free and fast on the small attribute
// strings we see in practice.
func containsCI(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	// Fast path: equal length, just compare.
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
