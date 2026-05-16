---
title: "s06 · Watchdog & event bus"
chapter: 6
slug: s06-watchdog-pattern
est_read_min: 14
---

# s06 · Watchdog & event bus

> Teaching focus: s05's Element actor talked straight to the (stub) CDP client. That style works for one concern; once a browser session has 10 — downloads, popups, security, DOM snapshots, screenshots, recordings — a monolithic controller drowns. Upstream's `BaseWatchdog` solves this with an event bus + auto-discovered handlers. This session ports both pieces to ~250 lines of Go: a channel-flavoured `EventBus`, plus reflection-based `AutoAttach` that scans a watchdog struct for `OnXxxEvent` methods.

---

## Problem / 问题

A `BrowserSession` has many concerns:

- **Downloads** — listen for `Browser.downloadWillBegin`, route PDF / zip / image to disk, fire a `FileDownloadedEvent`.
- **Popups** — listen for `Page.javascriptDialogOpening`, click OK on alert/confirm, Cancel on prompt.
- **Security** — block navigation to private subnets / disallowed origins.
- **DOM** — capture snapshots, hashes, screenshots on each navigation.

If you put all of that into one Python class, you get the kind of file we love to gawk at: 4000 lines, three layers of `try/except`, every concern fighting for the namespace. Upstream's `browser_use/browser/session.py` is 4000 LOC even *after* the watchdogs were split out. Without that split, double it.

The constraint each concern shares:

1. It reacts to **events** the browser emits (CDP frames, lifecycle ticks, agent actions).
2. It maintains **private state** the rest of the system should not touch.
3. It runs **independently** — a buggy popup handler shouldn't break downloads.

Translation: a pub-sub bus, where each concern is its own subscriber. Python's `bubus` library is the upstream choice; we want the Go equivalent.

s06 answers three questions:

1. **What is the minimum API of a bus?** → `Subscribe(eventName, handler)` + `Emit(ctx, event)`.
2. **How does a watchdog register without ceremony?** → Reflection finds `OnXxxEvent(ctx, *XxxEvent) error` automatically.
3. **What semantics do handlers need?** → Sync dispatch, registration-order, all-or-nothing error aggregation.

## Solution / 解决方案

| Role | Type | Upstream counterpart |
|---|---|---|
| Event payload | `Event` interface (one method: `EventName()`) | `bubus.BaseEvent` |
| Pub-sub core | `*EventBus` (sync.RWMutex + `map[string][]Handler`) | `bubus.EventBus` |
| Watchdog marker | `Watchdog interface{}` | `BaseWatchdog` (Pydantic BaseModel) |
| Auto-discovery | `AutoAttach(w, bus) ([]string, error)` via reflection | `BaseWatchdog.attach_to_session()` |

The whole bus is small enough to inline:

```go
type Handler func(ctx context.Context, e Event) error

type EventBus struct {
    mu       sync.RWMutex
    handlers map[string][]Handler
}

func (b *EventBus) Subscribe(eventName string, handler Handler) {
    b.mu.Lock(); defer b.mu.Unlock()
    b.handlers[eventName] = append(b.handlers[eventName], handler)
}

func (b *EventBus) Emit(ctx context.Context, e Event) error {
    b.mu.RLock()
    hs := make([]Handler, len(b.handlers[e.EventName()]))
    copy(hs, b.handlers[e.EventName()])
    b.mu.RUnlock()
    var errs []error
    for _, h := range hs {
        if err := h(ctx, e); err != nil { errs = append(errs, err) }
    }
    return errors.Join(errs...)
}
```

The reflection auto-attach is the centerpiece:

```go
func AutoAttach(w Watchdog, bus *EventBus) ([]string, error) {
    v := reflect.ValueOf(w); t := v.Type()
    ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
    errType := reflect.TypeOf((*error)(nil)).Elem()
    var registered []string
    for i := 0; i < t.NumMethod(); i++ {
        m := t.Method(i)
        if !strings.HasPrefix(m.Name, "On") || !strings.HasSuffix(m.Name, "Event") { continue }
        eventName := strings.TrimPrefix(m.Name, "On")
        mt := m.Type
        if mt.NumIn() != 3 || mt.In(1) != ctxType { continue }
        ev := mt.In(2)
        if ev.Kind() != reflect.Ptr || ev.Elem().Name() != eventName { continue }
        if mt.NumOut() != 1 || mt.Out(0) != errType { continue }
        // ... close over m, subscribe a type-asserting adaptor
    }
    return registered, nil
}
```

A concrete watchdog is now five lines:

```go
type DownloadsWatchdog struct{ Handled []s06.DownloadStartedEvent }

func (w *DownloadsWatchdog) OnDownloadStartedEvent(ctx context.Context, e *s06.DownloadStartedEvent) error {
    w.Handled = append(w.Handled, *e)
    return nil
}
```

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────────────┐
│                            startup                                   │
│                                                                      │
│   bus := NewEventBus()                                               │
│   dl  := &DownloadsWatchdog{}                                        │
│   pp  := &PopupsWatchdog{}                                           │
│                                                                      │
│   AutoAttach(dl, bus)  ─── reflect ─→  Subscribe("DownloadStarted... │
│   AutoAttach(pp, bus)  ─── reflect ─→  Subscribe("JSDialogOpened...  │
│                                                                      │
│   handlers map:                                                      │
│     "DownloadStartedEvent" → [adaptor → dl.OnDownloadStartedEvent]   │
│     "JSDialogOpenedEvent"  → [adaptor → pp.OnJSDialogOpenedEvent]    │
├──────────────────────────────────────────────────────────────────────┤
│                            run loop                                  │
│                                                                      │
│   bus.Emit(ctx, DownloadStartedEvent{URL: "x"})                      │
│         │                                                            │
│         ▼                                                            │
│   EventBus.Emit                                                      │
│     ┌──────────────────────────────┐                                 │
│     │ RLock                        │                                 │
│     │ snapshot handlers["Down..."] │  (copy under lock so handler    │
│     │ RUnlock                      │   work runs without it)         │
│     │ for h in snapshot:           │                                 │
│     │   err := h(ctx, e)           │  ← reflection adaptor calls     │
│     │   collect err                │     dl.OnDownloadStartedEvent   │
│     │ return errors.Join(errs)     │                                 │
│     └──────────────────────────────┘                                 │
│                                                                      │
│   bus.Emit(ctx, NavigationEvent{...})                                │
│         │                                                            │
│         ▼ (no subscriber)                                            │
│   EventBus.Emit returns nil — events without listeners are dropped.  │
└──────────────────────────────────────────────────────────────────────┘
```

**Four non-obvious points**:

1. **Why reflection-based auto-attach instead of explicit `bus.Subscribe(...)`?** Three reasons. (a) **No-typo guarantee** — the suffix of the method name and the struct name of the argument must match (`OnFooEvent(ctx, *FooEvent)`); the bus refuses to register `OnFooEvent(ctx, *BarEvent)`. Explicit subscriptions catch this at runtime, deep inside Emit. (b) **No registration boilerplate** — adding a new event subscription is "add a method", not "add a method AND remember to subscribe it AND remember to unsubscribe it". (c) **Direct correspondence with upstream Python**, where the same shape `on_EventName` is the contract. A learner reading both repos sees identical mental shapes.

2. **Why run handlers synchronously inside Emit (instead of pushing to a worker goroutine or channel)?** The bus's caller often needs the handler to *finish* before continuing — upstream's agent emits `ClickEvent`, waits for the DOM watchdog to capture the post-click snapshot, then reads it. A buffered channel would return immediately and the agent would race against the snapshot. Sync dispatch + per-handler error makes the contract explicit: "after Emit returns, every handler has finished and you have their errors".

3. **Why sync.RWMutex on a `map[string][]Handler` instead of `chan Event` per topic?** Two reasons. (a) **Snapshot-then-invoke** lets handler work run *outside* the lock — a 3-second download handler does not block other concerns from subscribing. A channel-per-topic with goroutine fan-out gives the same property but adds a goroutine per emit, plus lifecycle management. (b) **No queue means no backpressure choice** — there is no buffer to overflow. Each Emit is "fan out + wait"; the caller controls pacing by *not calling Emit faster than the consumer*. For our teaching scope, that's a feature.

4. **Why `errors.Join` instead of returning on the first error?** Two watchdogs subscribing to the same event must each get a chance. A flaky download handler should not silently mask a popup that needs accepting — the popup must still get its event. `errors.Join` preserves every failure for caller inspection (`errors.Is` can still find specific sentinel errors inside the join).

### ~50 lines of core code

The hot path is small. `Subscribe` (4 lines), `Emit` (12 lines), `AutoAttach` matching loop (~25 lines), plus the closure body (~10 lines). The reflection adaptor at the end of AutoAttach is the trickiest piece:

```go
bus.Subscribe(eventName, func(ctx context.Context, e Event) error {
    ev := reflect.ValueOf(e)
    var ptr reflect.Value
    if ev.Kind() == reflect.Ptr {
        ptr = ev
    } else {
        ptr = reflect.New(ev.Type())
        ptr.Elem().Set(ev)
    }
    out := methodVal.Call([]reflect.Value{reflect.ValueOf(ctx), ptr})
    if errVal := out[0]; !errVal.IsNil() { return errVal.Interface().(error) }
    return nil
})
```

The promote-value-to-pointer step lets callers `Emit(ctx, DownloadStartedEvent{...})` *and* `Emit(ctx, &DownloadStartedEvent{...})` — both work, because the handler method always takes a pointer. Convenience, not correctness.

## What Changed / 与上一节的变化

s05's Element actor talks to the (stub) CDP client directly:

```diff
- // s05-style: side effects inline in the actor methods
- func (e *Element) Click(ctx context.Context) error {
-     // record CDP frame
-     // ... but how do we ALSO notify "downloads might start"?
-     // ... and how do we ALSO notify "popups might open"?
-     return e.client.DispatchMouseEvent(...)
- }
```

s06 onward, side effects move to **watchdog subscribers**:

```diff
+ // s06-style
+ bus := NewEventBus()
+ AutoAttach(&DownloadsWatchdog{}, bus)
+ AutoAttach(&PopupsWatchdog{}, bus)
+
+ // The click action only needs to emit one event:
+ bus.Emit(ctx, ClickEvent{Index: 5})
+ // Downloads handler reacts. Popups handler reacts. They never need
+ // to know about each other — and the click code never needs to know
+ // about either of them.
```

The pivotal new capability: **decoupling by emission**. s05's actor had to know every consumer of its events. s06's actor only knows "the bus". You add a new watchdog without touching a single line of actor code; you remove a watchdog without leaving a dangling reference.

s07 (browser-session) re-uses both pieces: a `BrowserSession` owns the bus and the list of watchdogs, and calls `AutoAttach` on each at start time. The shape carries through.

## Try It / 动手试一试

```bash
cd agents/s06-watchdog-pattern

# auto-registration printout + 5 emits + summary
GOWORK=off go run ./cmd/demo

# 6 tests
GOWORK=off go test -v ./...
```

(`GOWORK=off` because the parent repo's `go.work` is shared across sibling sessions; the module is self-contained on its own `go.mod` and runs fine without the workspace.)

Expected output (verbatim from `testdata/expected.txt`):

```
# auto-registered handlers
DownloadsWatchdog → [DownloadStartedEvent]
PopupsWatchdog    → [JSDialogOpenedEvent]

# emit one event of each type
emitted DownloadStartedEvent
emitted DownloadStartedEvent
emitted JSDialogOpenedEvent
emitted JSDialogOpenedEvent
emitted NavigationEvent

# summary
downloads handled: 2
  - foo.pdf (https://example.com/foo.pdf)
  - bar.zip (https://example.com/bar.zip)
dialogs handled:   2
  - [confirm] "save changes?" → OK
  - [prompt] "your name?" → Cancel
navigation handlers: 0 (unsubscribed events drop silently)
```

Test coverage:

- `TestEmitCallsHandler` — single subscribed handler fires and sees the event payload.
- `TestAutoRegisterByMethodName` — well-shaped `OnDownloadStartedEvent` registers; helper method `LoggerHelper()` is ignored; mis-typed `OnNavigationEvent(*DownloadStartedEvent)` is rejected by the suffix-vs-arg-struct-name check.
- `TestConcurrentEmitsNoDeadlock` — 100 goroutines each call Emit on the same event; both subscribed handlers fire exactly 100 times each, no deadlock.
- `TestUnknownEventIgnored` — emitting `NavigationEvent` with zero subscribers returns nil error.
- `TestMultipleHandlersOrderedRegistration` — three handlers for the same event fire in subscription order: `first → second → third`.
- `TestHandlerErrorsAggregated` — when two of three handlers return errors, `errors.Is` finds both sentinels inside the join.

## Upstream Source Reading / 上游源码阅读

Below is the upstream `BaseWatchdog.attach_to_session()` excerpt from `browser_use/browser/watchdog_base.py#L243-L281`. This is the Python reflection loop our `AutoAttach` directly mirrors.

```python
def attach_to_session(self) -> None:
    """Attach watchdog to its browser session and start monitoring."""
    assert self.browser_session is not None, '...'
    from browser_use.browser import events

    event_classes = {}
    for name in dir(events):
        obj = getattr(events, name)
        if inspect.isclass(obj) and issubclass(obj, BaseEvent) and obj is not BaseEvent:
            event_classes[name] = obj

    # Find all handler methods (on_EventName)
    registered_events = set()
    for method_name in dir(self):
        if method_name.startswith('on_') and callable(getattr(self, method_name)):
            # Extract event name from method name (on_EventName -> EventName)
            event_name = method_name[3:]  # Remove 'on_' prefix

            if event_name in event_classes:
                event_class = event_classes[event_name]

                # ASSERTION: If LISTENS_TO is defined, enforce it
                if self.LISTENS_TO:
                    assert event_class in self.LISTENS_TO, (...)

                handler = getattr(self, method_name)
                self.attach_handler_to_session(self.browser_session, event_class, handler)
                registered_events.add(event_class)
```

**Six reading notes**:

1. **`dir(events)` builds the event lookup table at attach time** — every class defined in `browser_use/browser/events.py` that is a `BaseEvent` subclass becomes eligible. Our Go port skips this map entirely: we infer the expected event-struct *from the handler's argument type*, not from a global table. That tradeoff trades "discover by name" for "discover by signature" — a slightly stronger compile-time check.

2. **`method_name[3:]` is `on_EventName → EventName`.** Our Go port replaces this with `strings.TrimPrefix(name, "On")`. CamelCase-vs-snake_case is the only visible delta.

3. **`if event_name in event_classes` silently skips unrecognised names.** A helper method like `_log_pretty_path` is naturally excluded because there's no `_log_pretty_path` event class. We mirror this in Go: methods not matching the shape are skipped without warning.

4. **`LISTENS_TO` is enforced when declared.** This is Python's way of "the documentation must match reality" — if you declare you listen to `[FooEvent]` but never wrote `on_FooEvent`, the assertion fires. We drop this in Go: declaring a `LISTENS_TO` slice would add ceremony with no compile-time benefit. The Go shape — "your method's argument *is* the event you listen to" — already binds documentation to reality.

5. **`attach_handler_to_session` wraps the bound method in `unique_handler`** (defined upstream at L93-L207). That wrapper adds (a) a circuit-breaker that returns early when CDP is disconnected, (b) parent-event tracing for debug logs, (c) automatic CDP session repair on handler error. Our Go port omits all three: (a) is a side-effect-vs-side-effect concern we don't simulate; (b) is observability noise; (c) belongs to the s07 session lifecycle, not the bus.

6. **No explicit `event_bus.on(...)` calls anywhere in `browser_use/browser/watchdogs/*.py`.** Each watchdog file just defines `on_XxxEvent` methods; the framework finds them. That low-ceremony shape is the entire pragmatic argument for the pattern — see `docs/{zh,en}/s06` §3 point 1. The Go port replicates this exactly: no `Subscribe` call appears in `watchdogs/downloads.go` or `watchdogs/popups.go`, only the handler methods.

For the full annotated excerpt including the class-shell pattern and the full attach loop, see `upstream-readings/s06-watchdog-pattern.py`.
