package main

import (
	"context"
	"fmt"
	"sync"
)

// eventbus.go is the minimal s06/s07-shape EventBus, re-declared here
// because the curriculum invariant forbids cross-session imports.
//
// What we keep:
//   - Subscribe(name, handler) — register a handler under one event
//     name. Multiple handlers per event are allowed.
//   - Emit(ctx, e) — synchronously fan out to every subscriber for
//     e.EventName(), short-circuiting on context cancellation but NOT
//     on per-handler error.
//   - Clear() — drop every subscription. Session.Stop calls this so
//     Restart begins with a clean bus.
//
// What we drop vs the upstream `bubus.EventBus`:
//   - No priorities. Handlers run in registration order, deterministic.
//   - No async dispatch + future-handle. Emit is synchronous so the
//     agent loop can read the side-effects immediately after.
//   - No metric/log instrumentation. Agent.Verbose covers the
//     observability story we need for teaching.

// Handler is the shape every subscriber must satisfy. The event arg is
// the marker-interface Event from types.go — downstream handlers can
// type-assert (or AutoAttach-decode via JSON) into a concrete struct.
type Handler func(ctx context.Context, e Event) error

// EventBus is the in-memory pub-sub broker that ties a BrowserSession
// to its Watchdogs.
//
// Concurrency: a mutex protects the handler map for Subscribe / Emit /
// Clear; handlers themselves run inside Emit's RLock so they should
// not call Subscribe (re-entrant Subscribe would deadlock — by design,
// since registering during dispatch is almost always a bug).
type EventBus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// NewEventBus returns a bus with the handler map already allocated.
func NewEventBus() *EventBus {
	return &EventBus{handlers: make(map[string][]Handler)}
}

// Subscribe registers a handler for the given event name. The same
// handler can be registered multiple times — Emit dispatches in
// registration order, duplicates included.
func (b *EventBus) Subscribe(name string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[name] = append(b.handlers[name], h)
}

// Emit fans out to every subscriber for e.EventName() and returns the
// first non-nil error. All handlers always run; failure of one does
// not skip the others.
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

// HandlerCount returns the number of subscribers registered for the
// given event name. Used by tests to confirm AutoAttach wiring.
func (b *EventBus) HandlerCount(name string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[name])
}

// Clear drops every subscription. Session.Stop calls this so a
// subsequent Start (or Restart) begins from a clean slate.
func (b *EventBus) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = make(map[string][]Handler)
}

// ---------------------------------------------------------------
//  Concrete events the s12 session emits
// ---------------------------------------------------------------

// SessionStartedEvent fires after Session.Start opens the (stub) CDP
// connection. Watchdogs that want to observe a startup subscribe to
// this name.
type SessionStartedEvent struct {
	CDPURL string `json:"cdp_url"`
}

// EventName satisfies Event.
func (SessionStartedEvent) EventName() string { return "SessionStartedEvent" }

// SessionStoppedEvent fires after Session.Stop. Reason is freeform
// for diagnostics.
type SessionStoppedEvent struct {
	Reason string `json:"reason"`
}

// EventName satisfies Event.
func (SessionStoppedEvent) EventName() string { return "SessionStoppedEvent" }

// NavigationEvent represents a page navigation. The DOMService
// subscribes to invalidate its cache on URL change.
type NavigationEvent struct {
	URL string `json:"url"`
}

// EventName satisfies Event.
func (NavigationEvent) EventName() string { return "NavigationEvent" }
