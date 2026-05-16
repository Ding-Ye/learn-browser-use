---
title: "s09 · DOM service"
chapter: 9
slug: s09-dom-service
est_read_min: 14
---

# s09 · DOM service

> Teaching focus: s08 left us with a stateless serializer — `tree → text`. Real agent loops call `Get` on every step, navigate between pages, and target enormous DOM trees that need to be filtered down. s09 wraps the serializer in a `DOMService` that owns a Cache + a Snapshot driver + an EventBus subscription. About 500 lines of Go in total, ~120 in `dom_service.go` itself; the rest is supporting types and tests.

---

## Problem / 问题

By the end of s08 the toolbox looks like this:

```go
// s08-style usage
state := Serialize(domTree)         // pure transformation
text := state.LLMText
indexedRects := state.SelectorMap   // map[int]DOMRect
```

The serializer is a stateless function. That's the right shape for the *transformation*, but it's not the right shape for what the agent loop actually does. Concrete pain:

1. **Every step re-serializes from scratch.** The LLM picks an action; the action runs; we want the same DOM serialized again for the next prompt. Calling the snapshot pipeline + serializer every time is expensive — upstream's `_get_all_trees` does 5+ CDP round-trips per call (`DOMSnapshot.captureSnapshot`, `DOM.getDocument`, `Accessibility.getFullAXTree` per frame, `Runtime.evaluate` for iframe scroll + click-listener detection, etc.).
2. **No cache invalidation discipline.** If we cache naively and never invalidate, the LLM acts on stale DOM after a navigation. If we invalidate too aggressively (e.g. on every action), the cache is useless.
3. **No iframe-depth limit.** Production websites embed ad iframes nested 6+ deep. Walking them all blows the prompt token budget; cutting them off at a configurable depth is what makes the agent affordable.
4. **No viewport / area filter.** A 1920×1080 `<main>` wrapper isn't an interactable element; if we keep it the LLM gets confused. We need to drop oversized nodes before they reach the serializer.

s09 fixes all four. The shape is the standard Go composition we used in s07: one struct that holds the moving parts, methods that expose the lifecycle.

## Solution / 解决方案

Introduce `DOMService`, a composition container:

```go
type DOMService struct {
    Cache    *Cache       // TTL-based; supports explicit Invalidate
    Bus      *EventBus    // subscribes to NavigationEvent
    Snapshot SnapshotFunc // pluggable producer; stub in s09, CDP in s12

    CurrentURL        string
    IframeMaxDepth    int  // upstream max_iframe_depth, default 100 here
    ViewportThreshold int  // bbox area cap; 0 disables
}
```

…with two public methods:

| Method | Responsibility | Upstream analog |
|---|---|---|
| `Get(ctx)`     | Return cached state if fresh; otherwise Snapshot → filter → Serialize → Cache.Set → return. | `DomService.get_serialized_state` (conceptual; upstream calls captureSnapshot inline every step) |
| `Invalidate()` | Drop the cached state. Called by the NavigationEvent handler and exposed publicly for manual refresh. | Caller composes `self._dom_cache = None` upstream |

The bus subscription happens *at construction* in `NewDOMService`. This is important — see the non-obvious points below.

To stay self-contained (the learn-browser-use rule: **no cross-session imports**), we re-declare three concepts from earlier chapters locally:

| Local file | Re-declared concept | Originally in |
|---|---|---|
| `dom_node.go`   | `DOMNode` (Tag, Text, BBox, Visible, Children, BackendNodeID) | s08 |
| `serializer.go` | `Serialize` + `SerializedState` + `SelectorEntry`              | s08 |
| `eventbus.go`   | `EventBus` (Subscribe + Emit) + `NavigationEvent`              | s06 / s07 |

The serializer is a deliberately shrunk version of s08's — the spotlight here is on the service's lifecycle, not on paint-order tricks.

## How It Works / 工作原理

```
┌─────────────────────────────────────────────────────────────────┐
│                        service = NewDOMService                  │
│   ┌─────────────┐    ┌─────────┐    ┌────────────────────┐      │
│   │  Cache      │    │ EventBus│    │  Snapshot          │      │
│   │  (TTL+Inv)  │    │         │    │  (page A / B stub) │      │
│   └─────────────┘    └─────────┘    └────────────────────┘      │
│         ▲                  │                                     │
│         │  Invalidate()    │ Subscribe("NavigationEvent", ...)   │
│         └──────────────────┘                                     │
└─────────────────────────────────────────────────────────────────┘

  service.Get(ctx)
  ───────────────►
       Cache.Get() ──── hit  ───► return cached SerializedState
                  └─── miss ───► Snapshot(CurrentURL) → applyFilters → Serialize → Cache.Set → return

  bus.Emit(ctx, NavigationEvent{URL: "https://b.example.com"})
  ───────────────────────────────────────────────────────────►
       handler (registered at construction) → Cache.Invalidate()

  service.Get(ctx)   ← second time after Navigate
  ───────────────►
       Cache.Get() ── miss (Data == nil) ──► fresh Snapshot → fresh text
```

Core code (~50 lines):

```go
// dom_service.go (excerpt)
func NewDOMService(bus *EventBus, snap SnapshotFunc, cache *Cache) *DOMService {
    s := &DOMService{
        Cache:    cache,
        Bus:      bus,
        Snapshot: snap,
        IframeMaxDepth:    100,
        ViewportThreshold: 0,
    }
    s.subscribe()                          // ← bus wiring happens at construction
    return s
}

func (s *DOMService) subscribe() {
    s.Bus.Subscribe("NavigationEvent", func(ctx context.Context, e Event) error {
        s.Cache.Invalidate()
        return nil
    })
}

func (s *DOMService) Get(ctx context.Context) (*SerializedState, error) {
    if cached, ok := s.Cache.Get(); ok {   // ← happy path: cache hit
        return cached, nil
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    if cached, ok := s.Cache.Get(); ok {   // ← double-checked: lost the race
        return cached, nil
    }
    root, err := s.Snapshot(s.CurrentURL)  // ← the expensive call
    if err != nil { return nil, fmt.Errorf("dom snapshot: %w", err) }

    pruned := s.applyFilters(root)         // ← IframeMaxDepth + ViewportThreshold
    state := Serialize(pruned)             // ← s08-style transformation
    s.Cache.Set(state)
    return state, nil
}
```

**Four non-obvious points**:

1. **Why TTL *and* explicit Invalidate?** Two complementary trigger surfaces. Explicit invalidation is precise: when we *know* a navigation happened (the bus told us), we invalidate immediately. TTL is the safety net for everything else — JS-driven DOM edits, XHR mutations, scroll-into-view that we forgot to wire an event for. Without TTL, the "we missed an event" case turns into a stuck cache; without explicit invalidation, every navigation pays a full TTL wait. Both, not either.
2. **Why subscribe at construction, not lazily on first Get?** Because the bus might fire BEFORE Get is ever called. Imagine: agent constructs the service, navigation happens, *then* the agent's first step calls Get. With lazy subscription, the navigation would be lost and the first Get would return whatever stale page was loaded at boot. With construction-time subscription, the invalidation fires (the cache is empty so it's effectively a no-op) and the first Get correctly snapshots the new page. The `TestSubscribedAtConstruction` test pins exactly this scenario.
3. **Why double-checked locking in Get?** Two concurrent goroutines might both miss the cache, both grab the lock in series, and both fire a snapshot if we don't re-check after acquiring the lock. The cost is two `Cache.Get` calls in the worst case; the gain is N-1 avoided snapshots when N goroutines all miss simultaneously. `singleflight` is the proper tool for this but it's an extra dependency that obscures the lesson.
4. **Why does `applyFilters` order depth-first then area-second?** Depth pruning is structural — it copies the tree with some branches truncated. Area filtering walks the surviving tree once. Doing area first would visit nodes we're about to throw away in the depth pass. Marginal gain in our 4-node fixtures, but it's the right order on real 10k-node trees.

## What Changed / 与上一节的变化

s08 style:

```diff
- // s08 usage — caller drives snapshot + serialization manually
- domTree := captureRawSnapshot(url)        // ← caller's problem
- state := Serialize(domTree)               // ← stateless
- // next step: caller decides whether to re-snapshot
```

After s09:

```diff
+ service := NewDOMService(bus, snapshotFunc, NewCache(30*time.Second))
+ service.CurrentURL = "https://a.example.com"
+ state, _ := service.Get(ctx)              // ← cache miss; snapshot fires
+ state2, _ := service.Get(ctx)             // ← cache hit; same text
+
+ // Navigation:
+ service.CurrentURL = "https://b.example.com"
+ bus.Emit(ctx, NavigationEvent{URL: "..."}) // ← invalidates the cache
+ state3, _ := service.Get(ctx)             // ← cache miss; fresh snapshot
```

The headline gain: **the serializer is no longer the entry point**. In s08 the caller had to remember to snapshot, serialize, and re-snapshot on navigation; each was a separate concern at the call site. After s09 it's one `service.Get(ctx)`. The cache, the navigation handling, and the filter knobs are all hidden behind that one method.

This chapter's outputs get reused downstream:

- s12's Agent will own a `DOMService` and call `Get` at the top of every step.
- s_full's omission table will call out exactly which parts of the real `_get_all_trees` we stubbed.

## Try It / 动手试一试

```bash
cd agents/s09-dom-service

# Full cache + invalidation demo
GOWORK=off go run .

# 6 tests
GOWORK=off go test -v ./...
```

(`GOWORK=off` is just because the root `go.work` doesn't list s09 yet — the module is self-contained and builds without the workspace.)

Expected output (excerpt):

```
# DOMService cache + invalidation demo

[Get #1] snapshot calls so far: 1
         serialized text:
  [0] <button> submit
  [1] <button> cancel

[Get #2] snapshot calls so far: 1  (expected unchanged)
         text identical to Get #1? true

[NavigationEvent emitted; CurrentURL → https://b.example.com]

[Get #3] snapshot calls so far: 2  (expected to be +1 from Get #2)
         serialized text:
  [0] <a> home
  [1] <a> about
  [2] <a> contact

Selector map size for Get #3: 3 entries
```

Test coverage:

- `TestSnapshotIsCached` — two consecutive Gets must result in one snapshot invocation.
- `TestNavigationInvalidates` — emit NavigationEvent → next Get re-fetches AND the text reflects the new page.
- `TestCacheTTLExpires` — advance the injected clock past TTL → next Get re-fetches without any event.
- `TestIframeMaxDepthEnforced` — pass a 4-deep tree with max depth 2 → only the first 3 levels survive.
- `TestViewportThresholdFilters` — 1000×1000 wrapper drops; its 50×50 child stays.
- `TestSubscribedAtConstruction` (bonus) — navigate BEFORE first Get → first Get still sees page B.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/dom/service.py::DomService` is ~1,200 lines: real CDP plumbing (frame tree walk, AX merge, computed-style fetch), iframe handling, hidden-element hints, observability decorators. s09 takes the skeleton.

```python
# Source: browser_use/dom/service.py#L35-L70

class DomService:
    """
    Service for getting the DOM tree and other DOM-related information.

    Either browser or page must be provided.

    TODO: currently we start a new websocket connection PER STEP, we should definitely keep this persistent
    """

    logger: logging.Logger

    def __init__(
        self,
        browser_session: 'BrowserSession',
        logger: logging.Logger | None = None,
        cross_origin_iframes: bool = False,
        paint_order_filtering: bool = True,
        # ↓ Corresponds to our Go DOMService.IframeMaxDepth — upstream
        #   ships two knobs (count + depth); we keep depth.
        max_iframes: int = 100,
        max_iframe_depth: int = 5,
        # ↓ Corresponds to our ViewportThreshold, though the unit
        #   differs: upstream is "pixels beyond viewport edge", we
        #   simplified to "bbox area cap" so the test fixtures are
        #   self-contained.
        viewport_threshold: int | None = 1000,
    ):
        self.browser_session = browser_session
        self.logger = logger or browser_session.logger
        self.cross_origin_iframes = cross_origin_iframes
        self.paint_order_filtering = paint_order_filtering
        self.max_iframes = max_iframes
        self.max_iframe_depth = max_iframe_depth
        self.viewport_threshold = viewport_threshold

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc_value, traceback):
        pass  # no need to cleanup anything, browser_session auto handles cleaning up session cache
```

```python
# Source: browser_use/dom/service.py#L385-L450 (heavily excerpted)

async def _get_all_trees(self, target_id: TargetID) -> TargetAllTrees:
    cdp_session = await self.browser_session.get_or_create_cdp_session(target_id=target_id, focus=False)

    # Wait for the page to be ready first
    try:
        ready_state = await cdp_session.cdp_client.send.Runtime.evaluate(
            params={'expression': 'document.readyState'}, session_id=cdp_session.session_id
        )
    except Exception:
        pass

    # ↓ Corresponds to one big SnapshotFunc invocation in our Go.
    #   In production this is 5+ CDP round-trips combined.
    def create_snapshot_request():
        return cdp_session.cdp_client.send.DOMSnapshot.captureSnapshot(
            params={
                'computedStyles': REQUIRED_COMPUTED_STYLES,
                'includePaintOrder': True,
                'includeDOMRects': True,
                'includeBlendedBackgroundColors': False,
                'includeTextColorOpacities': False,
            },
            session_id=cdp_session.session_id,
        )

    # Run snapshot + document + AX tree in parallel
    snapshot, document, ax_tree = await asyncio.gather(
        create_snapshot_request(),
        cdp_session.cdp_client.send.DOM.getDocument(
            params={'depth': -1, 'pierce': True},
            session_id=cdp_session.session_id,
        ),
        self._get_merged_ax_tree(cdp_session),
    )
    # ... merge into EnhancedDOMTreeNode and return
```

**Reading notes**:

1. **`async def __aenter__/__aexit__`**: upstream uses the service as an async context manager — `async with DomService(...) as dom: ...`. Our Go has no equivalent because we don't open per-call CDP sessions; the snapshot func is plain. In s12 when we'd inject a real CDP, we'd add `Close()` for the same reason.
2. **`cross_origin_iframes: bool`**: upstream toggles whether to walk into cross-origin iframes. Real sites embed ad iframes from different origins; visiting them costs another CDP round-trip and risks racing the iframe's own load. We dropped this knob — our stub trees don't model cross-origin.
3. **`paint_order_filtering: bool`**: upstream uses CDP's paint-order field to drop occluded elements (z-stacked behind something else). Belongs in s08 (the serializer's job); s09 trusts whatever the serializer hands back.
4. **`asyncio.gather` of three requests**: upstream parallelizes snapshot + document + AX tree because all three can run concurrently on a single CDP session. Our `SnapshotFunc` is synchronous; in a real Go port you'd use a goroutine + channel join for the same parallelism.
5. **`return_exceptions=True` (line 370)**: upstream's AX tree fetch is best-effort per child frame — a detached iframe shouldn't break the whole snapshot. Our stub never errors; in s12 with real CDP we'd add the same per-frame guard.
6. **Cache invalidation upstream**: there isn't an explicit cache. Each step calls the snapshot pipeline fresh. The optimization we're teaching ("cache between calls, invalidate on Navigation") is one of the deliberate divergences — it makes the agent loop affordable when you're prototyping locally without an actual LLM. The trade-off: a snapshot that's exactly correct only at fetch time becomes a snapshot that's correct until the next NavigationEvent, with TTL as a safety net.

**Where to read next**: from `DomService.__init__` jump to `_get_all_trees` (line 385) to see the full real pipeline, then to `_count_hidden_elements_in_iframes` (line 70) for the LLM-hint logic we dropped, then to where `DOMWatchdog` calls `DomService` from `browser_use/browser/events.py` — the watchdog is how `BrowserSession` plumbs DOM access into the bus.

---

**Next up**: s10 introduces token cost tracking — `TokenCost` accumulates per-invocation token counts and computes dollar cost from embedded pricing data. s10 doesn't depend on s09 (it sits on the LLM provider, not the DOM), but it's the next concern an agent loop needs.
