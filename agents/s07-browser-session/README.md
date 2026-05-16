# s07 · 浏览器会话 (browser-session)

> s06 left us with a bus and free-floating watchdogs. s07 promotes both into a `BrowserSession` with `Start`/`Stop` — one container that opens the (stub) CDP connection, auto-attaches watchdogs, and is idempotent against double-Start.
> s06 给我们的是裸 bus + 散养 watchdog。s07 把它们封装进 `BrowserSession`，提供 `Start`/`Stop` 生命周期，统一管 CDP 连接 + watchdog 注册，并保证 Start 可重入。

## What this teaches / 教什么

- **Lifecycle is a state machine.** `Start` / `Stop` / `Restart` / `IsRunning` form the smallest API surface that hides "bus owns watchdogs" from callers.
- **生命周期是一个状态机**：`Start` / `Stop` / `Restart` / `IsRunning` 是把"bus 拥有 watchdog"这件事藏起来的最小 API。
- **Idempotency matters.** Calling `Start` twice must not double-subscribe handlers; calling `Stop` on a stopped session must not error.
- **幂等不是细节**：`Start` 调两次不能让 handler 重复订阅；`Stop` 一个已经停了的 session 不应报错。
- **Symmetric attach/detach.** The recorder shows `Target.attachToTarget` ↔ `Target.detachFromTarget` pairs, so a leaked session is visible in the log.
- **attach/detach 对称**：recorder 里 attach 和 detach 必须成对出现，泄漏一眼看穿。
- **JSON-bridged reflection** lets the `watchdogs` subpackage declare its own copies of the event types without importing the parent — and AutoAttach still routes events correctly.
- **JSON 桥接的反射**：`watchdogs` 子包可以自己声明事件 struct 副本而不 import 父包，AutoAttach 仍能正确路由。

## Run / 运行

```bash
GOWORK=off go run .              # demo: start → navigate → stop
GOWORK=off go test -v ./...      # 6 tests (5 required + 1 bonus)
```

(`GOWORK=off` 只是因为根目录 `go.work` 还没把 s07 加进 use 列表；模块本身是自洽的。)

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `event.go`             | `SessionStartedEvent` / `SessionStoppedEvent` / `NavigationEvent` — 3 minimal lifecycle events. |
| `eventbus.go`          | Local re-declaration of `EventBus` (Subscribe/Emit/HandlerCount/Clear). Same shape as s06 but mutex-backed for simplicity. |
| `watchdog.go`          | `Watchdog` marker + `AutoAttach` reflection helper. Uses a JSON marshal/unmarshal bridge so the subpackage's event mirrors work. |
| `cdp_client.go`        | `CDPClient` interface + `RecordingCDPClient` stub. Trimmed-down version of s05's recorder. |
| `session.go`           | `BrowserSession{Client, Bus, Watchdogs, Started}` with `Start` / `Stop` / `Restart` / `IsRunning`. |
| `lifecycle.go`         | `RunUntilCancelled(ctx, s)` (Start → block-until-ctx.Done → Stop) and `session.Navigate(ctx, url)`. |
| `watchdogs/example.go` | `LoggingWatchdog` — records every event it sees. Subpackage to teach the layout for future watchdogs. |
| `main.go`              | CLI demo: build session, Start, Navigate, Stop. Prints CDP frame log + watchdog event log. |
| `session_test.go`      | 6 tests covering the lifecycle contract. |
| `testdata/expected.txt`| Captured `go run .` + `go test -v` output. |

## Key teaching points / 关键学习点

1. **Why a `BrowserSession` struct and not a singleton?** Singletons hide ownership: who's responsible for cleanup? With a struct value, the caller's `defer session.Stop(ctx)` is what guarantees teardown. Upstream's `BrowserSession(BaseModel)` is the same shape — every Agent instantiates its own session.
2. **Why is `Start` idempotent?** Real callers wrap Start in a retry loop ("if the first CDP handshake races a port collision, try again"). If retries double-subscribed handlers, every event would fire twice. The upstream Python comment literally says "This method is idempotent" for the same reason.
3. **Why does `Stop` clear the bus subscribers?** Mirrors upstream's `self.event_bus = EventBus()` reset. After Stop the session is conceptually a fresh object; a subsequent Start has to re-AutoAttach because every old handler held a reference to a possibly-stale watchdog state.
4. **Why JSON-bridge in AutoAttach instead of identical types?** The `watchdogs` subpackage cannot import the parent `main` package (Go forbids it). The bridge marshals the emitted event and unmarshals into the watchdog's parameter type — two structs share a *shape* without sharing a Go type. The price is one JSON round-trip per event; the benefit is the subpackage is genuinely independent.
5. **Why a `Restart` method when you can just `Stop` + `Start`?** Because the test for "the state machine cleanly cycles" is its own concept, and giving it a method makes the contract explicit. Upstream callers do the same composition manually; we hoist it into the API.

## What this is NOT / 这一节"故意不做"什么

- No real CDP WebSocket (s07 stays in `RecordingCDPClient` land — `chromedp` would be a heavy dep we never need for teaching).
- No browser profile / launch flag handling (`BrowserProfile` upstream is ~1k LOC of Chromium launch args; not the lesson).
- No cloud-browser branch (the `use_cloud=True` path is a separate sub-system).
- No watchdog circuit-breaker on CDP disconnect (upstream's `is_cdp_connected` guard is real but not the s07 lesson).
- No reconnection / `RECONNECT_WAIT_TIMEOUT` semantics.

## Upstream / 上游对照

- `browser_use/browser/session.py#L101-L500` — `class BrowserSession`, `async def start`, `async def stop`, `attach_all_watchdogs`.
- `browser_use/browser/watchdog_base.py` — `BaseWatchdog.attach_handler_to_session` (our `AutoAttach` is a Go reflection of the same idea).

See `docs/{zh,en}/s07-browser-session.md` for the full walkthrough and `upstream-readings/s07-browser-session.py` for the annotated upstream excerpt.
