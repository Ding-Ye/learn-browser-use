package main

import "sort"

// paint_order.go — drop nodes that are fully occluded by a later-painted
// (higher Z-index) element. Upstream does this in
// browser_use/dom/serializer/paint_order.py with a disjoint-rect union; our
// version is the teaching-grade simplification: pairwise containment check
// against everything painted after, in stacking order.
//
// Why even bother? Pages routinely render an open modal as `<div>` overlay
// on top of the page body. The body buttons are still in the DOM (and
// still have BBoxes that intersect the viewport), but they're physically
// unclickable — the modal eats the mouse event. An LLM that's told about
// the back-of-modal buttons will try to click them and silently fail.
// Removing them from the serialized text is the cleanest fix.

// FilterByPaintOrder marks every node that is *fully* covered by a node
// painted later (i.e. higher in z-order) as `excludedByOcclusion`. The
// definition of "painted later" follows CSS stacking order:
//
//  1. Higher ZIndex paints later.
//  2. On ZIndex tie, later document-order paints later.
//
// We do NOT do the disjoint-rect-union approach upstream uses. That one
// is needed for pages where the occluding region is the *union* of many
// small rectangles (e.g. a fixed top bar that's actually a flex of 5
// nav links). For the teaching repo we settled on pairwise containment:
// if a single later-painted node fully contains an earlier one, drop the
// earlier one. This catches the modal/overlay case which is the only
// shape we actually exercise in the fixture.
//
// `nodes` is the flat list of every node in document order — call it
// with the result of a DFS-flatten over the tree. The function mutates
// each node's `excludedByOcclusion` field in place; no return value.
func FilterByPaintOrder(nodes []*DOMNode) {
	// Build a stable paint order: sort by (ZIndex asc, docIndex asc).
	// Sorting from front-to-back means the LAST element in `sorted` is
	// painted on top.
	type indexed struct {
		node    *DOMNode
		docIdx  int
		zIndex  int
	}
	idx := make([]indexed, 0, len(nodes))
	for i, n := range nodes {
		// Only element nodes participate; text nodes inherit from their
		// parent and have no meaningful BBox of their own.
		if n.Tag == "" {
			continue
		}
		idx = append(idx, indexed{node: n, docIdx: i, zIndex: n.ZIndex})
	}
	sort.SliceStable(idx, func(i, j int) bool {
		if idx[i].zIndex != idx[j].zIndex {
			return idx[i].zIndex < idx[j].zIndex
		}
		return idx[i].docIdx < idx[j].docIdx
	})

	// Walk back-to-front: for each node, check whether any LATER node
	// fully contains it. If so, the later node will paint on top and
	// occlude us → drop us.
	for i := 0; i < len(idx); i++ {
		victim := idx[i].node
		if victim.dropped || victim.excludedByOcclusion {
			continue
		}
		vRect := rectFromBBox(victim.BBox)
		if vRect.W <= 0 || vRect.H <= 0 {
			continue // zero-area can't be occluded in a meaningful sense
		}
		// Self-occlusion guard: a node is never occluded by its own
		// descendant. We approximate descendant-ship by skipping nodes
		// that the writer would treat as inside `victim` — the cleanest
		// proxy is "shares the same BBox AND has higher docIdx" which
		// matches when one's children paint themselves on top of self.
		// We use full containment + non-equal BBox to detect the
		// genuine third-party-overlay case.
		for j := i + 1; j < len(idx); j++ {
			cover := idx[j].node
			if cover.dropped {
				continue
			}
			cRect := rectFromBBox(cover.BBox)
			if rectsEqual(cRect, vRect) {
				continue // descendants painting on themselves
			}
			if isAncestor(cover, victim) || isAncestor(victim, cover) {
				continue // siblings-of-different-subtrees only
			}
			if rectFullyContains(cRect, vRect) {
				victim.excludedByOcclusion = true
				// Also mark descendants so the writer doesn't leak the
				// occluded subtree's text into the LLM prompt. The
				// alternative — having the writer treat
				// excludedByOcclusion the same as a "stop recursion"
				// signal — would make the writer's drop-vs-skip rules
				// asymmetric for hidden / viewport / paint, which is
				// the kind of difference that bites future readers.
				propagateOccluded(victim)
				break
			}
		}
	}
}

// rectsEqual is the obvious value-equality check, broken out so the
// occlusion loop reads top-to-bottom.
func rectsEqual(a, b DOMRect) bool {
	return a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H
}

// propagateOccluded walks a subtree and marks every node as
// excludedByOcclusion. Used after an occluder fully covers a node to
// also bury its (text) children, which would otherwise still print
// because text nodes don't carry their own BBox into the pairwise check.
func propagateOccluded(n *DOMNode) {
	for _, c := range n.Children {
		c.excludedByOcclusion = true
		propagateOccluded(c)
	}
}

// isAncestor reports whether `maybeAncestor` is a transitive parent of
// `n`. Used by the occlusion loop to avoid the "div contains its own
// button, therefore the button is occluded by its parent" bug.
//
// Upstream avoids this issue implicitly because their disjoint-rect-union
// adds rects in z-order — a child painted later naturally covers the
// parent's contribution. We don't have that, so we filter the relationship
// out explicitly here. O(depth) per pair, which for ~20-node fixtures is
// trivial; the optimization story for huge DOMs is to precompute parent
// pointers once, but s08 is teaching, not production.
func isAncestor(maybeAncestor, n *DOMNode) bool {
	if maybeAncestor == nil || n == nil {
		return false
	}
	for _, c := range maybeAncestor.Children {
		if c == n {
			return true
		}
		if isAncestor(c, n) {
			return true
		}
	}
	return false
}
