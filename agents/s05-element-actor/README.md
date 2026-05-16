# s05 · 元素操作 / Element actor (CDP abstraction)

> No real Chromium yet. This session introduces the **CDP boundary**: an `Element` type wraps a `BackendNodeID` and a `CDPClient`, and the client is a "recorder" — every CDP call is appended to a `Frames` slice instead of being sent over a WebSocket.
> 还没有真实的 Chromium。本节引入 **CDP 边界**：`Element` 类型封装 `BackendNodeID` 和 `CDPClient`，而客户端是一个"录像机"——每次 CDP 调用都被追加到 `Frames` 切片里，而不是通过 WebSocket 发出去。

## What this teaches / 教什么

- **A `CDPClient` interface with one `Send` method** is enough to model the entire CDP wire.
- **一个只有 `Send` 一个方法的 `CDPClient` interface** 足以描述完整的 CDP 协议。
- **`BackendNodeID` is the stable element handle** — not selectors, not CSS classes, not DOM nodeIds.
- **`BackendNodeID` 是稳定的元素句柄**——不是选择器，不是 CSS 类，也不是会被回收的 DOM nodeId。
- **Recording vs mocking** — the recorder is one-way; tests assert on captured frames, not on call expectations declared upfront.
- **Recording 而非 Mocking**——录像机是单向的，测试断言录制下来的 frame，而不是事先声明的调用期望。

## Run / 运行

```bash
go run .                    # prints recorded frames + screenshot bytes
go test -v ./...            # 6 tests
```

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`         | `BackendNodeID`, `ClickOptions`, `Frame` — load-bearing data types. |
| `cdp_client.go`    | `CDPClient` interface + `RecordingCDPClient` implementation with default-response table. |
| `element.go`       | `Element` struct + `Click` / `Type` / `Focus` / `Screenshot` methods. |
| `main.go`          | CLI demo — clicks with Shift+Ctrl, types unicode, screenshots; prints all frames. |
| `element_test.go`  | 6 tests: click triplet, unicode round-trip, modifier mask, PNG header, focus, zero-value defaults. |
| `testdata/expected.txt` | Captured `go run .` output. |

## Key teaching points / 关键学习点

1. **Why a recorder instead of a mock?** A mock library forces you to declare every call upfront ("expect `Input.dispatchMouseEvent` with these exact params"). A recorder is post-hoc: do the work, then read what was recorded. The latter is what you actually want when learning CDP — you don't yet know exactly which methods need to fire.
2. **Why `BackendNodeID` and not a CSS selector?** Selectors break when the page rebuilds. CDP exposes `backendNodeId` as the stable identifier survives most reflows; upstream's `Element` holds it for exactly this reason. We mirror that choice.
3. **Why does `Type` use `Input.insertText` instead of per-character `dispatchKeyEvent`?** The upstream `Element.fill` ships the keyboard-event triplet (keyDown/char/keyUp) per character because some pages listen to keystrokes for autocomplete. That's a *watchdog* concern. The CDP primitive for "insert this text now" is `Input.insertText`, which is also what upstream's `skill_cli` uses for fast paste. s07's BrowserSession chapter will reintroduce the keyboard path.
4. **Modifier bitmask weirdness is on purpose.** `{Alt:1, Control:2, Meta:4, Shift:8}` OR'd together feels like OS plumbing because it is — CDP inherits it from the underlying input-event types on Windows/X11/Cocoa. The recorder surfaces this irreducible weirdness instead of hiding it behind a Go enum.

## What this is NOT / 这一节"故意不做"什么

- No real Chromium / no `chromedp` dependency.
- No layout computation (no `getContentQuads`, no `getBoxModel`) — the demo pretends every element is at (0,0).
- No scroll-into-view, no JavaScript-click fallback, no per-keystroke autocomplete firing.
- No watchdogs, no popup handling, no session lifecycle.

## Upstream / 上游对照

- `browser_use/actor/element.py#L62-L100` — class init + `BackendNodeID` field.
- `browser_use/actor/element.py#L93-L351` — the production `click()` with viewport clipping and modifier bitmask.
- `browser_use/actor/element.py#L353-L507` — `fill()`, the per-character keystroke path.
- `browser_use/actor/element.py#L521-L526` — `focus()`, mirrored 1:1 in our `Element.Focus`.
- `browser_use/actor/element.py#L682-L709` — `screenshot()`, our shape minus the viewport clip.

See `docs/{zh,en}/s05-element-actor.md` for the full walkthrough and
`upstream-readings/s05-element-actor.py` for the annotated upstream excerpt.
