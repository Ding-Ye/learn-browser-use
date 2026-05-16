---
title: "s05 · Element actor (CDP abstraction)"
chapter: 5
slug: s05-element-actor
est_read_min: 12
---

# s05 · Element actor (CDP abstraction)

> Teaching focus: every action in the previous four sessions ended at a string ("the LLM picked `click(index=3)`"). Sooner or later that string has to turn into a wire frame for Chromium. This session introduces the **CDP boundary**: a `CDPClient` interface + a recorder-style stub. No real browser yet — just a recorder that captures the frames we *would* have sent. Element ports `browser_use/actor/element.py` into ~400 lines of Go: Element struct + 4 methods (Click / Type / Focus / Screenshot) + the recorder.

---

## Problem / 问题

By the end of s04 we have a tools registry that hands the LLM a typed menu. The LLM picks `click({"index":3})`, the Dispatcher calls `tool.Run(ctx, raw)`, and the tool returns a string. So far so good — but `tool.Run` has been a black box. What does the inside of `click` actually do?

In the real browser-use that body looks roughly like this:

```python
async def click_handler(params, browser_session):
    el = await browser_session.get_element(params.index)
    await el.click(button='left')   # ← what does this expand to?
```

`el.click(...)` is where the CDP wire frames are born. Concretely, a single click expands into:

1. `Page.getLayoutMetrics` — find the viewport size
2. `DOM.getContentQuads` (or `DOM.getBoxModel`) — locate the element on screen
3. `DOM.scrollIntoViewIfNeeded` — get it on screen if it isn't
4. `Input.dispatchMouseEvent type=mouseMoved` — move the cursor
5. `Input.dispatchMouseEvent type=mousePressed` — press
6. `Input.dispatchMouseEvent type=mouseReleased` — release

Six frames over a WebSocket for one logical "click". Multiply that by `Type` (one frame per character), `Focus`, `Screenshot`, and you have several hundred CDP method names spread across `Input`, `DOM`, `Page`, `Runtime`, `Target`, `Network` domains.

We need **somewhere to start writing this layer that doesn't require Chromium**. That's what s05 is for.

s05 answers three questions:

1. **What's the shape of "the thing Element talks to"?** → `CDPClient` interface with a single `Send(method, params)` method.
2. **How do we test Element without Chromium?** → `RecordingCDPClient` that appends every Send to a `Frames` slice instead of sending it over a WebSocket.
3. **What does Element itself look like?** → A two-field struct: `{Client, NodeID}`, plus methods that build CDP params and call `Send`.

## Solution / 解决方案

Three building blocks:

| Role | Type | Upstream counterpart |
|---|---|---|
| The wire | `CDPClient` interface | `cdp_use.CDPClient` |
| Test transport | `RecordingCDPClient` | (no upstream analogue — we invented this) |
| The element | `Element` struct | `browser_use/actor/element.py::Element` |

`CDPClient` is one method:

```go
type CDPClient interface {
    Send(ctx context.Context, method string, params map[string]any) (map[string]any, error)
}
```

That's it. No per-domain typed methods, no auto-generated CDP bindings. Real CDP on the wire is JSON envelopes anyway — `{"id": N, "method": "Domain.method", "params": {...}}` going out, `{"id": N, "result": {...}}` coming back — so collapsing every CDP call into a string+map call is honest, not lossy.

`RecordingCDPClient` is the s05 implementation:

```go
type RecordingCDPClient struct {
    Frames    []Frame                       // append-only log
    Responses map[string]map[string]any     // optional caller stubs
}
```

Every `Send` is recorded into `Frames`. Responses come from the `Responses` table if the caller pre-populated it, otherwise from a built-in default table (`Page.captureScreenshot` returns base64'd PNG header bytes, etc.). Unknown methods get an empty `{}` — matching real CDP's "side-effecting method, no payload" responses.

`Element` is two fields and four methods:

```go
type Element struct {
    Client CDPClient
    NodeID BackendNodeID
}

func (e Element) Click(ctx, opts ClickOptions) error
func (e Element) Type(ctx, text string) error
func (e Element) Focus(ctx) error
func (e Element) Screenshot(ctx) ([]byte, error)
```

Each method builds a `map[string]any` carrying `backendNodeId` plus method-specific keys, and calls `Send`. `Click` makes three calls (move/press/release); `Type` makes one (`Input.insertText`); `Focus` makes one (`DOM.focus`); `Screenshot` makes one (`Page.captureScreenshot`) and decodes the base64 result.

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────────────┐
│                              s05 dataflow                            │
│                                                                      │
│   el := Element{Client: rec, NodeID: 42}                             │
│                                                                      │
│   el.Click(ctx, opts)                                                │
│       │                                                              │
│       ├── build map: {type: "mouseMoved",     backendNodeId: 42, ..} │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   append → Frames                                            │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   lookup default response → return {}                        │
│       │                                                              │
│       ├── build map: {type: "mousePressed", button, clickCount, ...} │
│       │   rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│       │                                                              │
│       └── build map: {type: "mouseReleased", ...}                    │
│           rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│                                                                      │
│   el.Type(ctx, "hello 你好 👋")                                       │
│       │                                                              │
│       └── rec.Send(ctx, "Input.insertText",                          │
│                    {text: "...", backendNodeId: 42})                 │
│                                                                      │
│   el.Screenshot(ctx)                                                 │
│       │                                                              │
│       └── rec.Send(ctx, "Page.captureScreenshot", {format: "png"})   │
│             returns {"data": "<base64 PNG header>"}                  │
│             decoded → 8-byte slice                                   │
│                                                                      │
│   --- tests inspect ---                                              │
│   rec.Frames == [ Input.dispatchMouseEvent x3,                       │
│                   Input.insertText,                                  │
│                   Page.captureScreenshot ]                           │
└──────────────────────────────────────────────────────────────────────┘
```

Core ~50 lines:

```go
// cdp_client.go
func (c *RecordingCDPClient) Send(_ context.Context, method string, params map[string]any) (map[string]any, error) {
    c.Frames = append(c.Frames, Frame{Method: method, Params: params})
    if c.Responses != nil {
        if r, ok := c.Responses[method]; ok {
            return r, nil
        }
    }
    if r, ok := defaultResponses[method]; ok {
        return r, nil
    }
    return map[string]any{}, nil
}

// element.go (Click loop)
for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
    if _, err := e.Client.Send(ctx, "Input.dispatchMouseEvent", pressParams(kind)); err != nil {
        return fmt.Errorf("element click (%s): %w", kind, err)
    }
}

// element.go (Type)
params := map[string]any{
    "text":          text,
    "backendNodeId": int(e.NodeID),
}
_, err := e.Client.Send(ctx, "Input.insertText", params)

// element.go (modifier bitmask packing)
func modifiersToBitmask(mods []string) int {
    mask := 0
    for _, m := range mods {
        switch m {
        case "Alt":     mask |= 1
        case "Control": mask |= 2
        case "Meta":    mask |= 4
        case "Shift":   mask |= 8
        }
    }
    return mask
}
```

**Four non-obvious points**:

1. **Why "Recording" and not "Mocking"?** A mock library (gomock, testify/mock) makes you declare expectations upfront: "expect this method called with these exact params". That's the wrong shape for learning a protocol — you don't yet know which methods need to fire, so the test would be co-evolving with the code under test. A recorder inverts this: just do the work, then inspect what was recorded. Assertions read like prose, not like contracts.
2. **Why `BackendNodeID` and not selectors or DOM `nodeId`?** Chromium's `nodeId` is reassigned every time the frontend reattaches the document — fine for one synchronous call, useless across two LLM steps. `backendNodeId` is stable across most reflows; it's what survives long enough to be passed between LLM turns. Upstream's `Element` carries one for exactly this reason. Selectors are even worse: brittle to redesigns, and the LLM doesn't see the CSS anyway.
3. **Why does `Type` use `Input.insertText` instead of per-character `dispatchKeyEvent`?** Real `Element.fill` upstream walks each character through a keyDown/char/keyUp triplet because some pages listen to keystrokes for autocomplete. That's a *page-behaviour* concern, properly handled by a watchdog (s06). The CDP primitive for "insert this text right now" is `Input.insertText`, which is what upstream's `skill_cli` uses for fast paste. Using it here keeps Unicode boringly correct (one frame, full UTF-8) and keeps the chapter focused.
4. **Modifier bitmask packing is OS plumbing.** `{Alt:1, Control:2, Meta:4, Shift:8}` OR'd together is irreducibly weird; CDP inherits it from underlying OS input-event types (Windows VK_*, X11 ModifierMask, Cocoa NSEventModifierFlags). We surface the bitmask in the recorder so learners *see* this on the wire instead of hiding it behind a Go enum — there's no abstraction to invent here, only the actual protocol to look at.

## What Changed / 与上一节的变化

s04's tools were string-in / string-out. The Dispatcher called `tool.Run(ctx, raw)`, the tool returned a string, and the dispatcher rolled that into a `ContentBlock{Type:"tool_result"}`. No browser, no CDP, no element handles — just functions over JSON.

```diff
- // s04: tool.Run is a black box
- type Tool interface {
-     Run(ctx context.Context, input json.RawMessage) (string, error)
- }
-
- // and click looked like:
- func (SearchTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
-     // ... pure-text result, no browser side effects
- }
```

s05 introduces a parallel concern: the *layer underneath* tools. Once s07 ships BrowserSession, a real `click` tool will look like:

```diff
+ // s07+: click_tool delegates to Element through the registered BrowserSession
+ func (ClickTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
+     var args struct{ Index int `json:"index"` }
+     _ = json.Unmarshal(raw, &args)
+     el := session.GetElement(args.Index)             // s07
+     return "clicked", el.Click(ctx, ClickOptions{})  // s05 — this chapter
+ }
```

The crucial new capability: **Element is testable without Chromium**. The recorder gives us a substrate where we can verify the frame stream (the protocol contract) is correct in milliseconds, no browser, no flakiness. Production code paths will swap the recorder for a real WebSocket client; the Element code does not change.

## Try It / 动手试一试

```bash
cd agents/s05-element-actor

# build + run the demo (records click + type + screenshot)
go run .

# 6 tests
go test -v ./...
```

Expected output (excerpt):

```
# recorded CDP frames

[0] Input.dispatchMouseEvent
  {
    "backendNodeId": 42,
    "type": "mouseMoved",
    "x": 0,
    "y": 0
  }

[1] Input.dispatchMouseEvent
  {
    "backendNodeId": 42,
    "button": "left",
    "clickCount": 1,
    "modifiers": 10,
    "type": "mousePressed",
    "x": 0,
    "y": 0
  }
...
[3] Input.insertText
  {
    "backendNodeId": 42,
    "text": "hello 你好 👋"
  }

[4] Page.captureScreenshot
  {
    "backendNodeId": 42,
    "format": "png"
  }
```

Test coverage:

- `TestClickRecordsMouseEvent` — Click emits the 3-frame move/press/release sequence with the right backendNodeId.
- `TestTypeEncodesUnicode` — Type with `"café 你好 👋"` round-trips UTF-8 bytes through the frame.
- `TestModifierKeysPassed` — `Modifiers: ["Shift","Control"]` lands in the recorded frame as bitmask `10`.
- `TestScreenshotReturnsDummyPNG` — Screenshot returns ≥8 bytes starting with the PNG signature `89 50 4E 47 0D 0A 1A 0A`.
- `TestFocusDispatchesDOMFocus` — Focus emits exactly one `DOM.focus` frame with the backendNodeId.
- `TestEmptyClickOptionsNormalises` — zero-value `ClickOptions{}` defaults to left single-click, no modifiers.

## Upstream Source Reading / 上游源码阅读

The upstream `Element` class lives in `browser_use/actor/element.py`. The 60-line excerpt below covers the `__init__` + `click` method head + the final mouse-event dispatch — the parts that map 1:1 to our Go code.

```python
# Source: browser_use/actor/element.py#L62-L100, L268-L325
# License: MIT

class Element:
    """Element operations using BackendNodeId."""

    def __init__(
        self,
        browser_session: 'BrowserSession',
        backend_node_id: int,
        session_id: str | None = None,
    ):
        self._browser_session = browser_session
        self._client = browser_session.cdp_client
        self._backend_node_id = backend_node_id
        self._session_id = session_id

    async def click(
        self,
        button: 'MouseButton' = 'left',
        click_count: int = 1,
        modifiers: list[ModifierType] | None = None,
    ) -> None:
        """Click the element using the advanced watchdog implementation."""
        # ... viewport metrics + quad geometry + scrollIntoView elided ...

        # Calculate modifier bitmask for CDP
        modifier_value = 0
        if modifiers:
            modifier_map = {'Alt': 1, 'Control': 2, 'Meta': 4, 'Shift': 8}
            for mod in modifiers:
                modifier_value |= modifier_map.get(mod, 0)

        # Move mouse to element
        await self._client.send.Input.dispatchMouseEvent(
            params={'type': 'mouseMoved', 'x': center_x, 'y': center_y},
            session_id=self._session_id,
        )

        # Mouse down
        await self._client.send.Input.dispatchMouseEvent(
            params={
                'type': 'mousePressed',
                'x': center_x, 'y': center_y,
                'button': button,
                'clickCount': click_count,
                'modifiers': modifier_value,
            },
            session_id=self._session_id,
        )

        # Mouse up
        await self._client.send.Input.dispatchMouseEvent(
            params={
                'type': 'mouseReleased',
                'x': center_x, 'y': center_y,
                'button': button,
                'clickCount': click_count,
                'modifiers': modifier_value,
            },
            session_id=self._session_id,
        )
```

**Reading notes**:

1. **The `_client.send.Input.dispatchMouseEvent(...)` shape** in upstream is a *typed* CDP wrapper from the `cdp-use` library — every CDP domain becomes an attribute, every method becomes an async function, and the param dict is type-checked against a generated TypedDict. Our Go `Send(method, params)` collapses all of that into one method+string. We trade ergonomics for being stdlib-only.
2. **The modifier_map dict is identical to our Go switch** — same keys, same values. CDP itself defines this packing in the Chrome DevTools Protocol spec; both sides just respect it.
3. **upstream's `click` is ~250 lines**; our Go version is ~30. The difference is mostly: viewport clamping, quad-vs-box-model fallback, scrollIntoView, JS-click fallback when geometry is missing, and per-frame `asyncio.wait_for` timeouts. Every line you don't see in s05 represents one issue you'd hit in production and reach for upstream code to study.
4. **The per-character keyboard path** in upstream's `fill()` (line 423) emits *three* CDP frames per character (keyDown / char / keyUp). For a 10-character input that's 30 round-trips. Our s05 uses `Input.insertText` which is one frame total — correct for the teaching scope, and the same call upstream's `skill_cli` uses for fast paste at lines 122 and 177.
5. **Why `session_id` keyword?** Upstream's CDP client multiplexes multiple browser targets (tabs, frames, popups) over the same WebSocket. Each target gets a `session_id` and every method takes it as a kwarg. We drop this entirely in s05 — there's only one notional target. s07 will add it back when it introduces the lifecycle layer.

**Read further**: open `browser_use/actor/element.py` and read `click()` (L93–L351), `fill()` (L353–L507), `focus()` (L521–L526), and `screenshot()` (L682–L709) in that order. They share a shape: get geometry → maybe scroll → dispatch CDP frames → optionally fall back to a JS-eval path. Once you've internalised that shape, every other method (`hover`, `check`, `select_option`, `drag_to`) reads as variation on the theme.

---

**Next session preview**: s06 introduces an event bus and the watchdog pattern. Today's element calls run to completion synchronously; s06 lets concerns like popup-dismissal and download-handling subscribe to events emitted *during* the click without growing Element's body.
