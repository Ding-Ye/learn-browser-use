# s06 · 看门狗事件总线 (watchdog-pattern)

> A monolithic browser controller eats the world — downloads, popups, security blocks, DOM snapshots — until nobody can read it. This session ports the upstream `BaseWatchdog` pattern to Go: a tiny `EventBus`, plus reflection-based auto-discovery of `OnXxxEvent` handlers.
> 把所有浏览器副作用都堆进一个 controller 类，没几个月就没人敢碰了。本节把上游 `BaseWatchdog` 模式翻译成 Go：一个轻量的 `EventBus`，加上通过反射自动发现 `OnXxxEvent` 处理函数。

## What this teaches / 教什么

- **`Event` 是一个名字 + 任意 payload**：dispatch 只看 `EventName()`，handler 自己 type-assert。
- **An `Event` is a name + arbitrary payload**: the bus dispatches on `EventName()`; each handler asserts the concrete type itself.
- **`EventBus.Subscribe / Emit` 是 RWMutex + slice 的最小组合**：30 行 Go 替代 Python `bubus`。
- **`EventBus.Subscribe / Emit` is a RWMutex + slice combo**: 30 lines of Go in place of Python's `bubus`.
- **`AutoAttach(watchdog, bus)` 通过反射扫 `OnXxxEvent(ctx, *XxxEvent) error` 的方法集**：上游 `attach_to_session()` 的直译。
- **`AutoAttach(watchdog, bus)` walks the method set looking for `OnXxxEvent(ctx, *XxxEvent) error`**: a direct translation of upstream `attach_to_session()`.

## Run / 运行

```bash
GOWORK=off go run ./cmd/demo   # demo + summary
GOWORK=off go test -v ./...    # 6 tests
```

(`GOWORK=off` because the workspace `go.work` is shared across sibling sessions; the module is self-contained on its own `go.mod`.)

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `event.go`              | `Event` interface + 3 concrete events. |
| `eventbus.go`           | `EventBus` with `Subscribe` + `Emit`; RWMutex-guarded slice of handlers per event name. |
| `watchdog.go`           | `Watchdog` marker interface + `AutoAttach` reflection scanner. |
| `watchdogs/downloads.go`| `DownloadsWatchdog` — records `DownloadStartedEvent`s. |
| `watchdogs/popups.go`   | `PopupsWatchdog` — records `JSDialogOpenedEvent`s + would-accept choice. |
| `cmd/demo/main.go`      | CLI demo: build bus, attach 2 watchdogs, emit 5 events, print summary. |
| `eventbus_test.go`      | 6 tests: emit happy path, reflect auto-register, concurrent emits, unsubscribed drop, ordered handlers, error aggregation. |
| `testdata/expected.txt` | Captured `go run ./cmd/demo` output. |

## Key teaching points / 关键学习点

1. **Why a marker interface `Watchdog interface{}` instead of `Attach(bus)`?** Upstream's contract is "shape-driven" — any class with `on_EventName` methods is a watchdog. We mirror that: `AutoAttach` infers the contract from method shapes, the interface stays empty.
2. **Why pointer receivers on watchdogs?** Auto-discovered handlers mutate state (`w.Handled = append(...)`). The reflection scanner reads the *method set of `*T`*, so users must pass `&DownloadsWatchdog{}` not `DownloadsWatchdog{}`.
3. **Why synchronous handler dispatch (no channels)?** Upstream blocks on handler completion (e.g. agent waits for the click handler to finish before reading the resulting DOM). A buffered channel does not give us that. Sync dispatch + RWMutex is the Go-idiomatic minimum that matches the semantic.
4. **Why `errors.Join` instead of bail-on-first-error?** Two watchdogs subscribing to the same event should each get a chance to react — a flaky download handler should not prevent the popup handler from accepting a dialog. Aggregated errors preserve every failure for inspection.

## What this is NOT / 这一节"故意不做"什么

- No circuit-breaker on CDP disconnect (upstream `LIFECYCLE_EVENT_NAMES`).
- No parent-event tracing chain (`event_parent_id` / `grandparent`).
- No `LISTENS_TO` / `EMITS` class-level declarations — Go has no class vars.
- No `bubus` async semantics, no `await event.dispatch()` parent-tree walking.
- No duplicate-handler RuntimeError — Go has no method-equality concept; we leave dedup to the user.

## Upstream / 上游对照

- `browser_use/browser/watchdog_base.py` — the real `BaseWatchdog`, plus `attach_to_session()` reflection logic.
- `browser_use/browser/watchdogs/popups_watchdog.py` — concrete sample we paraphrase as `PopupsWatchdog`.
- `browser_use/browser/watchdogs/downloads_watchdog.py` — concrete sample we paraphrase as `DownloadsWatchdog`.

See `docs/{zh,en}/s06-watchdog-pattern.md` for the full walkthrough and `upstream-readings/s06-watchdog-pattern.py` for the annotated upstream excerpt.
