package main

import (
	"fmt"
	"sort"
	"strings"
)

// serializer.go — turn a DOM tree into a flat LLM-friendly text block plus
// a stable selector map. This is the heart of s08.
//
// Upstream lives in browser_use/dom/serializer/serializer.py (~1k LOC of
// shadow DOM, AX tree, scroll hints, compound controls, etc.). We reduce
// it to the four moves that actually teach the concept:
//
//   1. drop hidden subtrees                 (display:none / visibility)
//   2. drop nodes outside the viewport      (bbox_filter.go)
//   3. drop nodes covered by something else (paint_order.go)
//   4. walk the survivors and emit text     (this file, `writeNode`)
//
// Step 4 also assigns the stable integer indices that go into the
// SelectorMap. Indices are integers (not CSS selectors) on purpose:
//
//   - they are *visible inside the LLM prompt* as `[3]<a>...`, so the
//     model's output can literally say "click 3" with zero ambiguity;
//   - they survive across snapshots if the same BackendNodeID is seen
//     again, which CSS selectors generally do not;
//   - they're short — every saved token in the prompt is a saved token
//     in the answer.
//
// One important DESIGN CHOICE different from upstream: upstream uses the
// raw `backend_node_id` as the index value, which can be a 5-digit number
// from Chromium. We use a session-local 1-based counter assigned during
// serialization. This is the same approach used by every other LLM-DOM
// project we've seen (Microsoft's Pix2Struct, Playwright's accessibility
// snapshots) — short tokens, dense, no leak of internal Chromium IDs.

// SerializedDOM is the public result of Serialize().
type SerializedDOM struct {
	LLMText     string           // the human/LLM-readable rendering
	SelectorMap map[int]DOMRect  // index → rect (where to click)
}

// Serializer is the top-level entry point. Construct one, set a viewport
// (zero = "no viewport filtering"), then call Serialize on a root node.
//
// A Serializer is stateless across calls — every Serialize() resets the
// internal counter and walks fresh. That matters because we want the
// indices to start at 1 for each prompt, not to drift up forever the way
// they would if we shared state across calls.
type Serializer struct {
	ViewportWidth  int
	ViewportHeight int
}

// Serialize runs the full pipeline:
//
//	hidden filter → viewport filter → paint-order filter → write
//
// The order matters:
//   - hidden FIRST so we don't pay viewport math on display:none subtrees;
//   - viewport BEFORE paint-order so paint-order doesn't waste pairs on
//     offscreen overlays;
//   - write LAST so it can see all the drop flags set by the upstream
//     stages.
func (s *Serializer) Serialize(root *DOMNode) SerializedDOM {
	if root == nil {
		return SerializedDOM{LLMText: "", SelectorMap: map[int]DOMRect{}}
	}

	// Stage 1: hidden filter. Walk once and propagate the "hidden"
	// signal downward — a subtree under display:none is gone regardless
	// of its own visibility, which is the rule browsers actually
	// implement and the one upstream re-implements in _create_simplified_tree.
	markHiddenSubtrees(root, false)

	// Stage 2: viewport filter.
	if s.ViewportWidth > 0 && s.ViewportHeight > 0 {
		vp := DOMRect{X: 0, Y: 0, W: s.ViewportWidth, H: s.ViewportHeight}
		FilterByViewport(root, vp)
	}

	// Stage 3: paint-order filter. Needs a flat list in document order.
	flat := flattenDocOrder(root, nil)
	FilterByPaintOrder(flat)

	// Stage 4: write. Walks the tree once; emits lines and assigns
	// indices in document order so the LLM gets a stable left-to-right
	// reading.
	w := newWriter()
	w.writeNode(root, 0)

	return SerializedDOM{
		LLMText:     strings.TrimRight(w.buf.String(), "\n"),
		SelectorMap: w.selectorMap,
	}
}

// markHiddenSubtrees walks the tree, propagating the parent's hidden
// state. A subtree inherits hidden if the parent is hidden — but a child
// can also be hidden on its own (display:none on the child). Both cases
// end with `dropped=true` on the child, which is what the writer reads.
//
// Note the design: we drop here rather than in the writer because (a)
// dropping early lets viewport/paint-order skip work, and (b) it keeps
// the writer free of "is this thing actually visible" branches.
func markHiddenSubtrees(n *DOMNode, parentHidden bool) {
	if n == nil {
		return
	}
	self := parentHidden || (n.Tag != "" && n.isHidden())
	if self {
		n.dropped = true
	}
	for _, c := range n.Children {
		markHiddenSubtrees(c, self)
	}
}

// flattenDocOrder appends every node into a flat slice in document order.
// Used by the paint-order filter, which needs to know "what node came
// later in the doc" as a tiebreaker for same-z-index overlap.
func flattenDocOrder(n *DOMNode, acc []*DOMNode) []*DOMNode {
	if n == nil {
		return acc
	}
	acc = append(acc, n)
	for _, c := range n.Children {
		acc = flattenDocOrder(c, acc)
	}
	return acc
}

// ---- writer ----

// writer is the tree → text builder. It accumulates output into `buf`,
// hands out interactive indices via `nextIndex`, and records each
// interactive node's rect into `selectorMap`.
//
// One subtle rule about indices: we assign an index only if BOTH
//   (a) the node says Interactive=true, AND
//   (b) no *ancestor* has already been assigned an index.
// That's the "nested interactive merged" rule — see writeNode for the
// details and the doc for the why.
type writer struct {
	buf            strings.Builder
	selectorMap    map[int]DOMRect
	nextIndex      int
	ancestorIndexed bool // shadow-stack while walking; written through pointer
}

func newWriter() *writer {
	return &writer{
		selectorMap: map[int]DOMRect{},
		nextIndex:   1,
	}
}

// writeNode emits the node and recurses into its children. The depth is
// used purely for indentation; we use 2 spaces per level (upstream uses
// tabs, but spaces survive copy-paste through terminals more reliably).
//
// The non-obvious rule, copied from upstream serializer.py L939:
// "structural" wrappers (a non-interactive div/main/header/html/body) do
// NOT print their tag — they only forward their children at the current
// depth. Only nodes the LLM might *actually act on* (interactive, plus
// IFRAME/FRAME upstream, plus scrollable upstream) get printed as a
// tag line and bump the depth for their kids. The result is a flat,
// dense rendering that's optimized for prompt-token efficiency.
//
// Returns nothing — output is accumulated in w.buf.
func (w *writer) writeNode(n *DOMNode, depth int) {
	if n == nil {
		return
	}
	if n.dropped || n.excludedByOcclusion {
		// Even if WE are dropped, walk children — see upstream's
		// excluded_by_parent handling (serializer.py L889). A wrapper
		// div getting filtered shouldn't bury an interactive button
		// that survived independently. The viewport/hidden filters
		// already propagated their drop downward in cases where the
		// whole subtree should vanish, so any survivor below is meant
		// to render.
		for _, c := range n.Children {
			w.writeNode(c, depth)
		}
		return
	}

	// Text nodes: emit content verbatim at the current depth.
	if n.Tag == "" {
		t := strings.TrimSpace(n.Text)
		if t == "" {
			return
		}
		fmt.Fprintf(&w.buf, "%s%s\n", indent(depth), t)
		return
	}

	// Decide whether THIS element prints a tag-line.
	//   - Interactive elements always do (and get an index, unless an
	//     ancestor already has one — the "nested interactive merged"
	//     rule).
	//   - Other elements do not print a wrapper; they just forward
	//     their children at the same depth.
	if !n.Interactive {
		for _, c := range n.Children {
			w.writeNode(c, depth)
		}
		return
	}

	assignedHere := false
	if !w.ancestorIndexed {
		idx := w.nextIndex
		w.nextIndex++
		w.selectorMap[idx] = rectFromBBox(n.BBox)
		fmt.Fprintf(&w.buf, "%s[%d]<%s%s />\n", indent(depth), idx, n.Tag, formatAttrs(n))
		assignedHere = true
	} else {
		// Interactive but suppressed by an ancestor's index.
		fmt.Fprintf(&w.buf, "%s<%s%s />\n", indent(depth), n.Tag, formatAttrs(n))
	}

	prev := w.ancestorIndexed
	if assignedHere {
		w.ancestorIndexed = true
	}
	for _, c := range n.Children {
		w.writeNode(c, depth+1)
	}
	w.ancestorIndexed = prev
}

// indent returns `2*depth` spaces. Pulled out because it's used 3x and
// the per-call cost of strings.Repeat is real on large trees.
func indent(d int) string {
	if d <= 0 {
		return ""
	}
	return strings.Repeat("  ", d)
}

// formatAttrs renders the attribute bag in a deterministic, LLM-friendly
// way. Two non-obvious design choices in this function:
//
//   1. Sort keys. Map iteration order in Go is randomized, and our test
//      golden file would flake every other run if we didn't sort. The
//      sort also makes diffs across runs human-readable.
//   2. Skip the `style` attribute. Long inline styles bloat the prompt
//      with information the LLM cannot act on (and that we already used
//      ourselves to compute visibility). Upstream filters by an
//      include-list (DEFAULT_INCLUDE_ATTRIBUTES, ~50 entries); we use the
//      simpler skip-list approach because our fixtures are small.
func formatAttrs(n *DOMNode) string {
	if len(n.Attributes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(n.Attributes))
	for k := range n.Attributes {
		if k == "style" || k == "hidden" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, " %s=%q", k, n.Attributes[k])
	}
	return sb.String()
}
