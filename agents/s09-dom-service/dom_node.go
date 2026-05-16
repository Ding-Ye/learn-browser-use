package main

// dom_node.go is the local, deliberately tiny re-declaration of the
// DOM node type taught in s08. The rule across learn-browser-use is
// "no cross-session imports" — each session must build with
// `go build ./...` from its own directory regardless of what its
// neighbours do. So we copy the *shape* of s08's DOMNode but keep
// only the fields the service actually consults.
//
// Compared to s08 we drop:
//   - Attributes        (the service test doesn't care about ARIA / data-* attrs)
//   - Interactive       (s09 treats every visible leaf as a candidate)
//   - paint-order index (s08 owned that filter; here the snapshot is
//                        already paint-ordered by the upstream stub)
//
// The trade-off is honest: in production the service consumes the
// full enhanced node from cdp_use, then asks the serializer (s08) to
// turn it into LLM text. Here both producer and consumer share this
// shrunk struct so the test fixtures stay readable.
//
// Upstream analog: `EnhancedDOMTreeNode` in `browser_use/dom/views.py`
// — ~80 fields covering accessibility, snapshot bounds, computed
// styles, shadow roots, iframe contents. We need 6 fields to teach
// "the service caches and invalidates serialized DOM".

// DOMNode is the minimal tree node the service walks. Every field is
// public so test fixtures and the serializer can read them without
// going through accessor methods — there are no invariants to defend
// on the producer side, the tree is built fresh per snapshot.
type DOMNode struct {
	// BackendNodeID is the CDP-stable element identifier; in production
	// this is the only thing the actor needs to dispatch a click. The
	// service preserves it from snapshot to serialized text so the LLM
	// can refer to elements by index and the actor can convert back to
	// BackendNodeID via the selector map.
	BackendNodeID int

	// Tag and Text are the two pieces of human-readable surface the
	// LLM actually reads. Tag is the lowercase HTML element name
	// ("button", "a", "input"); Text is the visible label
	// (textContent for elements, value for inputs, aria-label as
	// fallback).
	Tag  string
	Text string

	// Children carries the recursive tree shape. Iframes upstream get
	// a special "content_document" pointer; for s09's teaching scope
	// we collapse that into ordinary Children — the iframe-depth test
	// just needs nesting deep enough to exercise the pruning loop.
	Children []*DOMNode

	// BBox is [x, y, w, h] in CSS pixels relative to the viewport.
	// The service uses h * w to compute the area and compares it to
	// ViewportThreshold. Upstream uses a richer DOMRect plus
	// paint-order index; we keep the shape minimal because the
	// spotlight here is on the service, not on the geometry.
	BBox [4]int

	// Visible mirrors upstream's `is_visible` — already the AND of
	// computed-style display/visibility/opacity checks. The service
	// trusts this field; in production the snapshot producer (CDP
	// DOMSnapshot.captureSnapshot + computed styles) is what sets it.
	Visible bool
}
