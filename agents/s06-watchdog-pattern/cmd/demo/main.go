// Command s06-watchdog-pattern is a tiny demo of the event bus + two
// auto-registered watchdogs. Equivalent shell session for upstream
// would be: start a BrowserSession (which spins up DOMWatchdog,
// DownloadsWatchdog, PopupsWatchdog, ...) and visit a page that
// triggers a download + a confirm() dialog.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	s06 "learn-browser-use/s06-watchdog-pattern"
	"learn-browser-use/s06-watchdog-pattern/watchdogs"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out io.Writer) error {
	ctx := context.Background()
	bus := s06.NewEventBus()

	dl := &watchdogs.DownloadsWatchdog{}
	pp := &watchdogs.PopupsWatchdog{}

	dlEvents, err := s06.AutoAttach(dl, bus)
	if err != nil {
		return err
	}
	ppEvents, err := s06.AutoAttach(pp, bus)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "# auto-registered handlers")
	fmt.Fprintf(out, "DownloadsWatchdog → %v\n", dlEvents)
	fmt.Fprintf(out, "PopupsWatchdog    → %v\n", ppEvents)

	fmt.Fprintln(out, "\n# emit one event of each type")
	emits := []s06.Event{
		s06.DownloadStartedEvent{URL: "https://example.com/foo.pdf", Filename: "foo.pdf"},
		s06.DownloadStartedEvent{URL: "https://example.com/bar.zip", Filename: "bar.zip"},
		s06.JSDialogOpenedEvent{Type: "confirm", Message: "save changes?"},
		s06.JSDialogOpenedEvent{Type: "prompt", Message: "your name?"},
		// No watchdog subscribes to NavigationEvent — bus must drop it
		// silently. We emit it to prove the no-subscriber path is OK.
		s06.NavigationEvent{URL: "https://example.com/next"},
	}
	for _, e := range emits {
		if err := bus.Emit(ctx, e); err != nil {
			return err
		}
		fmt.Fprintf(out, "emitted %s\n", e.EventName())
	}

	fmt.Fprintln(out, "\n# summary")
	fmt.Fprintf(out, "downloads handled: %d\n", len(dl.Handled))
	for _, e := range dl.Handled {
		fmt.Fprintf(out, "  - %s (%s)\n", e.Filename, e.URL)
	}
	fmt.Fprintf(out, "dialogs handled:   %d\n", len(pp.Handled))
	for i, e := range pp.Handled {
		accept := "Cancel"
		if pp.Accepted[i] {
			accept = "OK"
		}
		fmt.Fprintf(out, "  - [%s] %q → %s\n", e.Type, e.Message, accept)
	}
	fmt.Fprintf(out, "navigation handlers: %d (unsubscribed events drop silently)\n",
		bus.HandlerCount("NavigationEvent"))
	return nil
}
