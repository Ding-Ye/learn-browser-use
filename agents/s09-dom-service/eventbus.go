package main

import (
	"context"
	"sync"
)

// eventbus.go is the local re-declaration of the EventBus pattern
// first taught in s06 and threaded through s07. Each session in
// learn-browser-use is a *self-contained Go module* — no imports
// across `agents/sNN-*`. The teaching cost is one extra ~60-line file
// per session; the payoff is that you can
// `cd agents/s09-dom-service && go build ./...` with zero reference
// to the rest of the repo.
//
// Compared to s06's version this one drops:
//   - reflection-based AutoAttach (s09 only subscribes one handler
//     and does it explicitly in DOMService.subscribe)
//   - JSON-bridge between subpackages (no subpackages here either)
//   - HandlerCount diagnostics (s09's tests don't introspect the bus)
//
// What's left is the irreducible two-method bus: Subscribe + Emit.
//
// Upstream analog: `bubus.EventBus` used as `BrowserSession.event_bus`.
// In s12 the DOMService would receive the session's bus rather than
// owning its own; here we construct a fresh bus per service so the
// teaching example stays self-contained.

// Event is the marker interface every dispatched value must satisfy.
type Event interface {
	EventName() string
}

// EventBus is the in-memory pub-sub broker. Subscribers are looked
// up by Event.EventName(). Subscribe/Emit both lock `mu`. Handlers
// run synchronously inside Emit so test code can assert side effects
// (e.g. "cache was invalidated") immediately after Emit returns —
// channel-based fan-out would force every test to add a sync barrier.
type EventBus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// Handler is the shape every subscriber must satisfy. Same signature
// as s06/s07 — ctx threads cancellation, the typed event arrives
// second, any error bubbles to the bus caller.
type Handler func(ctx context.Context, e Event) error

// NewEventBus returns a bus with the handler map already allocated
// so callers can't accidentally Subscribe on a nil-map bus.
func NewEventBus() *EventBus {
	return &EventBus{handlers: make(map[string][]Handler)}
}

// Subscribe registers a handler for every event whose EventName()
// matches the given key. Same handler can be registered multiple
// times (we accept duplicates intentionally; the service only calls
// Subscribe once at construction so this isn't a practical concern).
func (b *EventBus) Subscribe(eventName string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventName] = append(b.handlers[eventName], h)
}

// Emit invokes every subscriber for e.EventName(), in registration
// order, and returns the first non-nil error. We *don't*
// short-circuit on the first error — every subscriber runs, because
// one watchdog's failure shouldn't silently skip another's cleanup.
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
	return firstErr
}

// NavigationEvent is the only event s09 cares about. It mirrors the
// type in s07's `event.go` — different Go type, same shape, same
// EventName() return so a real BrowserSession's bus would route to
// us without modification. Upstream's analog fires on every
// `Page.frameNavigated` from CDP.
type NavigationEvent struct {
	URL string
}

func (NavigationEvent) EventName() string { return "NavigationEvent" }
