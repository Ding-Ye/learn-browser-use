package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// dom_service.go is the deliberately-tiny DOMService for the s12
// integration. It does THREE things and nothing else:
//
//   1. Hold a hardcoded SerializedDOM for the demo page.
//   2. Cache that DOM. Subscribe to NavigationEvent so the cache
//      invalidates on URL change.
//   3. Expose Get(ctx) and CurrentURL(url) for the agent loop.
//
// The full s09 implementation (snapshot func, area-filter, depth-prune,
// TTL clock) was the spotlight of that chapter. Re-shipping that here
// would bury the integration story. Instead we keep the smallest
// surface that lets the Agent.Run loop call session.DOM.Get() and
// receive a non-empty serialized page.
//
// Upstream analog: `class DomService` in `browser_use/dom/service.py`.
// Our 90 lines preserve the bus-subscription + cache-invalidate
// shape; everything else (CDP snapshot, iframe walking) is replaced
// by a fixed fixture per URL.

// DOMService is the per-Agent DOM orchestrator. Construct via
// NewDOMService so the bus subscription is wired at the right moment.
type DOMService struct {
	Bus      *EventBus
	currentURL string

	mu     sync.Mutex
	cached *SerializedDOM
}

// NewDOMService builds a service and subscribes its invalidation
// handler onto the bus immediately. Subscribing at construction (vs
// lazily on first Get) matches s09 — if a NavigationEvent fires
// before the first Get, the service still notices.
func NewDOMService(bus *EventBus) *DOMService {
	s := &DOMService{Bus: bus}
	bus.Subscribe("NavigationEvent", func(ctx context.Context, e Event) error {
		nav, ok := e.(NavigationEvent)
		if !ok {
			return fmt.Errorf("dom service: unexpected event type %T", e)
		}
		s.mu.Lock()
		s.currentURL = nav.URL
		s.cached = nil // invalidate cache so next Get reflects new page
		s.mu.Unlock()
		return nil
	})
	return s
}

// SetCurrentURL is the manual seam — tests and the demo's first turn
// (before any NavigationEvent has fired) use this to position the
// service on a starting page.
func (s *DOMService) SetCurrentURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentURL = url
	s.cached = nil
}

// CurrentURL returns the URL the service believes is loaded.
func (s *DOMService) CurrentURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentURL
}

// Get returns the cached SerializedDOM, fetching from the fixture on
// a cache miss. We hold s.mu around the fetch so concurrent Get
// callers don't double-fetch.
func (s *DOMService) Get(ctx context.Context) (*SerializedDOM, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return s.cached, nil
	}
	state, err := fixtureFor(s.currentURL)
	if err != nil {
		return nil, err
	}
	s.cached = state
	return state, nil
}

// fixtureFor returns the hardcoded DOM fixture for a given URL. The
// two pages mimic the demo flow:
//
//   - "" or "https://search.example/" → a "Search" page with one input
//     and one submit button. Index 0 = input, 1 = submit.
//
//   - anything starting with "https://search.example/results"
//     → a results page with 3 links. Index 0..2 = the result links.
//
// In production this function would be replaced by a CDP DOMSnapshot
// call + serializer; the *signature* (url → SerializedDOM) is the
// stable contract.
func fixtureFor(url string) (*SerializedDOM, error) {
	if url == "" || strings.HasPrefix(url, "https://search.example") && !strings.Contains(url, "results") {
		return &SerializedDOM{
			LLMText: "[0] <input> q\n[1] <button> Search",
			SelectorMap: map[int]SelectorEntry{
				0: {BackendNodeID: 100, BBox: DOMRect{X: 10, Y: 10, W: 200, H: 24}},
				1: {BackendNodeID: 101, BBox: DOMRect{X: 220, Y: 10, W: 60, H: 24}},
			},
		}, nil
	}
	if strings.Contains(url, "results") {
		return &SerializedDOM{
			LLMText: "[0] <a> First result\n[1] <a> Second result\n[2] <a> Third result",
			SelectorMap: map[int]SelectorEntry{
				0: {BackendNodeID: 200, BBox: DOMRect{X: 10, Y: 40, W: 400, H: 18}},
				1: {BackendNodeID: 201, BBox: DOMRect{X: 10, Y: 60, W: 400, H: 18}},
				2: {BackendNodeID: 202, BBox: DOMRect{X: 10, Y: 80, W: 400, H: 18}},
			},
		}, nil
	}
	// Unknown URL → minimal "blank page" so the agent at least sees
	// SOMETHING. Better than an error here: an LLM that asked for an
	// unknown URL should get a blank-page signal back, not a hard fail.
	return &SerializedDOM{
		LLMText:     "(no DOM available)",
		SelectorMap: map[int]SelectorEntry{},
	}, nil
}
