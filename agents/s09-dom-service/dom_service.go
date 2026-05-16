package main

import (
	"context"
	"fmt"
	"sync"
)

// dom_service.go is the heart of s09: the DOMService struct that
// orchestrates snapshot → walk → filter → cache, and listens for
// NavigationEvent so the cache invalidates on page change.
//
// Upstream analog: `class DomService` in `browser_use/dom/service.py`,
// ~1,200 LOC stretching across CDP plumbing (frame tree walk, AX
// merge, computed-style fetch), iframe handling (cross-origin
// dispatch, depth limits), and hidden-element hints. We keep the
// *shape* — service owns a snapshot driver, a cache, a bus
// subscription — and discard everything else.
//
// The teaching contract for s09 is four behaviors:
//
//   1. Snapshot-on-miss, return-from-cache-on-hit.
//   2. NavigationEvent invalidates the cache.
//   3. Time-based TTL provides a safety-net invalidation.
//   4. IframeMaxDepth + ViewportThreshold are filter knobs applied
//      *before* the serializer runs.

// DOMService is the per-session DOM orchestrator. Construct via
// NewDOMService so the bus subscription is wired up at the right
// moment (see "subscribe-at-construction" comment in Try It).
//
// Fields are exported because tests and the demo introspect them;
// in production this struct would be returned through an interface.
type DOMService struct {
	Cache    *Cache       // cache.go: TTL + Invalidate
	Bus      *EventBus    // eventbus.go: NavigationEvent subscription
	Snapshot SnapshotFunc // snapshot.go: pluggable producer

	// CurrentURL is what the service hands to Snapshot on every miss.
	// The demo updates this before emitting NavigationEvent so the
	// stub returns the right page. In a real wiring the BrowserSession
	// would update it from a Page.frameNavigated handler.
	CurrentURL string

	// IframeMaxDepth caps recursion into nested frames. Upstream
	// defaults to 5 with a max_iframes count of 100. We expose depth
	// because that's the test-friendly knob — the count cap is a
	// production concern with no teaching value here.
	IframeMaxDepth int

	// ViewportThreshold is the maximum BBox area an element can have
	// and still be kept. The upstream knob is a pixel distance from
	// the viewport edge (default 1000); we use area instead because
	// our fixtures don't have a viewport rect, only per-element
	// bounding boxes. Same lesson — filter ginormous elements before
	// the serializer ever sees them — different unit.
	ViewportThreshold int

	mu sync.Mutex
}

// NewDOMService builds a service and *immediately* subscribes its
// invalidation handler to the bus. Subscribing at construction (vs
// lazily on first Get) is deliberate; see the "non-obvious points"
// in the README. The short version: if a navigation fires before the
// first Get, the service still notices, so the first Get fetches
// the right page rather than the page the bus *would* have known
// about if it had been subscribed.
func NewDOMService(bus *EventBus, snap SnapshotFunc, cache *Cache) *DOMService {
	s := &DOMService{
		Cache:    cache,
		Bus:      bus,
		Snapshot: snap,

		// Sensible defaults: don't filter anything by default. Tests
		// override these explicitly to exercise the filters.
		IframeMaxDepth:    100,
		ViewportThreshold: 0, // 0 == "no threshold", area filter disabled
	}
	s.subscribe()
	return s
}

// subscribe registers the NavigationEvent invalidation handler.
// Called once from NewDOMService. Kept separate from the constructor
// body so tests that want to verify "the subscription happens at
// construction" can grep for the call.
func (s *DOMService) subscribe() {
	s.Bus.Subscribe("NavigationEvent", func(ctx context.Context, e Event) error {
		// We don't care about the URL in the event — the service has
		// its own CurrentURL field that the caller updates before
		// emitting. Invalidate is enough; the next Get will fetch.
		s.Cache.Invalidate()
		return nil
	})
}

// Get returns the cached SerializedState if it's fresh; otherwise
// it triggers the snapshot pipeline. The pipeline order is:
//
//   1. Cache.Get — if hit, return immediately (the common case).
//   2. Snapshot(CurrentURL) — produces a *DOMNode tree, errors
//      bubble up untouched.
//   3. Apply iframe-depth pruning + viewport-threshold filter.
//   4. Serialize the pruned tree to (text, selector map).
//   5. Cache.Set + return.
//
// We hold s.mu for the whole pipeline. This serializes concurrent
// Get callers — at most one Snapshot fires per "stampede". A more
// sophisticated impl would use singleflight; for teaching, plain
// mutex is enough and easier to read.
func (s *DOMService) Get(ctx context.Context) (*SerializedState, error) {
	if cached, ok := s.Cache.Get(); ok {
		return cached, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-checked locking: another goroutine might have filled
	// the cache while we were waiting for the lock. Not just a
	// micro-optimization — it prevents N goroutines from doing N
	// snapshots when only one is needed.
	if cached, ok := s.Cache.Get(); ok {
		return cached, nil
	}

	root, err := s.Snapshot(s.CurrentURL)
	if err != nil {
		return nil, fmt.Errorf("dom snapshot: %w", err)
	}

	pruned := s.applyFilters(root)
	state := Serialize(pruned)
	s.Cache.Set(state)
	return state, nil
}

// Invalidate drops the cached state. Exposed as a method so tests
// and external callers (e.g. a manual "refresh" button in a
// hypothetical UI) can force a re-fetch without going through the bus.
func (s *DOMService) Invalidate() {
	s.Cache.Invalidate()
}

// applyFilters runs the two filter passes in order:
//   1. pruneDepth — drop subtrees nested deeper than IframeMaxDepth.
//   2. filterByArea — drop nodes whose BBox area exceeds ViewportThreshold.
// The order matters: depth-pruning is structural and cheap (a
// recursive copy), area-filtering walks the surviving tree once.
// Doing area first would visit nodes we're about to throw away.
func (s *DOMService) applyFilters(root *DOMNode) *DOMNode {
	if root == nil {
		return nil
	}
	depthPruned := s.pruneDepth(root, 0)
	areaPruned := s.filterByArea(depthPruned)
	return areaPruned
}

// pruneDepth returns a copy of the subtree with branches deeper than
// IframeMaxDepth replaced by leaves (their further children dropped).
// We don't drop the offending node itself — we just truncate its
// descendants — because the node at depth == IframeMaxDepth might
// still be useful (e.g. an iframe shell whose existence the LLM
// should know about).
func (s *DOMService) pruneDepth(n *DOMNode, depth int) *DOMNode {
	if n == nil {
		return nil
	}
	copy := *n
	if depth >= s.IframeMaxDepth {
		copy.Children = nil
		return &copy
	}
	copy.Children = nil
	for _, c := range n.Children {
		copy.Children = append(copy.Children, s.pruneDepth(c, depth+1))
	}
	return &copy
}

// filterByArea drops nodes whose BBox area exceeds the threshold,
// but keeps their visible children — a giant <main> wrapper
// shouldn't take its children with it. ViewportThreshold == 0
// disables the filter entirely.
func (s *DOMService) filterByArea(n *DOMNode) *DOMNode {
	if n == nil {
		return nil
	}
	if s.ViewportThreshold == 0 {
		return n
	}

	copy := *n
	copy.Children = nil
	for _, c := range n.Children {
		filtered := s.filterByArea(c)
		copy.Children = append(copy.Children, filtered)
	}

	area := n.BBox[2] * n.BBox[3]
	if area > s.ViewportThreshold {
		// Hide this node, but keep recursing so its visible kids stay.
		// "Hide" = flip Visible to false; the serializer drops it.
		copy.Visible = false
	}
	return &copy
}
