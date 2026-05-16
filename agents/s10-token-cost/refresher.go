package main

import (
	"errors"
	"time"
)

// refresher.go demonstrates the *optional* second pricing source —
// what upstream calls the LiteLLM HTTP fetch path. We don't actually
// make a network call; the `Source` field is a function the caller
// supplies, and the stubbed default just returns a hardcoded map.
//
// The teaching emphasis is the cache lifecycle:
//
//   - First Get(model) call (or first after TTL expiry) → invoke Source,
//     stash result with the current timestamp, return value.
//   - Subsequent Get(model) calls within TTL → return from `cached`.
//
// Why a `Source` function and not a URL? Because injecting a fake
// source for tests is one closure-literal — no httptest.Server, no
// mock library. The s10 chapter is about cost math and cache TTL,
// not about HTTP plumbing; that's covered by s02-llm-provider.
//
// Why TTL not invalidation event? An event-based scheme ("re-fetch
// when an upstream webhook fires") is one of two valid designs, but
// TTL is the right choice when:
//   (a) the source doesn't push notifications,
//   (b) staleness for a bounded window is acceptable (it is — model
//       prices change a few times per year), and
//   (c) the cost of re-fetching is small relative to the cost of
//       being wrong about a price (it is — a 24h cache lag means at
//       worst you bill yesterday's rate, not last month's).
// Both upstream and we settle on TTL.

// ErrNoSource is returned when Get is called and no Source is configured.
var ErrNoSource = errors.New("refresher: no pricing source configured")

// Refresher wraps a pricing source (real or fake) with a TTL cache.
//
// Field notes:
//   - Source: the pluggable "where do we get prices?" function. The
//     zero value is nil → Get returns ErrNoSource. A constructor
//     `NewStubRefresher` wires up the default hardcoded source.
//   - CacheTTL: how long a cached map stays fresh. Upstream defaults
//     to 24h; here we let the caller pick because tests want short
//     TTLs to exercise the expiry branch in milliseconds.
//   - lastFetch / cached: the actual cache. Private — callers should
//     interact via Get.
type Refresher struct {
	Source    func() (map[string]Pricing, error)
	CacheTTL  time.Duration
	lastFetch time.Time
	cached    map[string]Pricing

	// clock lets TestCacheTTL force time forward without sleeping.
	// Defaults to time.Now in NewStubRefresher; tests can set it
	// directly on the struct since the field is unexported but lives
	// in the same package as the test file.
	clock func() time.Time
}

// NewStubRefresher returns a Refresher whose Source is a hardcoded
// map — the "stubbed remote" path the spec calls for. The CacheTTL
// is the standard 24h that upstream uses. Tests that want shorter
// TTLs construct &Refresher{...} directly.
func NewStubRefresher() *Refresher {
	return &Refresher{
		Source:   defaultStubSource,
		CacheTTL: 24 * time.Hour,
		clock:    time.Now,
	}
}

// defaultStubSource pretends to fetch pricing from a remote endpoint.
// In production this would be an HTTP GET against LiteLLM's hosted
// JSON; here it's a hardcoded map kept deliberately *different* from
// pricing_data.json so callers (and tests) can verify they're seeing
// the refreshed values rather than the embedded ones.
func defaultStubSource() (map[string]Pricing, error) {
	return map[string]Pricing{
		// Slightly different rates to make "refreshed vs embedded"
		// visible. The actual values aren't load-bearing — just the
		// fact that they differ from pricing_data.json.
		"gpt-4o":            {InputPer1k: 0.0024, OutputPer1k: 0.0095},
		"gpt-4o-mini":       {InputPer1k: 0.00014, OutputPer1k: 0.00058},
		"claude-3-5-sonnet": {InputPer1k: 0.0029, OutputPer1k: 0.0145},
		"gemini-1.5-pro":    {InputPer1k: 0.00120, OutputPer1k: 0.0048},
	}, nil
}

// fresh reports whether the cache is within TTL.
// Separate method so tests can read the boolean directly if needed.
func (r *Refresher) fresh() bool {
	if r.cached == nil {
		return false
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	return r.clock().Sub(r.lastFetch) < r.CacheTTL
}

// Get returns the pricing for model, hitting the cache when fresh,
// otherwise invoking Source and updating the cache.
//
// The return contract mirrors LookupPricing: an unknown-model
// response is (zero, nil) — *not* an error. Errors are reserved for
// "the Source itself blew up", a categorically different failure.
func (r *Refresher) Get(model string) (Pricing, error) {
	if r.Source == nil {
		return Pricing{}, ErrNoSource
	}

	if !r.fresh() {
		fresh, err := r.Source()
		if err != nil {
			return Pricing{}, err
		}
		r.cached = fresh
		if r.clock == nil {
			r.clock = time.Now
		}
		r.lastFetch = r.clock()
	}

	// Unknown model is fine — caller treats it as "no override".
	return r.cached[model], nil
}

// All returns a copy of the currently cached map (after a Get-style
// fetch if needed). Useful for callers that want to feed the entire
// refreshed table into NewTokenCostWithPricing.
func (r *Refresher) All() (map[string]Pricing, error) {
	if r.Source == nil {
		return nil, ErrNoSource
	}
	if !r.fresh() {
		fresh, err := r.Source()
		if err != nil {
			return nil, err
		}
		r.cached = fresh
		if r.clock == nil {
			r.clock = time.Now
		}
		r.lastFetch = r.clock()
	}
	out := make(map[string]Pricing, len(r.cached))
	for k, v := range r.cached {
		out[k] = v
	}
	return out, nil
}
