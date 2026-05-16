# s08 · DOM 序列化 (dom-serializer)

> s07 leaves us with a session lifecycle but no DOM data. s08 introduces
> a `DOMNode` tree (data, decoupled from any session) and a `Serializer`
> that turns it into LLM-friendly text plus a stable index → bbox map.
> Three filters — hidden, viewport, paint-order — run before the text
> emit; together they decide what the LLM actually gets to see.
>
> s07 留给我们的是会话生命周期，但没有 DOM 数据。s08 引入 `DOMNode` 树（一份纯
> 数据，和 session 解耦）和一个 `Serializer`，把这棵树渲染成对 LLM 友好的文本，
> 同时给出稳定的 index → bbox 映射表。三道滤镜——hidden、viewport、paint-order
> ——在文本生成之前依次跑，共同决定 LLM 真正能看到什么。

## What this teaches / 教什么

- **Indices are integers, not selectors.** `[3]<button />` is two tokens
  in the prompt and zero ambiguity in the action. CSS selectors leak the
  page's internal CSS naming convention and don't survive single-page-app
  re-renders.
- **索引是整数而不是选择器**：prompt 里两个 token，动作里零歧义；CSS 选择器会泄
  漏页面的内部命名约定，而且单页应用一刷新就失效。
- **Filtering is a pipeline.** Hidden → viewport → paint-order. Each
  stage has an obvious physical justification, each stage is independently
  testable, each stage propagates its "dropped" verdict to descendants
  by a different rule.
- **过滤是一条流水线**：hidden → viewport → paint-order。每一级有清晰的物理依
  据、可独立测试，对子节点的"判死刑"传播规则也各不相同。
- **Nested interactive elements get merged.** A `<button>` inside an `<a>`
  is one logical click target; the serializer must not hand the LLM two
  indices for it.
- **嵌套的交互元素要合并**：`<a>` 里套 `<button>` 是一个点击目标，序列化器
  不能给 LLM 两个 index。
- **Paint-order occlusion is real.** A modal at `z-index:10` makes the
  body buttons unclickable. The DOM still has them; the LLM must not.
- **paint-order 遮挡是真问题**：z-index:10 的 modal 让背后的按钮真的点不到。
  DOM 里它们还在，但 LLM 看到就只会点空气。

## Run / 运行

```bash
GOWORK=off go run .              # CLI demo: load snapshot.json, print LLM text + selector map
GOWORK=off go test -v ./...      # 7 tests (5 required + 2 bonus)
```

`GOWORK=off` 只是因为根目录 `go.work` 还没把 s08 加进去；模块本身自洽。

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `dom_node.go`           | `DOMNode` + `DOMRect`. The shape of the JSON fixture; the only data structure the serializer reads. |
| `serializer.go`         | `Serializer{ViewportWidth, ViewportHeight}` + `Serialize(root) SerializedDOM`. Runs the pipeline and emits text. |
| `bbox_filter.go`        | `FilterByViewport` + AABB primitives (`rectIntersects`, `rectFullyContains`). |
| `paint_order.go`        | `FilterByPaintOrder` — pairwise occlusion using z-index + doc order as the stacking proxy. |
| `main.go`               | CLI demo wired to `testdata/snapshot.json`. |
| `testdata/snapshot.json`| ~20-node hand-crafted Chromium-shape DOM covering all the edge cases the tests exercise. |
| `testdata/expected.txt` | The golden LLM text. Pin point for `TestSerializeMatchesGolden`. |
| `serializer_test.go`    | 7 tests (5 required + 2 bonus). |

## Key teaching points / 关键学习点

1. **Why filter BEFORE writing, instead of inside the writer?** Because
   the writer should be readable. If "hidden", "off-viewport",
   "occluded" all branched inside `writeNode`, that function would have
   five reasons to skip a node and the bug surface would explode. By
   decomposing into three filters that mark `dropped` / `excludedByOcclusion`
   flags first, the writer's only job is "render the survivors".
2. **Why the unindexed inner button still renders as `<button />`?**
   Because the LLM benefits from knowing the structure. Just dropping
   the inner button means the prompt says `[1]<a href="/search" />` and
   then `Go` text floats with no obvious source. Showing the button
   without an index says "there's a button here, but click the anchor —
   `[1]` is your handle". The merge is about the *index*, not the *line*.
3. **Why paint-order uses pairwise containment, not the disjoint-rect
   union upstream uses?** Upstream's approach handles overlapping
   *fragments* of multiple occluders (think: a sticky header that is
   actually flexbox of five nav links). Pairwise containment only
   catches the case where *one* element fully covers another. That's
   the modal/overlay case which is by far the most common in practice
   and the only one the fixture exercises. The doc explicitly flags the
   simplification.
4. **Why a 1-based session-local index, not the raw `BackendNodeID`?**
   Upstream actually does use `backend_node_id` directly. We use 1-based
   counters because the digits are shorter in the prompt (3 → `[3]` is
   2 tokens; `[12345]` is 4). Cross-snapshot stability is handled
   separately (s09 is where the cache layer lives); s08 just needs the
   indices to be unambiguous within a single Serialize call.
5. **Why the modal in the fixture is partial (`[50,250,600,400]`) rather
   than full-viewport (`[0,0,1280,800]`)?** A full-viewport modal would
   trigger the paint-order filter to also occlude the header, the h1,
   etc. — technically correct under the algorithm but it makes the
   golden test less interesting (just one element survives). The partial
   modal demonstrates the rule on exactly one element (`#behind`) which
   is the cleanest teaching shape.

## What this is NOT / 这一节"故意不做"什么

- No real CDP snapshot ingest — `snapshot.json` is hand-written. s09
  will introduce the orchestration that captures it.
- No shadow DOM. Upstream serializer.py spends ~200 lines on shadow
  DOM (`DOCUMENT_FRAGMENT_NODE`, shadow-host indicators). Decoupling
  cost too high for the lesson.
- No iframe traversal (`content_document` not modeled).
- No accessibility tree (`ax_node`) — the upstream's ARIA-based
  interactivity heuristics live there.
- No `ClickableElementDetector`. Our `Interactive bool` field is
  authored per-node in the fixture; upstream computes it from 250
  lines of heuristics.
- No `_compound_children` for select/file/range inputs.
- No partial-occlusion via disjoint-rect union (see teaching point 3).

## Upstream / 上游对照

- `browser_use/dom/serializer/serializer.py#L43-L300` — `DOMTreeSerializer`
  class. Our `Serializer` is the same shape, much smaller.
- `browser_use/dom/serializer/paint_order.py#L146-L213` —
  `PaintOrderRemover`. Our `FilterByPaintOrder` keeps the same
  z-index-grouped semantics but uses pairwise containment instead of
  the disjoint-rect union.
- `browser_use/dom/views.py#L1-L200` — `EnhancedDOMTreeNode`, `DOMRect`,
  `SerializedDOMState`. Our `DOMNode` + `DOMRect` + `SerializedDOM` are
  the minimum-surface counterparts.

See `docs/{zh,en}/s08-dom-serializer.md` for the long-form walkthrough.
