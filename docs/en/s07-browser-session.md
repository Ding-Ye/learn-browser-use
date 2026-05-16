---
title: "s07 · Browser session"
chapter: 7
slug: s07-browser-session
est_read_min: 15
---

# s07 · Browser session

> Teaching focus: s05 left us with a stub `CDPClient`. s06 left us with an `EventBus` plus a `Watchdog` interface. Those two lines never met. s07 fuses them inside a `BrowserSession` — one container that owns the client, owns the bus, owns the watchdogs, and exposes `Start` / `Stop` / `Restart` / `IsRunning`. About 500 lines of Go in total, ~80 in `session.go` itself; the rest is tests and docs.

---

## Problem / 问题

By the end of s06 the toolbox looks like this:

```go
// s06-style usage
bus := NewEventBus()
wd1 := &DownloadsWatchdog{...}
wd2 := &PopupsWatchdog{...}
AutoAttach(bus, wd1)
AutoAttach(bus, wd2)
// ... the caller is on the hook for cleanup
```

The bus is bare, the watchdogs are free-floating, and the CDP plumbing (s05's `RecordingCDPClient`) sits in a third place that knows nothing about either. Three things in three locations, glued together at every call site by hand.

Concrete pain:

1. **Nobody owns the lifecycle.** Neither bus nor watchdogs know whether CDP is connected. CDP doesn't know whether the watchdogs are wired up. If one dies, the other two leak.
2. **No idempotent Start.** Callers have to implement "if already started, skip" themselves — the upstream Python `on_BrowserStartEvent` docstring literally says "This method is idempotent", which means it's a real-world footgun.
3. **No explicit Stop.** Watchdog handlers stay subscribed forever. The next Start re-subscribes them, every event fires twice, and the test that catches this is the one you forget to write.
4. **No `Restart`.** Callers do need "reset everything and try again", but in the free-floating version it's 6 lines of manual glue every time.

s07 fixes all four. The shape is unsurprising for Go: one struct that holds the three pieces, methods that expose the lifecycle.

## Solution / 解决方案

Introduce `BrowserSession`, a composition container:

```go
type BrowserSession struct {
    Client    CDPClient   // stub CDP (concept from s05, re-declared locally)
    Bus       *EventBus   // event bus (concept from s06, re-declared locally)
    Watchdogs []Watchdog  // a slice of attached watchdogs
    Started   bool
}
```

…with four lifecycle methods:

| Method | Responsibility | Upstream analog |
|---|---|---|
| `Start(ctx)`    | Open stub CDP (send `Target.attachToTarget`) + AutoAttach every watchdog + emit `SessionStartedEvent`. **Idempotent**. | `BrowserSession.start()` + `on_BrowserStartEvent` |
| `Stop(ctx)`     | Emit `SessionStoppedEvent` + send `Target.detachFromTarget` + clear bus subscriptions. **Idempotent**. | `BrowserSession.stop()` |
| `Restart(ctx)`  | Stop then Start. Whole state machine cycles. | Caller composes `await session.stop(); await session.start()` manually upstream |
| `IsRunning()`   | Getter for `Started`. Method form leaves room for future ctx/locking. | Distilled `is_cdp_connected` |

To stay self-contained (the learn-browser-use rule: **no cross-session imports**), we re-declare three concepts from earlier chapters locally:

| Local file | Re-declared concept | Originally in |
|---|---|---|
| `eventbus.go`   | `EventBus` (Subscribe/Emit/HandlerCount/Clear)   | s06 |
| `watchdog.go`   | `Watchdog` interface + `AutoAttach` reflection   | s06 |
| `cdp_client.go` | `CDPClient` interface + `RecordingCDPClient`     | s05 |

Note that `AutoAttach` here gets one extra wrinkle vs s06: a **JSON bridge**. Because `watchdogs/example.go` is a subpackage, it cannot import the parent `main` package. So the event structs it declares are *different Go types* from those in `event.go`. AutoAttach copies the emitted event into the watchdog's parameter type via `json.Marshal` + `json.Unmarshal`. Identical field names → faithful copy.

## How It Works / 工作原理

```
┌─────────────────────────────────────────────────────────────────┐
│                      session = NewBrowserSession                │
│   ┌─────────────┐    ┌─────────┐    ┌────────────────────┐      │
│   │ CDPClient   │    │ EventBus│    │ Watchdogs []        │     │
│   │ (Recording) │    │         │    │   LoggingWatchdog   │     │
│   └─────────────┘    └─────────┘    └────────────────────┘      │
└─────────────────────────────────────────────────────────────────┘

  session.Start(ctx)
  ─────────────────►
                                                                       Started=false→true
       1. Client.Send("Target.attachToTarget", {...})   ──► Frames[0]
       2. for w in Watchdogs: AutoAttach(Bus, w)
            ├─ reflection looks for "On*" methods
            ├─ method name suffix becomes the event key
            └─ each handler gets a JSON-bridging closure
       3. Bus.Emit(ctx, SessionStartedEvent{CDPURL:...}) ─► watchdog.OnSessionStartedEvent()

  session.Navigate(ctx, "https://example.com")
  ───────────────────────────────────────────►
       Bus.Emit(ctx, NavigationEvent{URL:...})         ─► watchdog.OnNavigationEvent()

  session.Stop(ctx)
  ─────────────────►                                                   Started=true→false
       1. Bus.Emit(ctx, SessionStoppedEvent{...})       ─► watchdog.OnSessionStoppedEvent()
       2. Client.Send("Target.detachFromTarget", {...}) ──► Frames[1]
       3. Bus.Clear()                                                  handlers = {}
```

Core code (~50 lines):

```go
// session.go
func (s *BrowserSession) Start(ctx context.Context) error {
    s.mu.Lock()
    if s.Started { s.mu.Unlock(); return nil }     // ← idempotency

    if _, err := s.Client.Send("Target.attachToTarget", map[string]any{
        "targetId": "stub-target-0",
        "flatten":  true,
    }); err != nil {
        s.mu.Unlock()
        return fmt.Errorf("session start: %w", err)
    }
    for _, w := range s.Watchdogs {                // ← attach all watchdogs in one loop
        AutoAttach(s.Bus, w)
    }
    s.Started = true
    s.mu.Unlock()
    return s.Bus.Emit(ctx, SessionStartedEvent{CDPURL: "stub://recorder"})
}

func (s *BrowserSession) Stop(ctx context.Context) error {
    s.mu.Lock()
    if !s.Started { s.mu.Unlock(); return nil }    // ← idempotency
    s.Started = false
    s.mu.Unlock()

    emitErr := s.Bus.Emit(ctx, SessionStoppedEvent{Reason: "Stop() called"})
    _, sendErr := s.Client.Send("Target.detachFromTarget", map[string]any{
        "sessionId": "stub-session-0",
    })
    s.Bus.Clear()                                  // ← mirrors upstream self.event_bus = EventBus()

    if emitErr != nil { return emitErr }
    if sendErr != nil { return fmt.Errorf("session stop: %w", sendErr) }
    return nil
}
```

**Four non-obvious points**:

1. **Why a `Started` field instead of `sync.Once`?** `sync.Once` runs the body exactly once forever; we need Start → Stop → Start (Restart) to work. We want "skip if running", not "fire once per program lifetime".
2. **Why Emit `SessionStoppedEvent` *before* `Bus.Clear()`?** Clear-then-Emit would mean no watchdog ever sees the stop — their cleanup handlers never run. Order is: announce "I'm stopping, run your cleanup", then clear the bus.
3. **Why AutoAttach inside the loop instead of in `NewBrowserSession`?** Because `Stop` clears the bus. If AutoAttach only ran in `New`, the second Start would find an empty bus and every watchdog would be silent. Re-attaching on every Start is the cost of a clean state-machine cycle, and it's why upstream's `attach_all_watchdogs()` runs every start.
4. **Why the JSON bridge instead of one shared event type?** Because `watchdogs/example.go` is a subpackage that cannot import the parent `main` package (Go forbids circular package deps). Two structs with identical field names live in two packages; AutoAttach marshals the source and unmarshals into the destination's type. The cost is a sub-microsecond round-trip per event; the gain is the subpackage genuinely builds in isolation. In a production layout you would hoist event types into a shared `events` subpackage, but the s07 lesson is about session-owns-watchdogs, so we accept the small overhead for readability.

## What Changed / 与上一节的变化

s06 style:

```diff
- // s06 usage
- bus := NewEventBus()
- wd := &MyWatchdog{...}
- AutoAttach(bus, wd)
- bus.Emit(ctx, MyEvent{...})
- // ... caller assembles the lifecycle / remembers cleanup
```

After s07:

```diff
+ session := NewBrowserSession(client, wd1, wd2)
+ defer session.Stop(ctx)            // ← one-line safety net
+ if err := session.Start(ctx); err != nil { return err }
+ session.Navigate(ctx, url)         // ← events routed via session
+ // session.IsRunning() exposes state
+ // session.Restart(ctx) resets in one call
```

The headline gain: **lifecycle is now an explicit object**. In s01–s06 every demo glued "start → work → stop" together by hand, and each one looked slightly different. After s07 it's the same four-line API everywhere. This is the inflection point between "event-bus pattern" and "session-object pattern".

This chapter's outputs get reused downstream:

- s09's `DOMService` will subscribe to `NavigationEvent` to invalidate its snapshot cache.
- s12's Agent will own a `BrowserSession` and bracket `agent.Run()` with `session.Start` / `session.Stop`.

## Try It / 动手试一试

```bash
cd agents/s07-browser-session

# Full session-lifecycle demo
GOWORK=off go run .

# 6 tests
GOWORK=off go test -v ./...
```

(`GOWORK=off` is just because the root `go.work` doesn't list s07 yet — the module is self-contained and builds without the workspace.)

Expected output (excerpt):

```
# session lifecycle demo

Initial state: IsRunning=false
After Start:   IsRunning=true  handlers(SessionStartedEvent)=1
After Stop:    IsRunning=false  handlers(SessionStartedEvent)=0

# CDP frame log (recorded by the stub client)
[0] Target.attachToTarget map[flatten:true targetId:stub-target-0]
[1] Target.detachFromTarget map[sessionId:stub-session-0]

# Watchdog event log (recorded by LoggingWatchdog)
[0] started: cdp=stub://recorder
[1] navigate: url=https://example.com
[2] stopped: reason=Stop() called
```

Test coverage:

- `TestStartOpensStubCDP` — Start must send `Target.attachToTarget`.
- `TestWatchdogsAttachOnStart` — after Start, every relevant event has exactly one handler.
- `TestStopDisconnectsCleanly` — Stop must broadcast `SessionStoppedEvent` and send the detach frame.
- `TestRestartWorks` — Stop+Start cycle returns the session to running and re-attaches every handler.
- `TestStartIdempotent` — calling Start twice does NOT double attach frames or handler counts.
- `TestNavigateRoutesThroughBus` (bonus) — `session.Navigate` refuses before Start, succeeds after, and the watchdog observes the event.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/browser/session.py::BrowserSession` is ~4000 lines: cloud integration, profile handling, multi-target session management, WebSocket reconnect logic, demo mode, etc. s07 only takes the skeleton.

```python
# Source: browser_use/browser/session.py#L101-L130, L502-L535

class BrowserSession(BaseModel):
    """Event-driven browser session with backwards compatibility."""

    model_config = ConfigDict(
        arbitrary_types_allowed=True,
        validate_assignment=True,
        extra='forbid',
        revalidate_instances='never',
    )

    # ↓ Corresponds to our Go Session.Bus field. The bus is OWNED by
    #   the session, not by the agent.
    event_bus: EventBus = Field(default_factory=EventBus)

    # Watchdog slots — upstream gives each watchdog a named field;
    # we use a []Watchdog slice.
    _crash_watchdog: Any | None = PrivateAttr(default=None)
    _downloads_watchdog: Any | None = PrivateAttr(default=None)
    _dom_watchdog: Any | None = PrivateAttr(default=None)
    # ... 12 watchdog fields total ...
    _watchdogs_attached: bool = PrivateAttr(default=False)
```

```python
# Source: browser_use/browser/session.py#L672-L725

@observe_debug(ignore_input=True, ignore_output=True, name='browser_session_start')
async def start(self) -> None:
    """Start the browser session."""
    # ↓ Corresponds to our Go Start():
    #     Client.Send + AutoAttach + Bus.Emit.
    #   Upstream collapses all three into a single event dispatch.
    start_event = self.event_bus.dispatch(BrowserStartEvent())
    await start_event
    await start_event.event_result(raise_if_any=True, raise_if_none=False)


async def stop(self) -> None:
    """Stop the browser session without killing the browser process."""
    self._intentional_stop = True

    # Save storage state before stopping. s07 has no storage layer,
    # so this step disappears.
    save_event = self.event_bus.dispatch(SaveStorageStateEvent())
    await save_event

    # ↓ Corresponds to our Bus.Emit(SessionStoppedEvent)
    await self.event_bus.dispatch(BrowserStopEvent(force=False))
    # ↓ Corresponds to our Bus.Clear()
    await self.event_bus.stop(clear=True, timeout=5)
    await self.reset()
    # ↓ Mirrors our re-attach-on-next-Start choreography.
    self.event_bus = EventBus()


async def on_BrowserStartEvent(self, event: BrowserStartEvent) -> dict[str, str]:
    """Handle browser start request.

    Note: This method is idempotent - calling start() multiple times is safe.
    """
    # ↓ Corresponds to our Start():
    #     `for _, w := range s.Watchdogs { AutoAttach(s.Bus, w) }`
    await self.attach_all_watchdogs()

    # The block below is the local/cloud/CDP-URL branch. s07 elides
    # it: the "CDP" is RecordingCDPClient — there's nothing to launch.
    try:
        if not self.cdp_url:
            if self.browser_profile.use_cloud:
                ...  # cloud browser
            elif self.is_local:
                ...  # dispatch BrowserLaunchEvent, LocalBrowserWatchdog launches Chromium
    except CloudBrowserError:
        raise
```

```python
# Source: browser_use/browser/session.py#L1593-L1696 (excerpts)

async def attach_all_watchdogs(self) -> None:
    # The pattern, per watchdog, is three lines:
    #   1. cls.model_rebuild()   ← pydantic late-binding fix
    #   2. self._xxx_watchdog = XxxWatchdog(event_bus=self.event_bus, browser_session=self)
    #   3. self._xxx_watchdog.attach_to_session()  ← reflection registers on_* methods

    DownloadsWatchdog.model_rebuild()
    self._downloads_watchdog = DownloadsWatchdog(event_bus=self.event_bus, browser_session=self)
    self._downloads_watchdog.attach_to_session()

    LocalBrowserWatchdog.model_rebuild()
    self._local_browser_watchdog = LocalBrowserWatchdog(event_bus=self.event_bus, browser_session=self)
    self._local_browser_watchdog.attach_to_session()

    # ... repeated 10 more times for the other watchdogs ...

    self._watchdogs_attached = True
```

**Reading notes**:

1. **`start()` → dispatch event → `on_BrowserStartEvent` → `attach_all_watchdogs()`**: upstream uses an event hop to decouple "user calls start" from "what start actually does". The same `start()` entry handles local Chromium, cloud browsers, and external CDP URLs — each path is a different handler. Our Go `Session.Start` calls the stub CDP directly because there's only one launch path to support.
2. **`@observe_debug` decorator**: telemetry hook (laminar.so). s10 picks up token cost / observability; here it's just a name.
3. **`_watchdogs_attached: bool`**: upstream uses this flag to avoid double-attach. We achieve the same effect with `Started bool` plus the early-return at the top of Start — one field fewer.
4. **`self.event_bus = EventBus()` in `stop()`**: upstream swaps in a fresh bus object. We `Bus.Clear()` instead of reassigning `s.Bus`, because external code holds pointers to the bus (the demo's `session.Bus.HandlerCount(...)` line). Reassigning would silently invalidate them. Functionally equivalent for the bus's state, but the model is different.
5. **`reset()` (~50 lines)**: upstream's reset cancels reconnect tasks, closes the CDP WebSocket, clears the SessionManager, nils out all 12 watchdog fields. We only set `Started=false` — `Bus.Clear()` handles subscriptions. Different cost because different surface area.
6. **`kill()` vs `stop()`**: upstream's stop is "gentle"; kill is "force=True". s07 only ships stop because the stub has no process to kill.

**Where to read next**: start at `BrowserSession.__init__` (~150 lines of param plumbing) for the surface area, then jump to `model_post_init` (line 642) and notice it calls `BaseWatchdog.attach_handler_to_session` 8 times — the session is also an implicit watchdog, registering its own handlers for BrowserStart/Stop/Navigate/etc. From there you can follow the wire into s12: `agent.session.start()` is the first line of `agent.run()`.

---

**Next up**: s08 introduces the DOM serializer — `DOMNode` tree + LLM-friendly text + selector map. s08 doesn't depend on s07's session (the serializer is a pure data transformer), but s09 will plug the serializer into a session: when a `NavigationEvent` arrives, the snapshot cache invalidates. That bridge is what s07's lifecycle made possible.
