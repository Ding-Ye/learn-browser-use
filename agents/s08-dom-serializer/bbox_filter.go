package main

// bbox_filter.go — drop nodes whose bounding box is fully outside the
// viewport, so the LLM never sees "Privacy Policy" links that live 4000px
// below the fold. Upstream calls the same step `_apply_bounding_box_filtering`
// in browser_use/dom/serializer/serializer.py (L729). Our version is much
// smaller because we don't propagate parent bounds — see the doc for why.
//
// The contract is intentionally conservative: a node is dropped ONLY when
// it is *entirely* outside the viewport. If even one pixel intersects, the
// node stays. This is the right default for an LLM-facing serializer
// because the cost of false-positive drops (the LLM is told the button
// doesn't exist) is much higher than the cost of false-positive keeps
// (the LLM gets one extra "View Cart" line in its prompt).

// FilterByViewport recursively marks every node whose BBox is fully
// outside `viewport` as dropped. The mark, not the removal, is what the
// writer then checks — keeping the tree shape intact lets paint-order
// filtering still reason about overlaps after this step runs.
//
// We return the same root pointer (the operation is in-place on the
// internal `dropped` flag); the return value is a convenience for
// pipeline-style call sites.
func FilterByViewport(node *DOMNode, viewport DOMRect) *DOMNode {
	if node == nil {
		return nil
	}
	filterViewportInPlace(node, viewport)
	return node
}

func filterViewportInPlace(node *DOMNode, vp DOMRect) {
	filterViewportRec(node, vp, false)
}

func filterViewportRec(node *DOMNode, vp DOMRect, parentDropped bool) {
	// Text nodes have no BBox of their own — they inherit visibility
	// from their parent element. We drop them only by inheritance.
	dropSelf := parentDropped
	if !dropSelf && node.Tag != "" {
		if !rectIntersects(rectFromBBox(node.BBox), vp) {
			dropSelf = true
		}
	}
	if dropSelf {
		node.dropped = true
	}
	for _, c := range node.Children {
		filterViewportRec(c, vp, dropSelf)
	}
}

// rectIntersects is the standard axis-aligned bounding-box intersection
// test. Zero-area rects (W==0 or H==0) are treated as non-intersecting
// because such a rect has no pixels for the LLM to "see" anyway.
//
// Returns true if there is any overlap (even a single pixel).
func rectIntersects(a, b DOMRect) bool {
	if a.W <= 0 || a.H <= 0 || b.W <= 0 || b.H <= 0 {
		return false
	}
	ax2 := a.X + a.W
	ay2 := a.Y + a.H
	bx2 := b.X + b.W
	by2 := b.Y + b.H
	// Non-overlap on either axis → no intersection.
	if ax2 <= b.X || bx2 <= a.X {
		return false
	}
	if ay2 <= b.Y || by2 <= a.Y {
		return false
	}
	return true
}

// rectFullyContains reports whether `outer` fully contains `inner`. Used
// by the paint-order filter, not the viewport filter — but co-located here
// because it's a sibling AABB primitive and putting it next to its peer
// keeps both legible.
func rectFullyContains(outer, inner DOMRect) bool {
	if inner.W <= 0 || inner.H <= 0 {
		return false
	}
	return inner.X >= outer.X &&
		inner.Y >= outer.Y &&
		inner.X+inner.W <= outer.X+outer.W &&
		inner.Y+inner.H <= outer.Y+outer.H
}
