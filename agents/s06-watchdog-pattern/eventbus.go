package s06

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Handler is the function shape every subscriber must satisfy.
//
// We pass the Event interface (not a concrete pointer) and let each
// handler type-assert to the event it cares about. The reflection-based
// AutoAttach in watchdog.go does that assertion for the writer, so most
// user code never sees an Event-typed parameter directly.
type Handler func(ctx context.Context, e Event) error

// EventBus is a tiny, channel-flavoured pub-sub.
//
// Despite the name, we don't actually expose channels: handlers run
// synchronously inside Emit. The reason — see docs/{zh,en}/s06 §3 — is
// that browser-use's real bus needs ordering guarantees (the agent
// blocks on a Click event until the click handler is done). A buffered
// channel would not, by itself, give us that. sync.RWMutex + a slice of
// handlers per event name does, and stays under 60 LOC.
//
// Concurrency:
//   - Subscribe is safe to call from any goroutine at any time.
//   - Emit is safe to call from any goroutine. Handlers for the same
//     event run sequentially (registration order); independent Emits
//     proceed in parallel.
//
// Errors from handlers are aggregated with errors.Join. The bus does
// not abort on the first handler error — every subscribed handler
// gets a chance to react.
type EventBus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// NewEventBus returns a ready-to-use bus.
func NewEventBus() *EventBus {
	return &EventBus{handlers: make(map[string][]Handler)}
}

// Subscribe registers handler to receive every Emit for the given
// eventName. Multiple handlers for the same name fire in registration
// order. Subscribing the same handler twice will register it twice —
// upstream raises a "duplicate handler" RuntimeError; we skip that
// because Go has no method-equality concept and the test in watchdog.go
// already de-duplicates by method name.
func (b *EventBus) Subscribe(eventName string, handler Handler) {
	if handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventName] = append(b.handlers[eventName], handler)
}

// Emit dispatches e to every handler subscribed to its EventName().
// Unsubscribed events are silently dropped (matches upstream behaviour:
// an EventBus with no listeners for X.event_type just no-ops).
//
// Returns nil if every handler returned nil. If any handlers returned
// non-nil errors, returns an errors.Join of all of them.
func (b *EventBus) Emit(ctx context.Context, e Event) error {
	if e == nil {
		return fmt.Errorf("eventbus: nil event")
	}
	name := e.EventName()

	// Copy the slice under RLock so handler execution does not hold the
	// lock — a long-running handler must not block Subscribe / Emit on
	// other event names. This is the classic "snapshot under lock,
	// invoke outside" pattern.
	b.mu.RLock()
	hs := make([]Handler, len(b.handlers[name]))
	copy(hs, b.handlers[name])
	b.mu.RUnlock()

	if len(hs) == 0 {
		return nil
	}

	var errs []error
	for _, h := range hs {
		if err := h(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HandlerCount reports how many handlers are subscribed to eventName.
// Intended for tests + the demo summary; production code should not
// rely on this number.
func (b *EventBus) HandlerCount(eventName string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventName])
}
