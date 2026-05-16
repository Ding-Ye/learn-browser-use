package main

import (
	"context"
	"fmt"
	"os"

	"learn-browser-use/s07-browser-session/watchdogs"
)

// s07-browser-session demo binary.
//
// What it does:
//  1. Build a recording CDP client + one LoggingWatchdog.
//  2. Wire them into a BrowserSession.
//  3. Start the session (records Target.attachToTarget + fires
//     SessionStartedEvent + attaches the watchdog's three handlers).
//  4. Emit one NavigationEvent so the demo shows the bus carrying
//     mid-run traffic.
//  5. Stop the session (records Target.detachFromTarget + fires
//     SessionStoppedEvent + clears the bus).
//  6. Print both ledgers — the CDP frame log AND the watchdog event
//     log — so the reader can see "what went over the wire" and
//     "what the watchdog observed" side by side.
//
// Run: `go run .`
//
// The point of the dual ledger is to make the lifecycle observable.
// In s06 the EventBus was a free-floating thing; here it's *owned* by
// a session, and the session's Start/Stop are what orchestrate the
// whole choreography.
func main() {
	ctx := context.Background()

	client := NewRecordingCDPClient()
	logger := watchdogs.NewLoggingWatchdog()

	session := NewBrowserSession(client, logger)

	fmt.Println("# session lifecycle demo")
	fmt.Println()
	fmt.Printf("Initial state: IsRunning=%v\n", session.IsRunning())

	if err := session.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Start failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("After Start:   IsRunning=%v  handlers(SessionStartedEvent)=%d\n",
		session.IsRunning(),
		session.Bus.HandlerCount("SessionStartedEvent"),
	)

	if err := session.Navigate(ctx, "https://example.com"); err != nil {
		fmt.Fprintf(os.Stderr, "Navigate failed: %v\n", err)
		os.Exit(1)
	}

	if err := session.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Stop failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("After Stop:    IsRunning=%v  handlers(SessionStartedEvent)=%d\n",
		session.IsRunning(),
		session.Bus.HandlerCount("SessionStartedEvent"),
	)

	fmt.Println()
	fmt.Println("# CDP frame log (recorded by the stub client)")
	fmt.Print(client.FrameLog())

	fmt.Println()
	fmt.Println("# Watchdog event log (recorded by LoggingWatchdog)")
	for i, ev := range logger.Snapshot() {
		fmt.Printf("[%d] %s\n", i, ev)
	}
}
