// Package watchdogs holds the two example watchdog implementations for
// session s06. They mirror upstream browser_use/browser/watchdogs/
// (downloads_watchdog.py, popups_watchdog.py) at teaching-toy fidelity:
// each watchdog records what it would have done, instead of actually
// driving CDP.
package watchdogs

import (
	"context"

	s06 "learn-browser-use/s06-watchdog-pattern"
)

// DownloadsWatchdog mirrors browser_use/browser/watchdogs/downloads_watchdog.py
// at sharply reduced scope: it just records each download it saw.
//
// In the real codebase this watchdog wires up Browser.downloadWillBegin
// + Browser.downloadProgress CDP callbacks, decides which files are PDFs,
// drops them into BrowserSession's downloads_path, and emits
// FileDownloadedEvent. We collapse all that to "append the event to a
// slice and return". The teaching point is the auto-registration shape,
// not download semantics.
type DownloadsWatchdog struct {
	Handled []s06.DownloadStartedEvent
}

// OnDownloadStartedEvent is the auto-discovered handler for
// DownloadStartedEvent. AutoAttach finds this by name, validates
// the (ctx, *DownloadStartedEvent) error shape, and Subscribes a
// reflection adaptor under "DownloadStartedEvent".
//
// Note this method has a pointer receiver — that's required for the
// append() to mutate the Handled slice on the bus's stored watchdog.
// AutoAttach pass &DownloadsWatchdog{} (a pointer) so this method-set
// is reachable.
func (w *DownloadsWatchdog) OnDownloadStartedEvent(ctx context.Context, e *s06.DownloadStartedEvent) error {
	_ = ctx // Real watchdog would honour ctx.Done() during CDP calls.
	w.Handled = append(w.Handled, *e)
	return nil
}
