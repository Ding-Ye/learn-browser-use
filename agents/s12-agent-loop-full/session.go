package main

import (
	"context"
	"fmt"
	"sync"
)

// session.go is the BrowserSession owned by Agent. It bundles the
// stub CDP client, the EventBus, and the list of attached watchdogs.
// Same lifecycle methods as s07: Start opens (stub) CDP, attaches
// every watchdog onto the bus, and emits SessionStartedEvent; Stop
// reverses that.
//
// What's intentionally smaller in s12: we ship exactly ONE watchdog —
// NavigationWatchdog. The agent loop drives one fake page-load per
// turn (via the search/click tools) which fires NavigationEvent; the
// DOMService is subscribed and invalidates its cache. That's enough
// to demonstrate "watchdog pattern is alive inside the integrated
// agent" without dragging in the seven-watchdog real implementation.

// Watchdog is the marker interface every attachable watchdog must
// implement. The actual handler methods are looked up at attach time —
// in s07 we used reflection; here we keep it simple and call
// Attach(bus) on a typed interface. The reflection version still
// works for readers who want it; we trade flexibility for ~30 lines
// of teaching clarity.
type Watchdog interface {
	Attach(bus *EventBus)
}

// BrowserSession is the per-task browser state owned by Agent.
// Concurrency: mu guards Started for idempotent Start/Stop. The bus
// itself has its own internal mutex.
type BrowserSession struct {
	Client    CDPClient
	Bus       *EventBus
	Watchdogs []Watchdog

	mu      sync.Mutex
	Started bool
}

// NewBrowserSession composes a session with sane defaults. The bus is
// created here so callers can't accidentally pass a nil-map bus into
// a watchdog. Watchdogs are variadic for natural call-site reading.
func NewBrowserSession(client CDPClient, watchdogs ...Watchdog) *BrowserSession {
	return &BrowserSession{
		Client:    client,
		Bus:       NewEventBus(),
		Watchdogs: watchdogs,
	}
}

// Start opens the (stub) CDP connection, attaches every watchdog, and
// emits SessionStartedEvent. Idempotent: calling twice is a no-op.
func (s *BrowserSession) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.Started {
		s.mu.Unlock()
		return nil
	}

	_, err := s.Client.Send("Target.attachToTarget", map[string]any{
		"targetId": "stub-target-0",
		"flatten":  true,
	})
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("session start: CDP attach failed: %w", err)
	}

	for _, w := range s.Watchdogs {
		w.Attach(s.Bus)
	}

	s.Started = true
	s.mu.Unlock()

	return s.Bus.Emit(ctx, SessionStartedEvent{CDPURL: "stub://recorder"})
}

// Stop reverses Start. We emit SessionStoppedEvent BEFORE clearing the
// bus so handler cleanup actually runs.
func (s *BrowserSession) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.Started {
		s.mu.Unlock()
		return nil
	}
	s.Started = false
	s.mu.Unlock()

	emitErr := s.Bus.Emit(ctx, SessionStoppedEvent{Reason: "Stop() called"})
	_, sendErr := s.Client.Send("Target.detachFromTarget", map[string]any{
		"sessionId": "stub-session-0",
	})

	s.Bus.Clear()

	if emitErr != nil {
		return emitErr
	}
	if sendErr != nil {
		return fmt.Errorf("session stop: CDP detach failed: %w", sendErr)
	}
	return nil
}

// IsRunning is the read-side of Started, with mu held so a concurrent
// reader sees a consistent state.
func (s *BrowserSession) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Started
}

// ---------------------------------------------------------------
//  NavigationWatchdog — the one watchdog s12 ships
// ---------------------------------------------------------------

// NavigationWatchdog records every NavigationEvent seen on the bus.
// In s07 the role was demonstrated by a DOMWatchdog that invalidated
// the cache; here we use a record-only watchdog because the
// DOMService.subscribe() in dom_service.go already wires the
// invalidation. The watchdog's job in s12 is to count navigations so
// tests / verbose-mode logging have an observable signal.
type NavigationWatchdog struct {
	mu      sync.Mutex
	visited []string
}

// NewNavigationWatchdog returns a fresh watchdog with no visits
// recorded.
func NewNavigationWatchdog() *NavigationWatchdog {
	return &NavigationWatchdog{}
}

// Attach subscribes the watchdog's handler to NavigationEvent.
func (w *NavigationWatchdog) Attach(bus *EventBus) {
	bus.Subscribe("NavigationEvent", func(ctx context.Context, e Event) error {
		nav, ok := e.(NavigationEvent)
		if !ok {
			return fmt.Errorf("nav watchdog: unexpected event type %T", e)
		}
		w.mu.Lock()
		w.visited = append(w.visited, nav.URL)
		w.mu.Unlock()
		return nil
	})
}

// Visited returns the URLs the watchdog has seen, in order.
func (w *NavigationWatchdog) Visited() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.visited))
	copy(out, w.visited)
	return out
}
