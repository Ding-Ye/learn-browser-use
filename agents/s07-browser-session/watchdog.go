package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// watchdog.go re-declares the Watchdog contract first taught in s06.
// Like eventbus.go, this is local on purpose — the curriculum invariant
// is "every session module is self-contained, zero cross-session
// imports". The reflection logic below is mechanically similar to
// s06's; the difference is that AutoAttach is *driven by Session.Start*
// here instead of being called directly by main().
//
// Upstream analog: `BaseWatchdog.attach_to_session()` in
// browser_use/browser/watchdog_base.py. Upstream walks the MRO, picks
// methods whose name starts with `on_` and ends with an event name,
// and registers each one via the bubus bus. We do the same with Go's
// reflect package.

// Watchdog is the empty interface every attachable watchdog must
// implement. The marker is intentionally empty — what makes something
// a watchdog is *having on-prefixed handler methods*, which AutoAttach
// discovers via reflection at attach time. Forcing implementers to
// also implement a method named (say) "Attach()" would duplicate that
// information.
//
// Method shape AutoAttach looks for:
//
//	On<EventName>(ctx context.Context, e *<EventParamType>) error
//
// where <EventName> is *both* the suffix of the method name and the
// EventName() returned by the emitted event. <EventParamType> only
// needs to share field names with the emitted struct — AutoAttach
// bridges the two via JSON round-trip (see the closure below). That
// indirection is what lets the watchdogs subpackage declare its own
// event struct types without importing the parent main package.
type Watchdog interface{}

// AutoAttach walks every method on `w` and, for each method whose
// name begins with "On" and matches the shape
// `func(*Watchdog, context.Context, *EventStruct) error`, subscribes
// the method to bus under the event name derived from the method name
// (strip "On"). The handler converts the emitted Event into the
// watchdog's typed parameter via JSON, which lets the two structs
// live in different packages without one importing the other.
//
// Returning the list of attached names makes Start observable: the
// demo prints them, and tests use the slice to assert "exactly these
// handlers were wired".
func AutoAttach(bus *EventBus, w Watchdog) []string {
	v := reflect.ValueOf(w)
	t := v.Type()

	var attached []string

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errType := reflect.TypeOf((*error)(nil)).Elem()

	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if !strings.HasPrefix(m.Name, "On") {
			continue
		}

		// Expected signature: (receiver, context.Context, *EventStruct) error.
		// reflect.Type.NumIn includes the receiver as the 0th arg.
		fn := m.Func.Type()
		if fn.NumIn() != 3 || fn.NumOut() != 1 {
			continue
		}
		if !fn.In(1).Implements(ctxType) {
			continue
		}
		if fn.In(2).Kind() != reflect.Ptr {
			continue
		}
		if !fn.Out(0).Implements(errType) {
			continue
		}

		eventPtrType := fn.In(2)
		eventName := strings.TrimPrefix(m.Name, "On")

		method := v.Method(i)

		// The closure copies the emitted event into the watchdog's
		// declared parameter type via JSON. Why JSON instead of
		// reflect-by-field? JSON is one line, handles nested structs
		// for free, and the encoding/json package is already in our
		// stdlib budget. The cost is a marshal+unmarshal per event,
		// which is fine for teaching and a real (sub-microsecond)
		// concern only at high event rates.
		handler := func(ctx context.Context, e Event) error {
			ptr := reflect.New(eventPtrType.Elem())
			// Re-encode the source event into JSON, decode into the
			// handler's typed param. Matching field names are enough;
			// EventName() identity doesn't need to be checked because
			// the bus already routed by string.
			data, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("autoattach: marshal event %q: %w", eventName, err)
			}
			if err := json.Unmarshal(data, ptr.Interface()); err != nil {
				return fmt.Errorf("autoattach: unmarshal into %s: %w", eventPtrType, err)
			}
			out := method.Call([]reflect.Value{
				reflect.ValueOf(ctx),
				ptr,
			})
			if !out[0].IsNil() {
				return out[0].Interface().(error)
			}
			return nil
		}

		bus.Subscribe(eventName, handler)
		attached = append(attached, fmt.Sprintf("%s.%s→%s", t.String(), m.Name, eventName))
	}
	return attached
}
