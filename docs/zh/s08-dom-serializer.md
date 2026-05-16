---
title: "s08 · DOM 序列化"
chapter: 8
slug: s08-dom-serializer
est_read_min: 15
---

# s08 · DOM 序列化

> 教什么：浏览器里有一棵 DOM 树，几千个节点；LLM 看不懂。s08 把这棵树压成几行
> 文本，每个可点击元素前面挂一个 `[N]` 整数索引；模型只要说"click 3"，调用方就
> 能在 `SelectorMap[3]` 里查到对应的 BBox。中间三道滤镜——hidden、viewport、
> paint-order——决定哪些节点能进文本、哪些被埋掉。s08 大约 600 行 Go，里面 60%
> 是 fixture + 文档；核心 `serializer.go` 大约 200 行。

---

## Problem / 问题

把 s07 的 session 拿在手里，下一步是"让 LLM 决定下一步点哪儿"。但 LLM 看到的应
该是什么？三个方向都不行：

1. **原始 DOM**：上万节点，token 爆炸；模型也看不懂语义。
2. **截图**：可以做（GPT-4V 之类），但每次 ~2k token、延迟高、价格贵，而且模型
   仍然要自己去框元素位置。
3. **`outerHTML`**：稍微 OK，但带着所有 inline style、`<script>`、`<svg>` 子节
   点等等，信噪比低；并且 LLM 输出的"点击哪个元素"是 CSS 选择器，单页应用 re-
   render 一次就失效了。

工业里收敛出的第四个答案是 **结构化文本 + 整数索引**。看起来像这样：

```
Welcome
[1]<a href="/search" />
  <button type="button" />
    Go
[2]<input name="q" placeholder="Search" type="text" />
Help text
[3]<button type="submit" />
  Submit
MODAL CONTENT
```

`[1]` `[2]` `[3]` 是稳定的会话内索引，LLM 输出 `click 3` 一句话就够了。后面调
用方拿 `SelectorMap[3]` 拿到 `(x, y, w, h)`，再交给 s05 的 actor 去点。

但是把 DOM 压成这个样子有几个非显然的难点：

- **哪些节点要进文本？** 上百个 `<div>` 包装大概率不该进。
- **哪些节点要被打索引？** 不是所有可点击的都该被打——`<a>` 里套一个
  `<button>`，给两个 index 就让 LLM 在一个目标上有两个把手。
- **看不见的节点要不要进？** `display:none`、视口外的、被 modal 盖住的，物理上
  都点不到。
- **怎么保持稳定？** Map 遍历是无序的，每次输出顺序不一样会让 cache 失效、让
  diff 一片红。

s08 要给这四个问题一个具体的、可测试的答案。

## Solution / 解决方案

把转换拆成 **数据 + 流水线 + 写出器** 三件事：

```go
type Serializer struct {
    ViewportWidth, ViewportHeight int
}

func (s *Serializer) Serialize(root *DOMNode) SerializedDOM
//   ↑                                            ↑
//   只接收一棵 DOMNode 树                         输出 LLMText + SelectorMap
```

流水线 4 步：

| 步骤 | 文件 | 做什么 | 上游对照 |
|---|---|---|---|
| 1. hidden 过滤  | `serializer.go::markHiddenSubtrees` | `display:none` / `hidden` 属性的子树全部 `dropped=true`，自顶向下传播 | `_create_simplified_tree` 里 `is_visible` 判断 |
| 2. viewport 过滤 | `bbox_filter.go::FilterByViewport`  | 整个 BBox 都不和视口相交的元素 `dropped=true`，子树继承 | `_apply_bounding_box_filtering` |
| 3. paint-order 过滤 | `paint_order.go::FilterByPaintOrder` | 被后面绘制（更高 z-index）的同级元素**完全覆盖**的节点 `excludedByOcclusion=true`，子节点也传播 | `PaintOrderRemover.calculate_paint_order` |
| 4. 写出 | `serializer.go::writeNode` | 仅对 interactive 元素打 `[N]` 索引 + 进 SelectorMap；非交互的"结构 wrapper"不打标签只递归 | `serialize_tree` |

四个核心数据类型：

```go
type DOMNode struct {
    BackendNodeID int
    Tag, Text     string             // Tag=="" 表示文本节点
    Attributes    map[string]string
    Children      []*DOMNode
    BBox          [4]int             // x, y, w, h
    Visible       bool               // 来自 CSS cascade
    Interactive   bool               // 来自上游 ClickableElementDetector
    ZIndex        int                // 来自 CSS stacking context
}

type DOMRect struct{ X, Y, W, H int }

type SerializedDOM struct {
    LLMText     string
    SelectorMap map[int]DOMRect      // 1-based 索引 → 点击位置
}
```

注意 `DOMNode.Interactive` 是一个 **写死在 fixture 里的 bool**，而不是由代码计
算出来的。理由是：上游 `ClickableElementDetector` 是 250 行启发式（搜索关键
class、aria role、onclick handler、JS 监听器、ARIA tree、文件 input 例外……），
把它从语义里抠掉就丢了 s08 真正想教的"流水线 + 写出器"两件事。fixture 写死的好
处是测试能直接命名"这个 button 应该被算作 interactive"这个契约，不是再实现一遍
启发式。

## How It Works / 工作原理

```
fixture JSON
     │
     ▼
┌──────────────────────────────────────────────────────┐
│             markHiddenSubtrees(root)                 │
│   parent display:none → 整个子树 dropped=true        │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│       FilterByViewport(root, {0,0,W,H})              │
│   BBox 完全在视口外的元素 + 子树 dropped=true        │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│       FilterByPaintOrder(flattenDocOrder(root))      │
│   按 (z-index asc, doc-order asc) 排序，             │
│   逐个检查"是否有后绘制的兄弟完全覆盖我"，是→        │
│   excludedByOcclusion=true，连带子树                 │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│              writeNode(root, depth=0)                │
│   ┌────────────────────────────────────────────┐     │
│   │ 非交互 wrapper（<html>、<body>、<div>...）  │     │
│   │  → 不打 tag 行，直接 forward 子节点         │     │
│   ├────────────────────────────────────────────┤     │
│   │ 文本节点                                    │     │
│   │  → 在当前缩进打文本                         │     │
│   ├────────────────────────────────────────────┤     │
│   │ 交互元素 + ancestorIndexed=false            │     │
│   │  → 分配 [N] 索引 + 进 SelectorMap           │     │
│   │  → 子树进入"已被索引祖先"模式               │     │
│   ├────────────────────────────────────────────┤     │
│   │ 交互元素 + ancestorIndexed=true             │     │
│   │  → 仍打 <tag /> 行（但不打 [N]）            │     │
│   └────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────┘
     │
     ▼
SerializedDOM{LLMText, SelectorMap}
```

核心 writer 大约 50 行：

```go
func (w *writer) writeNode(n *DOMNode, depth int) {
    if n == nil { return }
    if n.dropped || n.excludedByOcclusion {
        // 即使自己被丢掉，子节点里可能还有活的（比如 wrapper 被
        // 视口过滤掉但 wrapper 里的独立交互元素还该显示）。
        for _, c := range n.Children { w.writeNode(c, depth) }
        return
    }
    if n.Tag == "" {                                     // 文本
        t := strings.TrimSpace(n.Text)
        if t != "" { fmt.Fprintf(&w.buf, "%s%s\n", indent(depth), t) }
        return
    }
    if !n.Interactive {                                  // 结构 wrapper
        for _, c := range n.Children { w.writeNode(c, depth) }
        return
    }
    if !w.ancestorIndexed {                              // 头一个交互祖先
        idx := w.nextIndex
        w.nextIndex++
        w.selectorMap[idx] = rectFromBBox(n.BBox)
        fmt.Fprintf(&w.buf, "%s[%d]<%s%s />\n", indent(depth), idx, n.Tag, formatAttrs(n))
        prev := w.ancestorIndexed
        w.ancestorIndexed = true
        for _, c := range n.Children { w.writeNode(c, depth+1) }
        w.ancestorIndexed = prev
        return
    }
    // 交互但被祖先吞并：打 <tag /> 不打 [N]
    fmt.Fprintf(&w.buf, "%s<%s%s />\n", indent(depth), n.Tag, formatAttrs(n))
    for _, c := range n.Children { w.writeNode(c, depth+1) }
}
```

**4 个非显然之处**：

1. **为什么 index 是整数而不是 CSS 选择器？** prompt 经济学。`[3]` 是 2 个
   token；`document.querySelector("body > main > a.button")` 是 20+ 个，每一步
   都掉精度，而且单页应用 re-render 之后 class hash 就不一样了。整数+
   SelectorMap 是"显式的 indirection"，调用方负责把 index 翻译回点击坐标——这
   职责拆分让 LLM 输出"行为"，让程序代码维持"稳定性"。

2. **为什么 bbox 过滤要保守，不在边界上"差点点中也算视口外"？** 因为漏掉一个
   按钮（false negative）和多渲染一个按钮（false positive）代价完全不对称。
   多一行 LLM prompt 就是几个 token 的事；少一行让 LLM 找不到登录按钮，整个
   agent run 就崩了。`rectIntersects` 只要有 1 像素重叠就保留。

3. **为什么 paint-order 要按 z-index 排序后用 pairwise containment，而不是上游
   的 disjoint-rect-union？** 上游对的是"5 个并排的 nav link 在 sticky header
   里组合成一个遮挡区"这种 *合并* 的情况——某个元素被多个不重叠的兄弟联手盖
   住。我们的 fixture 只覆盖"modal 整个盖住底下一块"这一种 single-cover 形
   态。Pairwise containment 是这一形态的 O(n²) 直球做法，~20 节点跑起来纳秒
   级，代码 30 行而不是 200。**取舍是显式标注出的，不是漏写的**。

4. **为什么嵌套交互的内层 button 还要打 `<button />` 行（只是不打 `[N]`）？**
   因为 LLM 看到 `[1]<a /> Go` 但不知道 "Go" 是怎么来的，会很困惑。打出内层
   `<button />` 告诉它"这是个语义上的按钮，但你的点击把手是外层 anchor `[1]`"。
   合并的是 **索引**，不是 **结构**。

## What Changed / 与上一节的变化

s07 给的是浏览器会话生命周期，是个状态机；s08 引入的是 **DOM 数据模型**，是一
份纯数据 transformer，和 session 不直接耦合：

```diff
- s07: session.Start() → CDP attach → Bus.Emit(SessionStarted) → wait → Stop
+ s08: ser := &Serializer{...}
+      out := ser.Serialize(node)
+      // out.LLMText 喂给 LLM；out.SelectorMap 喂给 actor
```

两条线的合点在 s09：s09 的 `DOMService` 会订阅 s07 的 `NavigationEvent` 在 URL
变化时失效 snapshot 缓存，然后调用 s08 的 `Serializer` 重做文本。也就是说 s08
是一个被动的纯函数，s07 是主动的事件源——s09 把它们焊起来。

后续会反复用到：

- s09 的 DOMService 在它的 `OnNavigationEvent` 里调 `serializer.Serialize`。
- s12 的 Agent 在每一步 `observe()` 都会调 serializer，把 LLMText 喂给 prompt，
  把 SelectorMap 留作"翻译表"。
- s12 的 `click` action 接收一个整数 index，去当前快照的 SelectorMap 里查 rect，
  再走 s05 actor 的 `Click(x, y)`。

## Try It / 动手试一试

```bash
cd agents/s08-dom-serializer

# CLI demo：跑一遍 fixture，打印 LLM 文本 + selector map
GOWORK=off go run .

# 7 个测试（5 个必需 + 2 个加分）
GOWORK=off go test -v ./...
```

`GOWORK=off` 是因为根目录 `go.work` 还没把 s08 加进去；模块本身自洽。

期望输出（节选）：

```
# LLM-facing serialization

Welcome
[1]<a href="/search" />
  <button type="button" />
    Go
[2]<input name="q" placeholder="Search" type="text" />
Help text
[3]<button type="submit" />
  Submit
MODAL CONTENT

# SelectorMap (3 entries)
  [1] → x=16 y=120 w=100 h=32
  [2] → x=16 y=180 w=300 h=32
  [3] → x=16 y=260 w=100 h=32
```

测试覆盖：

- `TestSerializeMatchesGolden` — LLMText 与 `testdata/expected.txt` 一字不差。
- `TestSelectorMapCoversClickable` — 顶层可点击元素（外层 `<a>` / 独立 `<button>`
  / `<input>`）各一项 SelectorMap entry。
- `TestHiddenElementsDropped` — `display:none` 子树里的按钮和文本都不能出现。
- `TestNestedInteractiveMerged` — `<a>` 里套 `<button>`，只有外层 `<a>` 打 index。
- `TestBBoxFilterRemovesOffscreen` — `y=1900` 的 `<a>` 整个不进文本，也不进
  SelectorMap。
- `TestPaintOrderOcclusionDropsBehind`（加分）— modal（z=10）完全盖住 `#behind`
  （z=0），#behind 的文本和属性都不能出现。
- `TestEmptyTreeReturnsEmpty`（加分）— `Serialize(nil)` 返回空文本和空 map，不
  panic。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/dom/serializer/serializer.py` ~1100 行。我们抽出最骨架的 4 个
方法（合在一起 ~80 行）做对照。

```python
# Source: browser_use/dom/serializer/serializer.py#L43-L100

class DOMTreeSerializer:
    """Serializes enhanced DOM trees to string format."""

    # 这一组是 "propagating elements"——bbox 过滤会让它们的子节点继承父 bbox。
    # 我们 s08 不做 propagation（教学简化），但这个清单是上游处理"按钮的子
    # span 不该被独立索引"那条规则的入口。
    PROPAGATING_ELEMENTS = [
        {'tag': 'a', 'role': None},
        {'tag': 'button', 'role': None},
        {'tag': 'div', 'role': 'button'},
        {'tag': 'div', 'role': 'combobox'},
        {'tag': 'span', 'role': 'button'},
        {'tag': 'span', 'role': 'combobox'},
        {'tag': 'input', 'role': 'combobox'},
    ]
    # 99% 的子元素 bbox 要落在父 bbox 内才算"被父覆盖"。我们 paint_order.go
    # 里用的是 100% 完全包含，threshold=1.0 的简化版。
    DEFAULT_CONTAINMENT_THRESHOLD = 0.99

    def __init__(self, root_node, enable_bbox_filtering=True,
                 paint_order_filtering=True, ...):
        # 一个 1-based 计数器；和我们 writer.nextIndex 完全对应。
        self._interactive_counter = 1
        self._selector_map: DOMSelectorMap = {}
```

```python
# Source: browser_use/dom/serializer/serializer.py#L100-L150

def serialize_accessible_elements(self) -> tuple[SerializedDOMState, dict]:
    # 这一段是我们 Serialize() 的对应物。看流水线顺序：

    # Step 1: 简化树（含可见性判断），我们对应 markHiddenSubtrees。
    simplified_tree = self._create_simplified_tree(self.root_node)

    # Step 2: paint-order 过滤。上游在 step 2 跑，我们在 step 3 跑——区别
    # 在于上游下一步会"优化树形"（去掉空 wrapper），我们没这一步，所以
    # paint_order 顺序无所谓。
    if self.paint_order_filtering and simplified_tree:
        PaintOrderRemover(simplified_tree).calculate_paint_order()

    # Step 3: bbox 过滤——按 propagating 父框约束子节点。我们的 viewport
    # 过滤只做"和视口相交"这一件事，没有父→子的 propagation。
    if self.enable_bbox_filtering and optimized_tree:
        filtered_tree = self._apply_bounding_box_filtering(optimized_tree)

    # Step 4: 分配 interactive 索引。我们的 writer.writeNode 在生成文本的
    # 同时就分配，上游分了两步——先 assign index，再 serialize text。
    self._assign_interactive_indices_and_mark_new_nodes(filtered_tree)

    return SerializedDOMState(_root=filtered_tree, selector_map=self._selector_map)
```

```python
# Source: browser_use/dom/serializer/paint_order.py#L154-L213

def calculate_paint_order(self) -> None:
    # 上游用 disjoint-rect union 维护"已经被绘制的区域"。
    # 按 paint_order DESC 遍历，对每个节点先 contains() 检查是否已被覆
    # 盖（被覆盖→ignored_by_paint_order），然后再 add() 加进 union。

    rect_union = RectUnionPure()
    for paint_order, nodes in sorted(grouped_by_paint_order.items(), key=lambda x: -x[0]):
        for node in nodes:
            rect = Rect(...)
            if rect_union.contains(rect):           # 被前面绘制的盖住了
                node.ignored_by_paint_order = True
            rect_union.add(rect)
    # 我们的版本用 pairwise containment。代价是 single-occluder 才能命中；
    # 收益是 30 行代码而不是 200，且 fixture 想测的 case 完全够用。
```

```python
# Source: browser_use/dom/serializer/serializer.py#L882-L935 (节选)

@staticmethod
def serialize_tree(node, include_attributes, depth=0) -> str:
    # excluded_by_parent → 跳过自己但 forward 子节点。我们的对应是
    # writeNode 里 if (n.dropped || n.excludedByOcclusion) 那个分支。
    if node.excluded_by_parent:
        # ... forward children ...
        return '\n'.join(formatted_text)

    # 关键判断：只对真正"该让 LLM 知道存在"的元素打 tag 行。
    # is_interactive、is_scrollable、IFRAME、FRAME。
    # 我们这一版只判断 is_interactive；scrollable 和 iframe 都不支持。
    if (
        node.is_interactive
        or is_any_scrollable
        or node.original_node.tag_name.upper() == 'IFRAME'
        or node.original_node.tag_name.upper() == 'FRAME'
    ):
        next_depth += 1
        # 关键：打 [backend_node_id] 还是不打 index？
        # 上游的 index 直接是 backend_node_id；我们用 1-based session-
        # local counter（短 + 不泄漏内部 ID）。
        if node.is_interactive:
            new_prefix = '*' if node.is_new else ''
            line = f'{depth_str}{new_prefix}[{node.original_node.backend_node_id}]<{node.original_node.tag_name}'
        # ... 拼属性、加 />、append 到输出 ...
```

**对照阅读要点**：

1. **`_create_simplified_tree` → `markHiddenSubtrees`**：上游的"简化"动作含 5
   种过滤（DISABLED_ELEMENTS、SVG 子节点、`data-browser-use-exclude` 属性、
   iframe content_document、shadow root）。我们只做 visibility。其余是上游的
   "教学简化"清单上的项。
2. **`_optimize_tree`**：上游有一步 "remove non-meaningful wrappers"，把
   `<div><div><div><a /></div></div></div>` 压成 `<a />`。我们不做这步；我们的
   writer 通过"非交互不打 tag 行"实现了等价效果——本质上是把 optimize 步骤
   inline 进了 writer。
3. **`_assign_interactive_indices_and_mark_new_nodes` 里的 `is_new` 标志**：上
   游记录这个节点是否在上一帧出现过；如果是新的，prompt 里打 `*[N]` 而不是
   `[N]`，提示 LLM "这是个新元素"。s09 引入 cache 之后我们才会处理新/旧；s08
   只看"现在"。
4. **`_apply_bounding_box_filtering` 的 propagating bounds**：上游的过滤不只是
   "在视口里"，而是"在某个交互祖先（a/button/...）的 bbox 里就跳过"，因为锚点
   里套 5 个 span 的情况下，LLM 只需要那个 anchor 的 index。我们用 writer 的
   `ancestorIndexed` flag 在写出层做了对应的合并。两种实现等价，上游的位置选
   在"过滤"，我们在"写出"——位置不同但行为相同。
5. **`PaintOrderRemover` 的 0.95 opacity 跳过 + transparent background 跳过**：
   上游会跳过半透明的元素加入 union（半透明的盖不住），又是一组细节启发式。我
   们的 fixture 没有半透明节点，所以直接省了。
6. **`serialize_tree` 的 shadow DOM 处理（`Open Shadow` / `Closed Shadow` 标
   记）**：上游 ~30 行 shadow DOM 渲染，我们整段省。SPA 真实场景这块很常用，但
   不属于"序列化器原理"教学。

**想读更多**：从 `serializer.py#L60` 的 `__init__` 入手看 `enable_bbox_filtering`
和 `paint_order_filtering` 这两个开关——上游会让调用方按 use case 关掉过滤
（比如 debug 模式），这是 production-grade 序列化器都该有的旋钮。沿着
`session_id` 字段会跳到 `data-browser-use-exclude-{session_id}` 属性的处理逻
辑，那是上游"让网页主动声明'这个元素别给 LLM 看'"的接口；s12 不实现，但作为
合约要知道它存在。

---

**下一节预告**：s09 把 s07 的 Session 和 s08 的 Serializer 焊在一起：
`DOMService` 订阅 `NavigationEvent`，URL 一变就 invalidate snapshot cache，下次
agent 再 observe 时重跑序列化。s08 是这条线的"被动转换器"，s09 是它的"主动调
度者"。
