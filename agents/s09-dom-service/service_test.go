package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// service_test.go covers the five required behaviors of DOMService:
//
//   1. Snapshot is cached on the first call; the second Get is a hit.
//   2. NavigationEvent invalidates the cache so the next Get re-fetches.
//   3. The TTL expires on its own (no event needed) and the next Get re-fetches.
//   4. IframeMaxDepth prunes deep subtrees.
//   5. ViewportThreshold drops oversized nodes.
//
// Each test builds its own service so we don't share state — the
// cache + bus subscription bookkeeping is exactly what we want to
// test, and shared state would mask bugs.

// TestSnapshotIsCached — two consecutive Get calls must produce one
// snapshot invocation. This is the basic happy path; if it fails,
// nothing else works.
func TestSnapshotIsCached(t *testing.T) {
	snap, calls := NewStubSnapshot()
	svc := NewDOMService(NewEventBus(), snap, NewCache(time.Minute))
	svc.CurrentURL = "https://a.example.com"

	if _, err := svc.Get(context.Background()); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := svc.Get(context.Background()); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if *calls != 1 {
		t.Errorf("snapshot called %d times, want 1 (the second Get should hit the cache)", *calls)
	}
}

// TestNavigationInvalidates — emit NavigationEvent on the bus,
// expect the next Get to fetch a fresh snapshot. We also assert the
// returned LLMText changes (page A → page B), because a service that
// re-fetches but returns stale data still fails the contract.
func TestNavigationInvalidates(t *testing.T) {
	snap, calls := NewStubSnapshot()
	bus := NewEventBus()
	svc := NewDOMService(bus, snap, NewCache(time.Minute))
	svc.CurrentURL = "https://a.example.com"
	ctx := context.Background()

	first, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if !strings.Contains(first.LLMText, "submit") {
		t.Fatalf("page A text should mention 'submit', got %q", first.LLMText)
	}

	// Navigate — update URL then fire the bus event so subscribers
	// invalidate the cache.
	svc.CurrentURL = "https://b.example.com"
	if err := bus.Emit(ctx, NavigationEvent{URL: "https://b.example.com"}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	second, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if *calls != 2 {
		t.Errorf("snapshot called %d times, want 2 (post-navigation Get should miss the cache)", *calls)
	}
	if !strings.Contains(second.LLMText, "home") {
		t.Errorf("page B text should mention 'home', got %q", second.LLMText)
	}
	if first.LLMText == second.LLMText {
		t.Errorf("post-navigation text should differ from pre-navigation: both = %q", first.LLMText)
	}
}

// TestCacheTTLExpires — the TTL safety net fires on its own without
// any event. We inject a synthetic clock through the cache's `now`
// field so the test doesn't have to time.Sleep through the TTL.
func TestCacheTTLExpires(t *testing.T) {
	snap, calls := NewStubSnapshot()
	cache := NewCache(50 * time.Millisecond)

	// Override the clock: a single now value, advanced manually.
	clock := time.Now()
	cache.now = func() time.Time { return clock }

	svc := NewDOMService(NewEventBus(), snap, cache)
	svc.CurrentURL = "https://a.example.com"
	ctx := context.Background()

	if _, err := svc.Get(ctx); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// Same clock — second Get should hit.
	if _, err := svc.Get(ctx); err != nil {
		t.Fatalf("second Get (pre-expiry): %v", err)
	}
	if *calls != 1 {
		t.Errorf("after two Gets at same clock, snapshot called %d times, want 1", *calls)
	}

	// Advance the clock past the TTL — third Get should miss.
	clock = clock.Add(100 * time.Millisecond)
	if _, err := svc.Get(ctx); err != nil {
		t.Fatalf("third Get (post-expiry): %v", err)
	}
	if *calls != 2 {
		t.Errorf("after TTL expiry, snapshot called %d times, want 2", *calls)
	}
}

// TestIframeMaxDepthEnforced — pass a deeply nested tree with
// IframeMaxDepth=2 and assert the depth-3 node has lost its child.
// This is the "filter is applied before serialize" assertion.
func TestIframeMaxDepthEnforced(t *testing.T) {
	// Build a 4-level chain: root → mid1 → mid2 → leaf
	leaf := &DOMNode{BackendNodeID: 99, Tag: "span", Text: "deep", Visible: true, BBox: [4]int{0, 0, 10, 10}}
	mid2 := &DOMNode{BackendNodeID: 98, Tag: "div", Text: "mid2", Visible: true, BBox: [4]int{0, 0, 10, 10}, Children: []*DOMNode{leaf}}
	mid1 := &DOMNode{BackendNodeID: 97, Tag: "div", Text: "mid1", Visible: true, BBox: [4]int{0, 0, 10, 10}, Children: []*DOMNode{mid2}}
	root := &DOMNode{BackendNodeID: 96, Tag: "body", Text: "root", Visible: true, BBox: [4]int{0, 0, 10, 10}, Children: []*DOMNode{mid1}}

	customSnap := func(url string) (*DOMNode, error) { return root, nil }
	svc := NewDOMService(NewEventBus(), customSnap, NewCache(time.Minute))
	svc.IframeMaxDepth = 2

	state, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// root @ depth 0, mid1 @ depth 1, mid2 @ depth 2 (kept as leaf),
	// leaf @ depth 3 (dropped).
	wantLines := []string{
		"[0] <body> root",
		"[1] <div> mid1",
		"[2] <div> mid2",
	}
	got := strings.Split(state.LLMText, "\n")
	if len(got) != len(wantLines) {
		t.Fatalf("got %d lines, want %d: %q", len(got), len(wantLines), state.LLMText)
	}
	for i, want := range wantLines {
		if got[i] != want {
			t.Errorf("line %d: got %q, want %q", i, got[i], want)
		}
	}
	if strings.Contains(state.LLMText, "deep") {
		t.Errorf("the depth-3 'deep' leaf should have been pruned, but text = %q", state.LLMText)
	}
}

// TestViewportThresholdFilters — a giant node whose BBox area
// exceeds the threshold must be dropped from the serialized output
// (but its visible children stay).
func TestViewportThresholdFilters(t *testing.T) {
	// Outer is 1000×1000 = 1,000,000 area; inner is 50×50 = 2,500.
	inner := &DOMNode{BackendNodeID: 11, Tag: "button", Text: "ok", Visible: true, BBox: [4]int{10, 10, 50, 50}}
	outer := &DOMNode{BackendNodeID: 10, Tag: "main", Text: "giant", Visible: true, BBox: [4]int{0, 0, 1000, 1000}, Children: []*DOMNode{inner}}

	customSnap := func(url string) (*DOMNode, error) { return outer, nil }
	svc := NewDOMService(NewEventBus(), customSnap, NewCache(time.Minute))
	// Threshold = 100,000 area → outer (1M) drops; inner (2.5K) stays.
	svc.ViewportThreshold = 100_000

	state, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if strings.Contains(state.LLMText, "giant") {
		t.Errorf("oversized 'main' node should have been dropped, text = %q", state.LLMText)
	}
	if !strings.Contains(state.LLMText, "ok") {
		t.Errorf("inner 'button' node should be present, text = %q", state.LLMText)
	}

	// Bonus: the surviving entry should be at index 0 (it's the only one).
	if len(state.SelectorMap) != 1 {
		t.Errorf("selector map should have 1 entry, got %d", len(state.SelectorMap))
	}
	if entry, ok := state.SelectorMap[0]; !ok || entry.BackendNodeID != 11 {
		t.Errorf("selector map [0] should point at BackendNodeID 11, got %+v", entry)
	}
}

// TestSubscribedAtConstruction (bonus 6th) — verifies the
// "subscribe at construction not on first Get" design choice. If
// the navigation happens BEFORE any Get, the first Get must still
// see the post-navigation page.
func TestSubscribedAtConstruction(t *testing.T) {
	snap, calls := NewStubSnapshot()
	bus := NewEventBus()
	svc := NewDOMService(bus, snap, NewCache(time.Minute))
	svc.CurrentURL = "https://a.example.com"
	ctx := context.Background()

	// Navigate FIRST — no Get has happened yet. The cache is empty
	// so Invalidate is a no-op in effect, but the *subscription*
	// must already exist.
	svc.CurrentURL = "https://b.example.com"
	if err := bus.Emit(ctx, NavigationEvent{URL: "https://b.example.com"}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	state, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(state.LLMText, "home") {
		t.Errorf("first Get after pre-emptive Navigation should see page B, got %q", state.LLMText)
	}
	if *calls != 1 {
		t.Errorf("expected exactly one snapshot call, got %d", *calls)
	}
}
