package main

import (
	"sync"
	"time"
)

// cache.go is a tiny single-entry TTL cache. The DOMService keeps
// exactly one cached snapshot — the most recent serialized state for
// the current page. Multiple-page caches exist upstream (one per
// CDP target), but s09 collapses that to one entry because adding
// per-URL keying obscures the *invalidation* lesson.
//
// Why TTL *and* explicit invalidation? Two complementary trigger
// surfaces:
//
//   - Explicit Invalidate() is fired by the NavigationEvent
//     subscriber — the moment we know the cache is stale.
//   - TTL is the safety net for things we *didn't* explicitly
//     subscribe to (XHR mutations, JS-driven scroll-into-view DOM
//     edits, the "we forgot to wire up SomeOtherEvent" case).
//
// Upstream's equivalent ships with no TTL by default but the LLM
// loop calls captureSnapshot every step anyway. In our teaching
// repo the TTL is configurable so tests can assert expiry without
// fighting time.

// Cache holds at most one SerializedState plus the timestamp at
// which it was stored and the TTL after which it expires. Concurrent
// access is mutex-guarded; in production a session is single-agent
// so contention is low but the lock keeps the contract honest.
type Cache struct {
	mu        sync.Mutex
	Data      *SerializedState
	UpdatedAt time.Time
	TTL       time.Duration

	// now is a clock injection point. Tests override it to drive the
	// "TTL expires" case without a real time.Sleep — the lesson is
	// the TTL logic, not how Go schedules goroutines. In production
	// the default `time.Now` is used.
	now func() time.Time
}

// NewCache constructs a cache with the given TTL. A TTL of 0 means
// "always expired immediately" — useful as a kill switch when an
// experiment disables caching entirely.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		TTL: ttl,
		now: time.Now,
	}
}

// Get returns the cached state if it exists and the TTL has not
// elapsed. The boolean is the "fresh hit" signal — callers branch
// on it rather than the *SerializedState alone, because a nil value
// *after* a recent Set is meaningful (Invalidate dropped it on a
// navigation), distinct from a never-populated cache.
func (c *Cache) Get() (*SerializedState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Data == nil {
		return nil, false
	}
	// TTL = 0 means "expire immediately on read" — every Get is a miss.
	// Strict less-than on age, not <=, so a Set immediately followed
	// by a Get inside the same nanosecond is a hit.
	if c.now().Sub(c.UpdatedAt) >= c.TTL {
		return nil, false
	}
	return c.Data, true
}

// Set replaces the cached value and resets the timer. We don't
// validate that `s != nil`; the service is the only caller and only
// ever sets non-nil. A separate Invalidate is the explicit way to
// drop the cache.
func (c *Cache) Set(s *SerializedState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Data = s
	c.UpdatedAt = c.now()
}

// Invalidate drops the cached value. The next Get returns
// (nil, false). Called from the NavigationEvent handler — see
// DOMService.subscribe().
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Data = nil
	// We deliberately *don't* zero UpdatedAt; it's harmless either
	// way because Get's Data == nil check fires first, but leaving
	// the timestamp lets debugging code reconstruct when the
	// invalidation happened relative to the last Set.
}
