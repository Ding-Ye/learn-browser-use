---
title: "s09 · DOM 服务"
chapter: 9
slug: s09-dom-service
est_read_min: 14
---

# s09 · DOM 服务

> 教什么：s08 给了我们一个无状态的序列化器 `tree → text`。真实的 agent loop 每一步都会调一次 Get，会在页面之间导航，会面对需要被砍掉的巨大 DOM 树。s09 把序列化器包进 `DOMService`，由它持有 Cache + Snapshot 驱动 + EventBus 订阅。整章大约 500 行 Go，核心 `dom_service.go` 只有 ~120 行，剩下是配套类型和测试。

---

## Problem / 问题

s08 结束时手里的东西是这样的：

```go
// s08 的典型用法
state := Serialize(domTree)         // 纯转换
text := state.LLMText
indexedRects := state.SelectorMap   // map[int]DOMRect
```

序列化器是无状态函数。对于"做转换"这件事这个形状是对的，但对于 agent loop 实际要做的事情就不够了。更具体的痛点：

1. **每一步都要从头再序列化一遍**：LLM 选一个 action，action 执行完了，下一步又要把同一棵 DOM 序列化一次。每次都重新跑 snapshot pipeline + 序列化器是昂贵的——上游 `_get_all_trees` 每次都要发 5+ 个 CDP round-trip（`DOMSnapshot.captureSnapshot`、`DOM.getDocument`、每个 frame 一次 `Accessibility.getFullAXTree`、`Runtime.evaluate` 拿 iframe 滚动位置 + 点击监听器等）。
2. **没有缓存失效纪律**：如果什么都缓存、永远不失效，LLM 会在导航后用旧 DOM 操作。如果失效太激进（比如每次 action 之后都清），缓存本身就没用。
3. **没有 iframe 深度限制**：生产环境的网站会嵌套 6+ 层广告 iframe。全部走完会爆 prompt token 预算；按可配深度截断才能让 agent 负担得起。
4. **没有视口/面积过滤**：1920×1080 的 `<main>` 包装层不是可点击元素；保留它会让 LLM 困惑。需要在序列化器看到这些节点之前就把它们扔掉。

s09 解决这四件事。形状还是 s07 用过的标准 Go 组合：一个 struct 装下所有活动部件，方法暴露生命周期。

## Solution / 解决方案

引入 `DOMService`，一个组合容器：

```go
type DOMService struct {
    Cache    *Cache       // 基于 TTL；支持显式 Invalidate
    Bus      *EventBus    // 订阅 NavigationEvent
    Snapshot SnapshotFunc // 可插拔生产者；s09 是 stub，s12 是 CDP

    CurrentURL        string
    IframeMaxDepth    int  // 上游 max_iframe_depth，本节默认 100
    ViewportThreshold int  // bbox 面积上限；0 表示不过滤
}
```

只暴露两个公共方法：

| 方法 | 责任 | 上游对照 |
|---|---|---|
| `Get(ctx)`     | 缓存新鲜则返回缓存；否则 Snapshot → 过滤 → Serialize → Cache.Set → 返回。 | `DomService.get_serialized_state`（概念上；上游每步内联调 captureSnapshot） |
| `Invalidate()` | 丢弃当前缓存。被 NavigationEvent 处理器调用，也暴露给外部做手动刷新。 | 上游调用方手动 `self._dom_cache = None` |

**bus 订阅是在 `NewDOMService` 构造时完成的**——这是有意的，后面会展开。

为了 self-contained，本节本地重声明了三个上一节才出现的东西——这是 learn-browser-use 的硬性约束（无跨 session import）：

| 本地文件 | 重声明的概念 | 上一节出处 |
|---|---|---|
| `dom_node.go`   | `DOMNode`（Tag、Text、BBox、Visible、Children、BackendNodeID） | s08 |
| `serializer.go` | `Serialize` + `SerializedState` + `SelectorEntry`              | s08 |
| `eventbus.go`   | `EventBus`（Subscribe + Emit）+ `NavigationEvent`              | s06 / s07 |

这里的序列化器是 s08 的精简版——本节的舞台属于服务的生命周期，不属于 paint-order 那些细枝末节。

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
       Cache.Get() ──── 命中 ───► 返回缓存的 SerializedState
                  └─── 未命中 ─► Snapshot(CurrentURL) → applyFilters → Serialize → Cache.Set → return

  bus.Emit(ctx, NavigationEvent{URL: "https://b.example.com"})
  ───────────────────────────────────────────────────────────►
       构造时注册的 handler → Cache.Invalidate()

  service.Get(ctx)   ← Navigate 之后再调一次
  ───────────────►
       Cache.Get() ── 未命中 (Data == nil) ──► 重新 Snapshot → 全新文本
```

核心代码（~50 行）：

```go
// dom_service.go (节选)
func NewDOMService(bus *EventBus, snap SnapshotFunc, cache *Cache) *DOMService {
    s := &DOMService{
        Cache:    cache,
        Bus:      bus,
        Snapshot: snap,
        IframeMaxDepth:    100,
        ViewportThreshold: 0,
    }
    s.subscribe()                          // ← 构造时就完成 bus 订阅
    return s
}

func (s *DOMService) subscribe() {
    s.Bus.Subscribe("NavigationEvent", func(ctx context.Context, e Event) error {
        s.Cache.Invalidate()
        return nil
    })
}

func (s *DOMService) Get(ctx context.Context) (*SerializedState, error) {
    if cached, ok := s.Cache.Get(); ok {   // ← 主路径：缓存命中
        return cached, nil
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    if cached, ok := s.Cache.Get(); ok {   // ← 双重检查：另一个 goroutine 抢先填好了
        return cached, nil
    }
    root, err := s.Snapshot(s.CurrentURL)  // ← 昂贵的那一步
    if err != nil { return nil, fmt.Errorf("dom snapshot: %w", err) }

    pruned := s.applyFilters(root)         // ← IframeMaxDepth + ViewportThreshold
    state := Serialize(pruned)             // ← s08 风格的转换
    s.Cache.Set(state)
    return state, nil
}
```

**四个不那么显然的点**：

1. **为什么 TTL 和显式 Invalidate 同时存在？** 两种互补的触发表面。显式失效是精确的：当我们*知道*页面跳转了（bus 告诉我们了），立刻失效。TTL 是兜底——JS 改 DOM、XHR 引起的变更、我们忘了订阅事件的情况都靠它。没有 TTL，"漏掉一个事件"就变成了"缓存永久卡死"；没有显式失效，每次导航都要等一个完整 TTL。两个都要，不是二选一。
2. **为什么构造时订阅而不是首次 Get 时懒加载？** 因为 bus 可能在 Get 被调用之前就触发。设想：agent 构造完 service，导航事件发生了，*然后*第一次 step 才调用 Get。懒订阅的话，导航事件就丢了，第一次 Get 会拿到启动时那个旧页面。构造时订阅的话，失效信号会到（缓存空所以是 no-op）然后第一次 Get 正确地为新页面拍快照。`TestSubscribedAtConstruction` 测试就是这个场景。
3. **为什么 Get 里要用双重检查锁？** 两个并发 goroutine 可能都未命中，都拿到锁（串行），如果不二次检查就都会触发一次 snapshot。代价是最坏情况下两次 `Cache.Get`；收益是 N 个 goroutine 同时未命中时省下 N-1 次 snapshot。`singleflight` 才是这件事的标准答案，但要额外引依赖反而遮蔽了课程的重点。
4. **为什么 `applyFilters` 是先深度后面积？** 深度修剪是结构性的——它复制一棵树，把某些分支截短。面积过滤再扫一遍幸存的树。反过来先做面积，会访问那些马上要在深度阶段被扔掉的节点。在 4 节点的测试 fixture 上差别可忽略，但在 10k 节点的真实树上顺序就对了。

## What Changed / 与上一节的变化

s08 的写法：

```diff
- // s08 用法 —— 调用方手动驱动 snapshot + 序列化
- domTree := captureRawSnapshot(url)        // ← 调用方负责
- state := Serialize(domTree)               // ← 无状态
- // 下一步：调用方决定是否重新 snapshot
```

s09 之后：

```diff
+ service := NewDOMService(bus, snapshotFunc, NewCache(30*time.Second))
+ service.CurrentURL = "https://a.example.com"
+ state, _ := service.Get(ctx)              // ← 缓存未命中；snapshot 触发
+ state2, _ := service.Get(ctx)             // ← 缓存命中；同样的文本
+
+ // 导航：
+ service.CurrentURL = "https://b.example.com"
+ bus.Emit(ctx, NavigationEvent{URL: "..."}) // ← 失效缓存
+ state3, _ := service.Get(ctx)             // ← 缓存未命中；新的 snapshot
```

最关键的变化：**序列化器不再是入口**。s08 里调用方必须记得 snapshot、序列化，并在导航后重新 snapshot；每件事都是调用点的独立责任。s09 之后只剩 `service.Get(ctx)` 一行。缓存、导航处理、过滤器旋钮全都被藏在这一个方法背后。

本章的输出会被下游复用：

- s12 的 Agent 会拥有一个 `DOMService`，在每一步开头调 `Get`。
- s_full 的"故意省略"表会列出 `_get_all_trees` 真实实现中我们 stub 掉的具体片段。

## Try It / 动手试一试

```bash
cd agents/s09-dom-service

# 完整的缓存 + 失效演示
GOWORK=off go run .

# 6 个测试
GOWORK=off go test -v ./...
```

(`GOWORK=off` 只是因为根目录 `go.work` 还没把 s09 加进 use 列表；模块本身是自洽的。)

期望输出（节选）：

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

测试覆盖：

- `TestSnapshotIsCached` — 连续两次 Get 必须只触发一次 snapshot。
- `TestNavigationInvalidates` — 发 NavigationEvent → 下一次 Get 重新拉，并且文本反映新页面。
- `TestCacheTTLExpires` — 把注入的 clock 推到 TTL 之外 → 下一次 Get 重新拉，不需要任何事件。
- `TestIframeMaxDepthEnforced` — 传一棵 4 层深的树，max depth = 2 → 只剩前 3 层。
- `TestViewportThresholdFilters` — 1000×1000 的容器丢弃；它 50×50 的子节点保留。
- `TestSubscribedAtConstruction`（额外）— 在第一次 Get *之前* 触发 navigate → 第一次 Get 仍能拿到 page B。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/dom/service.py::DomService` ~1,200 行：真 CDP 调度（frame tree 遍历、AX 合并、computed-style 拉取）、iframe 处理、隐藏元素提示、observability 装饰器。s09 只取骨架。

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
        # ↓ 对应我们 Go 端的 DOMService.IframeMaxDepth —— 上游
        #   提供了两个旋钮（数量 + 深度），我们只保留深度。
        max_iframes: int = 100,
        max_iframe_depth: int = 5,
        # ↓ 对应我们 ViewportThreshold，但单位不一样：上游是
        #   "距离视口边的像素"，我们简化为"bbox 面积上限"，
        #   这样测试 fixture 可以自洽。
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
# Source: browser_use/dom/service.py#L385-L450 (大量节选)

async def _get_all_trees(self, target_id: TargetID) -> TargetAllTrees:
    cdp_session = await self.browser_session.get_or_create_cdp_session(target_id=target_id, focus=False)

    # 等待页面 ready
    try:
        ready_state = await cdp_session.cdp_client.send.Runtime.evaluate(
            params={'expression': 'document.readyState'}, session_id=cdp_session.session_id
        )
    except Exception:
        pass

    # ↓ 对应我们 Go 端的一次 SnapshotFunc 调用。
    #   生产环境这里是 5+ 个 CDP round-trip 的合集。
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

    # 并发跑 snapshot + document + AX tree
    snapshot, document, ax_tree = await asyncio.gather(
        create_snapshot_request(),
        cdp_session.cdp_client.send.DOM.getDocument(
            params={'depth': -1, 'pierce': True},
            session_id=cdp_session.session_id,
        ),
        self._get_merged_ax_tree(cdp_session),
    )
    # ... 合并成 EnhancedDOMTreeNode 然后返回
```

**阅读笔记**：

1. **`async def __aenter__/__aexit__`**：上游把 service 用作 async context manager —— `async with DomService(...) as dom: ...`。Go 端没有对应物，因为我们不开按调用计算的 CDP session；snapshot 函数是纯的。s12 注入真 CDP 时会同理加 `Close()`。
2. **`cross_origin_iframes: bool`**：上游用来决定是否走进跨源 iframe。真站点有不同源的广告 iframe，访问它要多一次 CDP round-trip，还可能撞上 iframe 自己的 load 时序。我们丢掉了这个旋钮——stub 树不模拟跨源。
3. **`paint_order_filtering: bool`**：上游用 CDP 的 paint-order 字段砍掉被遮挡的元素（z 轴位于其他元素后面）。这件事归 s08（序列化器的活）；s09 信任序列化器交回来的任何东西。
4. **`asyncio.gather` 3 个请求**：上游并发跑 snapshot + document + AX tree，因为在同一个 CDP session 上这三个可以并行。我们的 `SnapshotFunc` 是同步的；真 Go 端口要用 goroutine + channel 做同样的并行。
5. **`return_exceptions=True`（line 370）**：上游对每个 child frame 的 AX tree 拉取都是 best-effort——一个 detached 的 iframe 不能让整次 snapshot 崩。我们的 stub 不会出错；s12 上真 CDP 时需要同样的 per-frame 守卫。
6. **上游的缓存失效**：根本没有显式缓存。每一步都从头跑一次 snapshot pipeline。我们教的这套优化（"两次调用之间缓存，在 Navigation 时失效"）是一处有意分歧——它让 agent loop 在本地原型时（没接真 LLM）也跑得起。代价：原本"只在拍快照那一刻完全正确"的快照变成了"在下一次 NavigationEvent 之前都视为正确"，TTL 作为兜底。

**接下来读什么**：从 `DomService.__init__` 跳到 `_get_all_trees`（line 385）看真实的全 pipeline，再回到 `_count_hidden_elements_in_iframes`（line 70）看我们丢掉的 LLM-hint 逻辑，最后看 `DOMWatchdog` 在 `browser_use/browser/events.py` 里怎么调 `DomService` —— watchdog 就是 `BrowserSession` 把 DOM 接入 bus 的方式。

---

**下一节预告**：s10 引入 token 计费 —— `TokenCost` 累积每次调用的 token 数，并根据嵌入的定价数据算出美元成本。s10 不依赖 s09（它挂在 LLM provider 上而不是 DOM 上），但这是 agent loop 接下来要解决的下一个问题。
