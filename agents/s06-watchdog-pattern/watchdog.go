package s06

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// Watchdog is the marker interface every concrete watchdog implements.
//
// The interface is deliberately empty for the same reason upstream's
// BaseWatchdog is mostly empty: a watchdog's contract is "I expose
// methods that look like OnXxxEvent(ctx, *XxxEvent) error", which is
// shape-driven not interface-driven. Go has no built-in shape matching
// at the type-system level, so we lean on reflection — see AutoAttach.
type Watchdog interface{}

// AutoAttach scans w for methods named OnXxxEvent(ctx, *XxxEvent) error
// and Subscribes each one to the bus under "XxxEvent". This is the
// direct Go translation of upstream attach_to_session() in
// browser_use/browser/watchdog_base.py#L243-L291: walk dir(self), match
// on_EventName names, and register.
//
// Concretely, given a struct value with
//
//	func (w *T) OnFooEvent(ctx context.Context, e *FooEvent) error
//
// AutoAttach calls bus.Subscribe("FooEvent", adaptor) where adaptor
// does the *FooEvent type-assertion on the bus's Event payload.
//
// Returns the list of event names it subscribed to (sorted by
// registration scan order — Go reflect returns methods sorted by name,
// so this is deterministic).
//
// Methods that don't match the shape are silently skipped, including:
//   - Names not starting with "On" or not ending with "Event".
//   - Wrong arity (not 2 args after the receiver).
//   - Wrong first arg (not context.Context).
//   - Wrong second arg (not a pointer to a struct, or a pointer to a
//     struct whose name doesn't match the method's suffix).
//   - Wrong return type (not a single error).
//
// This permissiveness matches upstream behaviour: arbitrary helper
// methods (logger, _log_pretty_path, ...) coexist on a watchdog
// without breaking auto-registration.
func AutoAttach(w Watchdog, bus *EventBus) ([]string, error) {
	if w == nil || bus == nil {
		return nil, fmt.Errorf("watchdog: nil watchdog or bus")
	}
	v := reflect.ValueOf(w)
	t := v.Type()

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errType := reflect.TypeOf((*error)(nil)).Elem()

	var registered []string
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		name := m.Name

		// Must look like OnXxxEvent.
		if !strings.HasPrefix(name, "On") || !strings.HasSuffix(name, "Event") {
			continue
		}
		eventName := strings.TrimPrefix(name, "On")
		if eventName == "Event" || eventName == "" {
			continue
		}

		mt := m.Type
		// Receiver + (ctx, *Event) → 3 inputs total.
		if mt.NumIn() != 3 {
			continue
		}
		if mt.In(1) != ctxType {
			continue
		}
		eventArg := mt.In(2)
		if eventArg.Kind() != reflect.Ptr || eventArg.Elem().Kind() != reflect.Struct {
			continue
		}
		// The pointed-to struct's name must equal the suffix. This is
		// the sharp safety net: it lets us catch typos like
		// OnDownloadStartedEvent(ctx, *NavigationEvent) at attach time
		// instead of letting the handler silently never fire.
		if eventArg.Elem().Name() != eventName {
			continue
		}
		// Single error return.
		if mt.NumOut() != 1 || mt.Out(0) != errType {
			continue
		}

		methodVal := v.Method(i)
		registered = append(registered, eventName)

		// Adaptor closes over methodVal and the expected pointer type.
		// At Emit time we receive an Event interface; we allocate a
		// fresh *EventStruct, copy the value in, and Call(method).
		expectedPtr := eventArg
		bus.Subscribe(eventName, func(ctx context.Context, e Event) error {
			ev := reflect.ValueOf(e)
			// Accept both value and pointer event payloads. Our
			// constants emit values (e.g. DownloadStartedEvent{...}),
			// so we promote them to a pointer here for the method.
			var ptr reflect.Value
			switch ev.Kind() {
			case reflect.Ptr:
				ptr = ev
			default:
				ptr = reflect.New(ev.Type())
				ptr.Elem().Set(ev)
			}
			if ptr.Type() != expectedPtr {
				return fmt.Errorf(
					"watchdog: handler %s expected %s, got %s",
					name, expectedPtr, ptr.Type(),
				)
			}
			out := methodVal.Call([]reflect.Value{
				reflect.ValueOf(ctx),
				ptr,
			})
			if errVal := out[0]; !errVal.IsNil() {
				return errVal.Interface().(error)
			}
			return nil
		})
	}
	return registered, nil
}
