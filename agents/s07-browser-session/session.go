package main

import (
	"context"
	"fmt"
	"sync"
)

// session.go is the heart of s07: the BrowserSession struct that owns
// the CDP client, the EventBus, and the list of attached Watchdogs.
//
// Upstream analog: `class BrowserSession(BaseModel)` in
// browser_use/browser/session.py, ~4,000 LOC stretching across cloud
// integration, profile handling, multi-target lifecycle, and a dozen
// other concerns. We keep the *shape* — Client + Bus + Watchdogs in
// one container, Start/Stop methods — and discard everything else.
// The deliberate omissions (no real CDP WebSocket, no profile manager,
// no cloud auth) are called out in the README and s_full's omission
// table.
//
// Three lifecycle methods that matter for teaching:
//
//   - Start(ctx)  : open the (stub) CDP connection, AutoAttach every
//                   Watchdog onto the bus, fire SessionStartedEvent.
//                   Idempotent — calling twice is safe and does NOT
//                   double-attach.
//   - Stop(ctx)   : fire SessionStoppedEvent, issue Target.detachFromTarget
//                   on the stub, clear bus handlers. After Stop the
//                   session is in the same state as right after
//                   NewBrowserSession().
//   - Restart(ctx): Stop followed by Start. The whole point is to
//                   demonstrate that the state-machine cleanly cycles.

// BrowserSession bundles the three things a real session needs:
//
//   - Client    : the CDP transport (stubbed here; in s12 you'd swap
//                 in a chromedp-backed implementation by satisfying
//                 the CDPClient interface).
//   - Bus       : the in-process pub/sub coordinating watchdogs.
//   - Watchdogs : domain-specific event handlers that observe and
//                 occasionally react to lifecycle + work events.
//
// Started is the lifecycle bit: false until Start succeeds, false
// again after Stop. We surface it as an exported field rather than
// hiding it behind IsRunning() alone so test code can read state
// directly without method-call ceremony.
type BrowserSession struct {
	Client    CDPClient
	Bus       *EventBus
	Watchdogs []Watchdog

	mu      sync.Mutex
	Started bool
}

// NewBrowserSession composes a session with sane defaults. The bus is
// created here rather than expecting the caller to supply one, because
// the bus and the watchdogs need to share lifetime semantics — if the
// caller built their own bus and forgot to Clear it on Stop, the
// session would silently misbehave.
//
// Watchdogs are received by variadic so the common usage
// `NewBrowserSession(client, w1, w2)` reads naturally; you can also
// build a session with zero watchdogs (useful for tests that only
// care about the CDP recorder).
func NewBrowserSession(client CDPClient, watchdogs ...Watchdog) *BrowserSession {
	return &BrowserSession{
		Client:    client,
		Bus:       NewEventBus(),
		Watchdogs: watchdogs,
	}
}

// Start opens the (stub) CDP connection and attaches every watchdog
// onto the bus. The implementation is:
//
//  1. If already Started, return nil. This is the idempotency the
//     upstream comment ("Note: This method is idempotent…") promises.
//  2. Send Target.attachToTarget — the canonical first frame any CDP
//     session sends. With the recorder, this is what the test for
//     "Start actually opened a CDP connection" looks at.
//  3. AutoAttach every watchdog. Order matters in the upstream Python
//     (LocalBrowserWatchdog must be ready before BrowserLaunchEvent
//     fires) — we attach in the slice order the caller gave us.
//  4. Emit SessionStartedEvent so watchdogs that want to "do something
//     on startup" hear about it.
//
// ctx is honoured to two places: the Emit call (so a watchdog handler
// can see ctx.Done()) and the lifecycle marker. We don't actually
// abort mid-attach on ctx cancellation — attaching is cheap and racing
// against the canceller buys little. Real implementations might want
// to bail mid-launch.
func (s *BrowserSession) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.Started {
		s.mu.Unlock()
		return nil
	}

	// Stub CDP connect — the upstream Python does roughly:
	//   await self.cdp_client.send.Target.attachToTarget(...)
	// We mimic that single round-trip with the recorder client. The
	// SessionID is fake but realistic enough that watchdogs that
	// care could read it off the result map.
	result, err := s.Client.Send("Target.attachToTarget", map[string]any{
		"targetId": "stub-target-0",
		"flatten":  true,
	})
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("session start: CDP attach failed: %w", err)
	}
	_ = result // intentionally unused — the stub returns {"ok": true}

	// Auto-attach all watchdogs to the bus.
	for _, w := range s.Watchdogs {
		AutoAttach(s.Bus, w)
	}

	s.Started = true
	s.mu.Unlock()

	// Emit *outside* the lock so handlers that re-enter the session
	// (e.g. inspecting IsRunning) don't deadlock.
	return s.Bus.Emit(ctx, SessionStartedEvent{CDPURL: "stub://recorder"})
}

// Stop reverses Start. The sequence is the inverse of Start:
//
//  1. If not Started, return nil. Stopping an already-stopped session
//     is a no-op — same reason Start is idempotent.
//  2. Emit SessionStoppedEvent first, *before* tearing the bus down,
//     so watchdog cleanup handlers actually run.
//  3. Send Target.detachFromTarget so the recorder shows symmetric
//     attach/detach pairs.
//  4. Clear bus subscriptions so the next Start begins clean — this
//     mirrors upstream's `self.event_bus = EventBus()` reset.
//
// We accept ctx so future implementations could honour a shutdown
// deadline (the upstream Python uses `timeout=5` in event_bus.stop).
// For the stub, ctx is just passed to Emit.
func (s *BrowserSession) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.Started {
		s.mu.Unlock()
		return nil
	}
	s.Started = false
	s.mu.Unlock()

	// Emit stopped event BEFORE clearing handlers — otherwise the
	// watchdogs would never hear about the stop. Capture the error
	// from Emit so it bubbles up but don't let it skip the detach
	// frame; we want symmetry in the recorder even if a handler
	// returned an error.
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

// Restart is the obvious composition of Stop + Start. We expose it
// as its own method because (a) it has its own test and (b) upstream
// promotes a similar `await session.stop(); await session.start()`
// pattern as the canonical way to recover from a stuck state.
func (s *BrowserSession) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return fmt.Errorf("restart: stop phase: %w", err)
	}
	if err := s.Start(ctx); err != nil {
		return fmt.Errorf("restart: start phase: %w", err)
	}
	return nil
}

// IsRunning is the read-side of Started. Existing as a method (rather
// than only the exported field) keeps callers honest under future
// changes: when we tighten the model to require ctx-cancellation
// awareness, the field can become private and the method's signature
// can grow.
func (s *BrowserSession) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Started
}
