# s09 · DOM 服务 (dom-service)

> s08 gave us a stateless serializer: `tree → text`. Real production needs caching (the LLM loop calls Get on every step), invalidation on navigation, and pre-serialize filtering (iframe depth, viewport area). s09 wraps the serializer in a `DOMService` that owns a Cache + a Snapshot driver + an EventBus subscription, so the agent loop just calls `service.Get(ctx)`.
> s08 给了我们一个无状态的序列化器：`tree → text`。生产环境需要缓存（agent 每一步都会调一次 Get）、导航时失效、序列化前的过滤（iframe 深度、视口面积）。s09 把序列化器包进 `DOMService`，由它持有 Cache + Snapshot 驱动 + EventBus 订阅；agent loop 只要 `service.Get(ctx)` 就够了。

## What this teaches / 教什么

- **A service owns the cache, not the serializer.** Serializer in s08 was pure; s09 puts it behind a TTL cache + invalidation hook. Same function, different lifecycle ownership.
- **服务拥有缓存，序列化器不拥有**：s08 的序列化器是纯函数；s09 把它放在 TTL 缓存 + 失效钩子背后。同一个函数，生命周期归属不同。
- **Two complementary invalidation triggers.** Explicit (NavigationEvent on the bus) for known cases, TTL for the cases you forgot to subscribe to.
- **两种互补的失效触发器**：显式（bus 上的 NavigationEvent）覆盖你想到的情况，TTL 覆盖你没想到的情况。
- **Subscribe at construction, not lazily.** If a navigation fires before the first Get, the service still notices — the bus subscription has to exist *before* the bus might emit.
- **构造时订阅，而不是懒订阅**：navigation 可能在第一次 Get 之前就触发；订阅必须在 bus 可能 Emit 之前存在。
- **Pre-serialize filters protect token budget.** IframeMaxDepth and ViewportThreshold are applied to the tree *before* serialization, so the LLM never sees pruned branches.
- **序列化前过滤保护 token 预算**：IframeMaxDepth 和 ViewportThreshold 在序列化前作用于树，LLM 永远看不到被砍掉的分支。

## Run / 运行

```bash
GOWORK=off go run .              # demo: Get / Get-cached / Navigate / Get-fresh
GOWORK=off go test -v ./...      # 6 tests (5 required + 1 bonus on subscribe-at-construction)
```

(`GOWORK=off` 只是因为根目录 `go.work` 还没把 s09 加进 use 列表；模块本身是自洽的。)

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `dom_node.go`     | Minimal `DOMNode` redeclaration (no cross-session import). Same shape as s08 but only 6 fields. |
| `serializer.go`   | ~50 line tree walk producing `SerializedState{LLMText, SelectorMap}`. Spotlight is on the service, not the serializer. |
| `snapshot.go`     | `SnapshotFunc` type + `NewStubSnapshot` returning two hand-crafted trees (page A / page B) toggled by URL. |
| `cache.go`        | `Cache{Data, UpdatedAt, TTL}` with `Get / Set / Invalidate`. Mutex-guarded; `now` is injectable for tests. |
| `eventbus.go`     | Local minimal EventBus (Subscribe + Emit) + `NavigationEvent`. No cross-session import from s06/s07. |
| `dom_service.go`  | `DOMService{Cache, Bus, Snapshot, CurrentURL, IframeMaxDepth, ViewportThreshold}`. Auto-subscribes at construction. |
| `main.go`         | CLI demo: build service, Get twice (cached), emit NavigationEvent, Get again (fresh). |
| `service_test.go` | 6 tests covering cache hit, invalidation, TTL, depth pruning, area filter, subscribe-at-construction. |
| `testdata/expected.txt` | Captured `go run .` + `go test -v` output. |

## Key teaching points / 关键学习点

1. **Why a `DOMService` struct and not free functions?** Because the cache + bus subscription + snapshot driver + filter knobs must travel together. A free function `GetDOM(url)` would let callers forget the cache and re-trigger expensive snapshots. The struct makes the right behavior the default.
2. **Why TTL *and* explicit invalidation?** Explicit invalidation is precise for events we wired up (NavigationEvent). TTL is the safety net for events we didn't (XHR mutations, DOM edits from setTimeout, etc.). Two surfaces, two failure modes, two triggers.
3. **Why subscribe at construction, not lazily on first Get?** Because the bus might fire BEFORE Get is ever called. Lazy subscription would let the service start with a stale cache that nobody invalidated. The `TestSubscribedAtConstruction` test pins this: navigate first, Get second, expect the post-navigation page.
4. **Why filter *before* serialize, not after?** Filtering after serialization means token-paying for branches we're about to drop. The serializer is cheap; the LLM round-trip is not. Filter early, pay once.
5. **Why double-checked locking in Get?** Concurrent agent loops (e.g. parallel actions) might both miss the cache; without the second check, both fire snapshots. The mutex + re-check is the simplest stampede protection — singleflight would be cleaner but overkill for the teaching scope.

## What this is NOT / 这一节"故意不做"什么

- No real CDP DOMSnapshot pipeline (production captureSnapshot returns ~50 fields per node — `enhanced_snapshot.py` is 800 LOC; we just hand-craft `*DOMNode`).
- No accessibility-tree merge (upstream merges `Accessibility.getFullAXTree` per frame into `EnhancedAXNode`; not the s09 lesson).
- No JS-click-listener detection (upstream runs `getEventListeners()` via Runtime.evaluate; here every visible node is implicitly interactive).
- No hidden-elements-in-iframes hint (the `_count_hidden_elements_in_iframes` 100-line method upstream is interesting but tangential).
- No per-URL cache keying (one cached snapshot, one URL).

## Upstream / 上游对照

- `browser_use/dom/service.py#L35-L150` — `class DomService.__init__`, `__aenter__/__aexit__` (the service shell).
- `browser_use/dom/service.py#L385-L550+` — `_get_all_trees` (the real snapshot pipeline our stub replaces).
- `browser_use/dom/service.py#L70-L182` — `_count_hidden_elements_in_iframes` (deliberately omitted but worth reading).

See `docs/{zh,en}/s09-dom-service.md` for the full walkthrough and `upstream-readings/s09-dom-service.py` for the annotated upstream excerpt.
