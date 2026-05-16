package main

import (
	"fmt"
	"strings"
)

// serializer.go is the deliberately-tiny serializer for s09. The
// *real* serializer lives in s08 — paint-order filtering, bbox
// propagation through interactive parents, accessibility-tree merge,
// ~600 LOC across `serializer.go` + `bbox_filter.go` + `paint_order.go`.
//
// We *don't* re-implement that here. The spotlight of s09 is on the
// SERVICE — caching, invalidation, iframe-depth limits, viewport
// thresholds. The serializer just needs to produce a stable string
// the service can stick in the cache and the test can assert against.
//
// So this file is ~50 lines of plain tree walk:
//
//   1. Pre-order traversal of the tree.
//   2. Skip non-visible nodes.
//   3. Assign each visible node an index (the order in which we visit it).
//   4. Emit one line per visible node: "[idx] <tag> text".
//
// The output is what the service caches; the selector map maps the
// indexes back to BackendNodeID + BBox so the actor (s05) can look
// up "click [3]" and find the right DOM element.

// SerializedState is what DOMService.Get returns. The shape mirrors
// upstream's SerializedDOMState (`browser_use/dom/views.py`), with
// LLMText for the prompt and SelectorMap for the action-dispatch lookup.
type SerializedState struct {
	// LLMText is what the agent loop drops into the user-turn prompt.
	// One line per visible interactive node, with a leading index the
	// LLM will use when it says "click [3]".
	LLMText string

	// SelectorMap maps the index the LLM sees to a tuple of
	// (BackendNodeID, BBox). Upstream's map value is DOMRect; we add
	// BackendNodeID because in s09 the actor is going to need both —
	// rect for click coordinates, ID for cross-snapshot stability.
	SelectorMap map[int]SelectorEntry
}

// SelectorEntry is one row of the selector map. The actor takes the
// LLM's chosen index, looks up this entry, and decides whether to
// dispatch the click via BackendNodeID (preferred, race-free) or via
// raw coordinates from BBox (fallback if the DOM mutated since the
// snapshot was taken).
type SelectorEntry struct {
	BackendNodeID int
	BBox          [4]int
}

// Serialize walks the (already-filtered) tree and produces an
// LLM-friendly text + selector map. The walk is intentionally
// stateless w.r.t. anything outside the tree — no clock, no
// configuration, no I/O. That's what makes it cheap to call from the
// cache miss path: the only inputs are the tree and the iframe-depth
// has-already-been-pruned guarantee.
//
// We accept a `nil` root and emit "(no DOM available)" so the test
// fixture for the "fresh page" case is one printable string instead
// of a special-cased empty result.
func Serialize(root *DOMNode) *SerializedState {
	state := &SerializedState{
		SelectorMap: make(map[int]SelectorEntry),
	}
	if root == nil {
		state.LLMText = "(no DOM available)"
		return state
	}

	var lines []string
	idx := 0

	var walk func(n *DOMNode)
	walk = func(n *DOMNode) {
		if n == nil {
			return
		}
		if n.Visible {
			// Only visible nodes get an index + a text line. The
			// indentation is dropped on purpose — upstream emits a
			// flat list because tree depth is information the LLM
			// rarely needs and tokens are expensive.
			lines = append(lines, fmt.Sprintf("[%d] <%s> %s",
				idx, n.Tag, strings.TrimSpace(n.Text)))
			state.SelectorMap[idx] = SelectorEntry{
				BackendNodeID: n.BackendNodeID,
				BBox:          n.BBox,
			}
			idx++
		}
		// Continue regardless of visibility — an invisible parent
		// might have visible children (e.g. an opacity:0 wrapper
		// around a visible link). The filter is per-node, not
		// per-subtree.
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)

	state.LLMText = strings.Join(lines, "\n")
	return state
}
