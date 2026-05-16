package main

import (
	"context"
	"fmt"
)

// lifecycle.go holds the helpers that sit *next to* Session.Start /
// Session.Stop without crowding session.go. There are two:
//
//   - RunUntilCancelled wraps a session so it auto-Stops when ctx is
//     cancelled. Real systems install a SIGINT handler that cancels
//     ctx; the session then unwinds cleanly without a separate
//     "did anyone remember to call Stop" question. Upstream's
//     `_setup_signal_handler` in Agent.run does the same thing one
//     layer up.
//
//   - Navigate is a tiny convenience that emits a NavigationEvent
//     through the bus. Watchdogs subscribed to NavigationEvent will
//     hear it. This is the "mid-run" event used in the demo and the
//     restart test, separate from the lifecycle events.
//
// Putting these next to session.go (rather than in main.go) makes
// them re-usable by tests and keeps the demo binary skeletal.

// RunUntilCancelled starts the session, blocks until ctx is Done(),
// and then guarantees Stop has been called. The returned error is
// either the Start error, the ctx error, or the Stop error — whichever
// is most informative.
//
// Usage in main(): start a session, then `RunUntilCancelled(sigCtx, s)`
// and the binary exits cleanly on Ctrl-C. We don't actually wire
// signals in main() for s07 (the demo is finite) but the helper is
// here so the lifecycle pattern is teachable.
func RunUntilCancelled(ctx context.Context, s *BrowserSession) error {
	if err := s.Start(ctx); err != nil {
		return fmt.Errorf("RunUntilCancelled: start: %w", err)
	}

	<-ctx.Done()

	// Use a fresh background ctx for Stop — the caller's ctx is
	// already cancelled, and we *do* want Stop to complete its work
	// rather than short-circuit on a dead context. This is the same
	// pattern Go's http.Server.Shutdown(context.Background()) uses.
	if err := s.Stop(context.Background()); err != nil {
		return fmt.Errorf("RunUntilCancelled: stop: %w", err)
	}
	return ctx.Err()
}

// Navigate emits a NavigationEvent through the session's bus. The
// helper exists so callers don't have to remember "construct the
// struct, call bus.Emit" each time, and so the test for "watchdog
// sees a NavigationEvent between Start and Stop" reads naturally.
//
// Returning the bus emit error lets callers branch on watchdog
// failures, though most test code just discards it.
func (s *BrowserSession) Navigate(ctx context.Context, url string) error {
	if !s.IsRunning() {
		return fmt.Errorf("Navigate: session not running (call Start first)")
	}
	return s.Bus.Emit(ctx, NavigationEvent{URL: url})
}
