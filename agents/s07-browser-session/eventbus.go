package main

import (
	"context"
	"fmt"
	"sync"
)

// eventbus.go is a local, deliberately tiny re-declaration of the
// EventBus first taught in s06. Each session in learn-browser-use is
// a *self-contained Go module* — no imports across `agents/sNN-*`. The
// teaching cost is one extra ~80-line file; the payoff is that you can
// `cd agents/s07-browser-session && go build ./...` with zero
// reference to the rest of the repo.
//
// Compared to the s06 version this one drops a few features we don't
// need yet:
//   - no concurrent-safe handler invocation (s06 demonstrated channels;
//     here a mutex-guarded slice is enough and easier to reason about
//     for the lifecycle tests)
//   - no reflection-based auto-registration *on the bus itself* — that
//     lives in watchdog.go where AutoAttach walks the watchdog's
//     methods
//
// Upstream analog: `bubus.EventBus` from the bubus library, used as
// `self.event_bus = EventBus()` on `BrowserSession`. The two-method
// surface (Emit / Subscribe) is the irreducible shape — everything
// upstream adds (priorities, async/await chaining, dispatch.event_result)
// is icing we don't need for teaching session lifecycle.

// Event is the marker interface every dispatched value must satisfy.
// Returning a string from EventName() rather than letting the bus call
// reflect.TypeOf(e).Name() keeps subscription keys explicit; tests
// that subscribe by string literal still match if the struct is
// embedded or aliased downstream.
type Event interface {
	EventName() string
}

// EventBus is the in-memory pub-sub broker that ties a BrowserSession
// to its Watchdogs. Subscribers are looked up by Event.EventName().
//
// Thread safety: subscribe/emit both lock `mu`. Handlers run
// synchronously inside Emit so test code can assert side effects
// immediately after Emit returns — channel-based fan-out would force
// every test to add a `time.Sleep` or a sync.WaitGroup, which is more
// noise than it's worth for the teaching repo.
type EventBus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// Handler is the shape every subscriber must satisfy. The signature
// mirrors upstream's `async def on_FooEvent(self, event: FooEvent)` —
// ctx threads cancellation through, the typed event arrives second,
// and any error bubbles out to the bus for the caller to log.
type Handler func(ctx context.Context, e Event) error

// NewEventBus returns a bus with the handler map already allocated.
// The constructor exists so callers can't accidentally Subscribe on a
// nil-map bus.
func NewEventBus() *EventBus {
	return &EventBus{handlers: make(map[string][]Handler)}
}

// Subscribe registers a handler for every event whose EventName()
// matches the given key. The same handler can be registered multiple
// times (we accept duplicates intentionally — tests want to assert
// that AutoAttach doesn't double-subscribe; the assertion happens by
// counting, not by deduplicating here).
func (b *EventBus) Subscribe(eventName string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventName] = append(b.handlers[eventName], h)
}

// Emit invokes every subscriber for e.EventName(), in registration
// order, and returns the first non-nil error. We *don't* short-circuit
// on the first error — every subscriber runs, because in the real
// system one watchdog's failure shouldn't silently skip another's
// cleanup work. The returned error is just the first one observed so
// the caller has *something* to surface.
func (b *EventBus) Emit(ctx context.Context, e Event) error {
	b.mu.RLock()
	subs := append([]Handler(nil), b.handlers[e.EventName()]...)
	b.mu.RUnlock()

	var firstErr error
	for _, h := range subs {
		if err := h(ctx, e); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("event %q: %w", e.EventName(), firstErr)
	}
	return nil
}

// HandlerCount returns how many handlers are currently registered for
// the given event name. Tests use it to assert that AutoAttach wired
// up the expected number of subscribers and that calling Start twice
// did not double the count.
func (b *EventBus) HandlerCount(eventName string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventName])
}

// Clear drops every subscription. Session.Stop calls this so a
// subsequent Start (or Restart) begins from a clean slate — exactly
// matching upstream's `self.event_bus = EventBus()` reset on stop.
func (b *EventBus) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = make(map[string][]Handler)
}
