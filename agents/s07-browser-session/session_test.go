package main

import (
	"context"
	"strings"
	"testing"

	"learn-browser-use/s07-browser-session/watchdogs"
)

// session_test.go covers the five lifecycle invariants the README
// promises. Each test builds its own session so we don't share state
// across t.Run cases — the lifecycle bookkeeping is exactly what we
// want to test, and shared mutable state would mask bugs.

// TestStartOpensStubCDP — Start must send the canonical
// Target.attachToTarget frame. The recorder is the only oracle: a
// real WebSocket-backed session would have an additional layer to
// mock, here we just inspect Frames.
func TestStartOpensStubCDP(t *testing.T) {
	client := NewRecordingCDPClient()
	s := NewBrowserSession(client)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if len(client.Frames) != 1 {
		t.Fatalf("expected exactly 1 CDP frame after Start, got %d: %v",
			len(client.Frames), client.Frames)
	}
	if client.Frames[0].Method != "Target.attachToTarget" {
		t.Errorf("first frame method = %q, want Target.attachToTarget",
			client.Frames[0].Method)
	}
	if !s.IsRunning() {
		t.Errorf("IsRunning() = false after successful Start, want true")
	}
}

// TestWatchdogsAttachOnStart — every watchdog's matching handlers
// must be subscribed on the bus by the time Start returns. We check
// the bus's handler count directly rather than emitting + asserting
// downstream effects, because the test should fail at the *attach*
// step regardless of whether the watchdog body works.
func TestWatchdogsAttachOnStart(t *testing.T) {
	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()
	s := NewBrowserSession(client, logger)

	// Before Start, no handlers should be subscribed.
	if got := s.Bus.HandlerCount("SessionStartedEvent"); got != 0 {
		t.Errorf("pre-Start SessionStartedEvent handlers = %d, want 0", got)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// LoggingWatchdog has three On* methods → three subscriptions.
	wantEvents := []string{
		"SessionStartedEvent",
		"SessionStoppedEvent",
		"NavigationEvent",
	}
	for _, evt := range wantEvents {
		if got := s.Bus.HandlerCount(evt); got != 1 {
			t.Errorf("post-Start handler count for %q = %d, want 1", evt, got)
		}
	}

	// And the watchdog actually observed the SessionStartedEvent.
	events := logger.Snapshot()
	if len(events) != 1 || !strings.HasPrefix(events[0], "started:") {
		t.Errorf("watchdog should have logged one 'started:' event, got %v", events)
	}
}

// TestStopDisconnectsCleanly — Stop must fire SessionStoppedEvent
// (so the watchdog records it) AND send Target.detachFromTarget on
// the recorder. Symmetric attach/detach pairing is the canonical
// "we cleaned up after ourselves" signal.
func TestStopDisconnectsCleanly(t *testing.T) {
	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()
	s := NewBrowserSession(client, logger)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The recorder should now have BOTH attach and detach frames.
	if len(client.Frames) != 2 {
		t.Fatalf("expected 2 CDP frames after Start+Stop, got %d", len(client.Frames))
	}
	if client.Frames[1].Method != "Target.detachFromTarget" {
		t.Errorf("second frame method = %q, want Target.detachFromTarget",
			client.Frames[1].Method)
	}

	if s.IsRunning() {
		t.Errorf("IsRunning() = true after Stop, want false")
	}

	// The watchdog should have seen both lifecycle events.
	events := logger.Snapshot()
	if len(events) < 2 {
		t.Fatalf("watchdog event count = %d, want at least 2", len(events))
	}
	last := events[len(events)-1]
	if !strings.HasPrefix(last, "stopped:") {
		t.Errorf("last watchdog event = %q, want stopped:* prefix", last)
	}

	// Bus subscriptions should be cleared so a fresh Start has a clean slate.
	if got := s.Bus.HandlerCount("SessionStartedEvent"); got != 0 {
		t.Errorf("post-Stop SessionStartedEvent handlers = %d, want 0", got)
	}
}

// TestRestartWorks — Restart is Stop+Start composed. After Restart
// the session must be running again AND the watchdog must be wired
// back up (Stop cleared the bus; Start has to re-Attach).
func TestRestartWorks(t *testing.T) {
	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()
	s := NewBrowserSession(client, logger)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if !s.IsRunning() {
		t.Errorf("IsRunning() = false after Restart, want true")
	}

	// Frames so far: attach, detach (from Stop inside Restart), attach.
	wantMethods := []string{
		"Target.attachToTarget",
		"Target.detachFromTarget",
		"Target.attachToTarget",
	}
	if len(client.Frames) != len(wantMethods) {
		t.Fatalf("frame count after Restart = %d, want %d (%v)",
			len(client.Frames), len(wantMethods), client.Frames)
	}
	for i, want := range wantMethods {
		if client.Frames[i].Method != want {
			t.Errorf("frame[%d].Method = %q, want %q",
				i, client.Frames[i].Method, want)
		}
	}

	// The bus should once again have all three watchdog handlers attached.
	for _, evt := range []string{"SessionStartedEvent", "SessionStoppedEvent", "NavigationEvent"} {
		if got := s.Bus.HandlerCount(evt); got != 1 {
			t.Errorf("post-Restart handler count for %q = %d, want 1", evt, got)
		}
	}

	// And the watchdog should have caught both started events (initial + restart).
	events := logger.Snapshot()
	startedCount := 0
	for _, e := range events {
		if strings.HasPrefix(e, "started:") {
			startedCount++
		}
	}
	if startedCount != 2 {
		t.Errorf("watchdog observed %d started events across Restart, want 2 (snapshot=%v)",
			startedCount, events)
	}
}

// TestStartIdempotent — calling Start twice must NOT double-subscribe
// the watchdog's handlers. This is the contract the upstream comment
// makes ("This method is idempotent — calling start() multiple times
// is safe") and a real risk: a careless retry loop that calls Start
// in a retry-on-error path would otherwise double every emit.
func TestStartIdempotent(t *testing.T) {
	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()
	s := NewBrowserSession(client, logger)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	// Idempotency assertion #1: only ONE attach frame was sent.
	attachCount := 0
	for _, f := range client.Frames {
		if f.Method == "Target.attachToTarget" {
			attachCount++
		}
	}
	if attachCount != 1 {
		t.Errorf("Target.attachToTarget sent %d times, want 1 (Start is supposed to be idempotent)",
			attachCount)
	}

	// Idempotency assertion #2: each event has exactly one handler.
	for _, evt := range []string{"SessionStartedEvent", "SessionStoppedEvent", "NavigationEvent"} {
		if got := s.Bus.HandlerCount(evt); got != 1 {
			t.Errorf("after double-Start, handler count for %q = %d, want 1 (no double-attach)",
				evt, got)
		}
	}

	// Idempotency assertion #3: SessionStartedEvent was emitted exactly once.
	// (the second Start should be a no-op all the way down)
	startedSeen := 0
	for _, e := range logger.Snapshot() {
		if strings.HasPrefix(e, "started:") {
			startedSeen++
		}
	}
	if startedSeen != 1 {
		t.Errorf("watchdog observed %d started events, want 1", startedSeen)
	}
}

// TestNavigateRoutesThroughBus — bonus 6th test (we promised "at
// least 5"). Verifies that Navigate fires a NavigationEvent the
// watchdog observes — which is the mid-run behaviour distinct from
// Start/Stop. This is what would let s09's DOMService invalidate
// its snapshot cache on real navigation.
func TestNavigateRoutesThroughBus(t *testing.T) {
	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()
	s := NewBrowserSession(client, logger)
	ctx := context.Background()

	// Navigate before Start should refuse — that's part of the
	// state-machine contract.
	if err := s.Navigate(ctx, "https://example.com"); err == nil {
		t.Errorf("Navigate before Start should error, got nil")
	}

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Navigate(ctx, "https://example.com/page"); err != nil {
		t.Errorf("Navigate after Start: %v", err)
	}

	events := logger.Snapshot()
	saw := false
	for _, e := range events {
		if strings.HasPrefix(e, "navigate: url=https://example.com/page") {
			saw = true
			break
		}
	}
	if !saw {
		t.Errorf("watchdog never observed navigate event, snapshot=%v", events)
	}
}
