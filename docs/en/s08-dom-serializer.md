---
title: "s08 · DOM serializer"
chapter: 8
slug: s08-dom-serializer
est_read_min: 15
---

# s08 · DOM serializer

> Teaching focus: the browser has a DOM tree with thousands of nodes; the
> LLM cannot read it. s08 collapses that tree into a few lines of text,
> tagging every clickable element with an integer index. The model just
> says "click 3" and the caller looks up `SelectorMap[3]` to get a BBox.
> Three filters along the way — hidden, viewport, paint-order — decide
> what survives into the prompt and what gets buried. About 600 lines of
> Go in total, 60% of which is the fixture and tests; the core
> `serializer.go` is around 200 lines.

---

## Problem / 问题

With a Session in hand (from s07), the next move is "let the LLM decide
where to click next". But what should the LLM *see*? Three obvious
candidates all fail:

1. **The raw DOM**: tens of thousands of nodes, token budget explodes,
   semantics buried under markup.
2. **A screenshot**: works for vision models (GPT-4V and friends), but
   it's ~2k tokens per turn, slow, expensive, and the model still has
   to figure out *where* to click in image coordinates.
3. **`outerHTML`**: marginally OK, but it drags along every inline
   style, every `<script>`, every `<svg>` child — low signal-to-noise.
   Worse, the LLM's response would be a CSS selector, and CSS selectors
   die the first time an SPA re-renders.

The industry's fourth answer, which we implement here, is **structured
text plus integer indices**:

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

`[1]` `[2]` `[3]` are stable session-local indices. The LLM says
`click 3` in one short token; the caller looks up `SelectorMap[3]` for
the `(x, y, w, h)` and hands it to the s05 actor to click.

Compressing the DOM down to that shape has four non-trivial decisions:

- **Which nodes deserve a tag-line?** A hundred wrapping `<div>`s
  probably don't.
- **Which nodes deserve an index?** Not every clickable thing — an
  `<a>` wrapping a `<button>` is one logical target, and handing the
  LLM two indices for one target invites duplicate clicks.
- **What about invisible nodes?** `display:none`, off-viewport, hidden
  under a modal — all physically unclickable.
- **How do we stay deterministic?** Map iteration is random in Go;
  output that flickers between runs nukes prompt caching and turns
  diffs into noise.

s08 gives each of these a specific, testable answer.

## Solution / 解决方案

We split the transform into **data + pipeline + writer**:

```go
type Serializer struct {
    ViewportWidth, ViewportHeight int
}

func (s *Serializer) Serialize(root *DOMNode) SerializedDOM
//   ↑                                            ↑
//   takes only a DOMNode tree                    returns LLMText + SelectorMap
```

Four pipeline steps:

| Step | File | What it does | Upstream |
|---|---|---|---|
| 1. hidden filter   | `serializer.go::markHiddenSubtrees` | Subtrees under `display:none` / `hidden=true` get `dropped=true`, propagated top-down. | `is_visible` check in `_create_simplified_tree` |
| 2. viewport filter | `bbox_filter.go::FilterByViewport` | Nodes whose BBox doesn't intersect the viewport (plus their descendants) get `dropped=true`. | `_apply_bounding_box_filtering` |
| 3. paint-order filter | `paint_order.go::FilterByPaintOrder` | Nodes *fully covered* by a later-painted (higher z-index) non-ancestor sibling get `excludedByOcclusion=true`, propagated to descendants. | `PaintOrderRemover.calculate_paint_order` |
| 4. write | `serializer.go::writeNode` | Only interactive elements get a `[N]` index and a SelectorMap entry; non-interactive structural wrappers skip the tag-line and forward their children at the same depth. | `serialize_tree` |

The four core types:

```go
type DOMNode struct {
    BackendNodeID int
    Tag, Text     string             // Tag=="" means text node
    Attributes    map[string]string
    Children      []*DOMNode
    BBox          [4]int             // x, y, w, h
    Visible       bool               // from CSS cascade
    Interactive   bool               // from upstream ClickableElementDetector
    ZIndex        int                // from CSS stacking context
}

type DOMRect struct{ X, Y, W, H int }

type SerializedDOM struct {
    LLMText     string
    SelectorMap map[int]DOMRect      // 1-based index → click target
}
```

A deliberate choice: `DOMNode.Interactive` is a **boolean baked into the
fixture**, not something this module computes. Upstream's
`ClickableElementDetector` is 250 lines of heuristics (search-related
class names, ARIA roles, onclick handlers, JS click listeners, the AX
tree, file-input edge cases…). Re-implementing that buries the actual
lesson — the pipeline and the writer. A fixture-authored boolean lets
the test *name the contract* ("this button is interactive") instead of
re-deriving the heuristics.

## How It Works / 工作原理

```
fixture JSON
     │
     ▼
┌──────────────────────────────────────────────────────┐
│             markHiddenSubtrees(root)                 │
│   parent display:none → whole subtree dropped=true   │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│       FilterByViewport(root, {0,0,W,H})              │
│   BBox fully outside viewport (+ descendants)        │
│   marked dropped=true                                │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│       FilterByPaintOrder(flattenDocOrder(root))      │
│   Sort by (z-index asc, doc-order asc).              │
│   For each, check whether any later-painted          │
│   non-ancestor sibling fully contains it →           │
│   excludedByOcclusion=true, propagated downward      │
└──────────────────────────────────────────────────────┘
     │
     ▼
┌──────────────────────────────────────────────────────┐
│              writeNode(root, depth=0)                │
│   ┌────────────────────────────────────────────┐     │
│   │ Non-interactive wrapper (html/body/div...) │     │
│   │  → don't emit a tag-line, forward children │     │
│   ├────────────────────────────────────────────┤     │
│   │ Text node                                  │     │
│   │  → emit text at current indent             │     │
│   ├────────────────────────────────────────────┤     │
│   │ Interactive + no ancestor indexed          │     │
│   │  → assign [N], record SelectorMap[N]       │     │
│   │  → subtree enters "ancestor indexed" mode  │     │
│   ├────────────────────────────────────────────┤     │
│   │ Interactive + ancestor already indexed     │     │
│   │  → still emit <tag />, but no [N]          │     │
│   └────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────┘
     │
     ▼
SerializedDOM{LLMText, SelectorMap}
```

The writer is about 50 lines:

```go
func (w *writer) writeNode(n *DOMNode, depth int) {
    if n == nil { return }
    if n.dropped || n.excludedByOcclusion {
        // Even if WE are dropped, children may still be live (a wrapper
        // div got dropped by viewport but contains an interactive that
        // survived independently). Forward them.
        for _, c := range n.Children { w.writeNode(c, depth) }
        return
    }
    if n.Tag == "" {                                     // text
        t := strings.TrimSpace(n.Text)
        if t != "" { fmt.Fprintf(&w.buf, "%s%s\n", indent(depth), t) }
        return
    }
    if !n.Interactive {                                  // structural wrapper
        for _, c := range n.Children { w.writeNode(c, depth) }
        return
    }
    if !w.ancestorIndexed {                              // first interactive ancestor
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
    // Interactive but suppressed by an ancestor's index: tag-line, no [N].
    fmt.Fprintf(&w.buf, "%s<%s%s />\n", indent(depth), n.Tag, formatAttrs(n))
    for _, c := range n.Children { w.writeNode(c, depth+1) }
}
```

**Four non-obvious points**:

1. **Why an integer index instead of a CSS selector?** Prompt economics.
   `[3]` is two tokens; `document.querySelector("body > main > a.button")`
   is twenty-plus, each step shedding precision, and the class hash
   changes the next time the SPA re-renders. Integer + SelectorMap is
   an *explicit indirection*: the caller translates index → click
   coordinates. The split lets the LLM emit *behavior* while the program
   maintains *stability*.

2. **Why is the bbox filter conservative — a single pixel of intersection
   keeps the node?** False negatives (an existing button vanishes from
   the prompt) and false positives (one extra line in the prompt)
   have asymmetric costs. The latter wastes a few tokens; the former
   makes the whole agent fail to find the login button. So
   `rectIntersects` keeps anything with ≥1 px overlap and leaves
   "almost intersecting" cases on the safe side.

3. **Why pairwise containment for paint-order instead of upstream's
   disjoint-rect union?** Upstream handles the case where multiple
   non-overlapping siblings *collectively* cover a node (five flexbox
   nav links forming a sticky header that covers the body). Our fixture
   only exercises the single-cover case — a modal fully on top of one
   thing. Pairwise containment is the O(n²) direct implementation,
   ~30 lines instead of 200, runs in nanoseconds on a 20-node tree,
   and **the simplification is explicitly flagged** rather than hidden.

4. **Why does the suppressed inner button still render its `<button />`
   line, just without an index?** Because the LLM seeing `[1]<a /> Go`
   with no obvious source for "Go" is confusing. Emitting the inner
   `<button />` tells it "yes, this is semantically a button, but the
   click handle is the outer anchor `[1]`". What's being merged is the
   *index*, not the *structure*.

## What Changed / 与上一节的变化

s07 gave us a browser session lifecycle — a state machine. s08
introduces a **DOM data model** — a pure-function transformer with no
direct coupling to the session:

```diff
- s07: session.Start() → CDP attach → Bus.Emit(SessionStarted) → wait → Stop
+ s08: ser := &Serializer{...}
+      out := ser.Serialize(node)
+      // out.LLMText feeds the LLM prompt; out.SelectorMap feeds the actor
```

The two lines fuse at s09: the `DOMService` there will subscribe to
s07's `NavigationEvent` to invalidate its snapshot cache on URL change,
then re-invoke s08's `Serializer`. So s08 is the passive pure function;
s07 is the active event source; s09 is the glue.

Downstream consumers:

- s09's DOMService calls `serializer.Serialize` from its
  `OnNavigationEvent` handler.
- s12's Agent calls the serializer on every `observe()`. The LLMText
  goes into the prompt; the SelectorMap is kept as the translation
  table.
- s12's `click` action takes an integer index, looks up the rect in the
  current snapshot's SelectorMap, then dispatches to s05's
  `actor.Click(x, y)`.

## Try It / 动手试一试

```bash
cd agents/s08-dom-serializer

# CLI demo: load the fixture, print LLM text + selector map
GOWORK=off go run .

# 7 tests (5 required + 2 bonus)
GOWORK=off go test -v ./...
```

`GOWORK=off` is only because the root `go.work` doesn't list s08 yet;
the module itself is self-contained.

Expected output (excerpt):

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

Test coverage:

- `TestSerializeMatchesGolden` — LLMText byte-equal to
  `testdata/expected.txt`.
- `TestSelectorMapCoversClickable` — one SelectorMap entry per top-level
  clickable (outer `<a>` / standalone `<button>` / `<input>`).
- `TestHiddenElementsDropped` — buttons and text inside `display:none`
  must never appear.
- `TestNestedInteractiveMerged` — `<a>` containing a `<button>` produces
  exactly one index (on the anchor).
- `TestBBoxFilterRemovesOffscreen` — an `<a>` at `y=1900` is excluded
  from both text and SelectorMap when the viewport ends at 800.
- `TestPaintOrderOcclusionDropsBehind` (bonus) — modal at z=10 fully
  covers `#behind` at z=0; #behind's text and attributes must not
  appear.
- `TestEmptyTreeReturnsEmpty` (bonus) — `Serialize(nil)` returns empty
  text and empty map, no panic.

## Upstream Source Reading / 上游源码阅读

Upstream's `browser_use/dom/serializer/serializer.py` is ~1100 lines.
We pull out the four skeletal methods (~80 lines combined) for
side-by-side reading.

```python
# Source: browser_use/dom/serializer/serializer.py#L43-L100

class DOMTreeSerializer:
    """Serializes enhanced DOM trees to string format."""

    # This list defines "propagating elements" — bbox filtering will
    # let their children inherit the parent's bbox. We don't propagate
    # (teaching simplification), but this list is upstream's entry into
    # the "the inner span shouldn't be independently indexed" rule.
    PROPAGATING_ELEMENTS = [
        {'tag': 'a', 'role': None},
        {'tag': 'button', 'role': None},
        {'tag': 'div', 'role': 'button'},
        {'tag': 'div', 'role': 'combobox'},
        {'tag': 'span', 'role': 'button'},
        {'tag': 'span', 'role': 'combobox'},
        {'tag': 'input', 'role': 'combobox'},
    ]
    # 99% of the child's bbox must fall within the parent's bbox to count
    # as "covered". Our paint_order.go uses 100% full containment, i.e.
    # threshold=1.0, as a simplification.
    DEFAULT_CONTAINMENT_THRESHOLD = 0.99

    def __init__(self, root_node, enable_bbox_filtering=True,
                 paint_order_filtering=True, ...):
        # A 1-based counter — exact parallel of our writer.nextIndex.
        self._interactive_counter = 1
        self._selector_map: DOMSelectorMap = {}
```

```python
# Source: browser_use/dom/serializer/serializer.py#L100-L150

def serialize_accessible_elements(self) -> tuple[SerializedDOMState, dict]:
    # This method is the counterpart of our Serialize(). Watch the
    # pipeline ordering:

    # Step 1: simplified tree (includes visibility); maps to our
    # markHiddenSubtrees.
    simplified_tree = self._create_simplified_tree(self.root_node)

    # Step 2: paint-order filter. Upstream runs it second because the
    # next step is "remove non-meaningful wrappers" — paint order has
    # to run before that. We don't optimize wrappers, so paint-order
    # order doesn't matter; we run it third.
    if self.paint_order_filtering and simplified_tree:
        PaintOrderRemover(simplified_tree).calculate_paint_order()

    # Step 3: bbox filter — descendants get constrained by their
    # propagating parent's bbox. Our viewport filter just does
    # "intersects viewport", no parent → child propagation.
    if self.enable_bbox_filtering and optimized_tree:
        filtered_tree = self._apply_bounding_box_filtering(optimized_tree)

    # Step 4: assign interactive indices. Our writer.writeNode assigns
    # indices *while* it emits text; upstream splits these into two
    # passes — assign first, serialize after.
    self._assign_interactive_indices_and_mark_new_nodes(filtered_tree)

    return SerializedDOMState(_root=filtered_tree, selector_map=self._selector_map)
```

```python
# Source: browser_use/dom/serializer/paint_order.py#L154-L213

def calculate_paint_order(self) -> None:
    # Upstream maintains a disjoint-rect union of the already-painted
    # region. It walks paint_order DESC; for each node it first asks
    # contains() — already covered? → ignored_by_paint_order. Then it
    # add()s the node's rect into the union.

    rect_union = RectUnionPure()
    for paint_order, nodes in sorted(grouped_by_paint_order.items(), key=lambda x: -x[0]):
        for node in nodes:
            rect = Rect(...)
            if rect_union.contains(rect):           # already painted over
                node.ignored_by_paint_order = True
            rect_union.add(rect)
    # Our version does pairwise containment. Cost: only single-occluder
    # cases get caught. Benefit: 30 lines instead of 200, and every
    # fixture case is single-occluder.
```

```python
# Source: browser_use/dom/serializer/serializer.py#L882-L935 (excerpt)

@staticmethod
def serialize_tree(node, include_attributes, depth=0) -> str:
    # excluded_by_parent → skip self but forward children. Our parallel
    # is the `if (n.dropped || n.excludedByOcclusion)` branch in
    # writeNode.
    if node.excluded_by_parent:
        # ... forward children ...
        return '\n'.join(formatted_text)

    # The key decision: only emit a tag-line for nodes the LLM might
    # actually act on — is_interactive, is_scrollable, IFRAME, FRAME.
    # Our version handles only is_interactive; scrollable and iframes
    # are deferred.
    if (
        node.is_interactive
        or is_any_scrollable
        or node.original_node.tag_name.upper() == 'IFRAME'
        or node.original_node.tag_name.upper() == 'FRAME'
    ):
        next_depth += 1
        # The other key decision: which [index] format?
        # Upstream uses backend_node_id directly. We use a 1-based
        # session-local counter (shorter token, doesn't leak internal IDs).
        if node.is_interactive:
            new_prefix = '*' if node.is_new else ''
            line = f'{depth_str}{new_prefix}[{node.original_node.backend_node_id}]<{node.original_node.tag_name}'
        # ... attributes, ' />', append to output ...
```

**Reading notes**:

1. **`_create_simplified_tree` vs `markHiddenSubtrees`**: upstream's
   "simplify" pass has five filters (DISABLED_ELEMENTS, SVG children,
   `data-browser-use-exclude` attribute, iframe content_document, shadow
   roots). We only do visibility. The rest is on the explicit teaching
   simplifications list.
2. **`_optimize_tree`**: upstream has a separate step that strips
   non-meaningful wrappers, collapsing `<div><div><div><a /></div></div></div>`
   into `<a />`. We don't run this step; our writer achieves the
   equivalent effect by *not emitting tag-lines for non-interactive
   wrappers*. Essentially we inline `optimize` into the writer.
3. **The `is_new` flag in `_assign_interactive_indices_and_mark_new_nodes`**:
   upstream tracks whether each node appeared in the previous snapshot;
   if not, the prompt shows `*[N]` instead of `[N]` as a "this is new"
   hint. We don't have a snapshot cache yet (s09 introduces that), so
   s08 only renders "now".
4. **`_apply_bounding_box_filtering` propagating bounds**: upstream
   doesn't just filter by viewport — it filters by "inside an interactive
   ancestor's (a/button/...) bbox". Anchor wrapping five spans? The LLM
   only needs the anchor's index. We implement the equivalent merge at
   the *writer* level via `ancestorIndexed`. Same behavior, different
   stage.
5. **`PaintOrderRemover`'s opacity-0.95 skip and transparent-background
   skip**: upstream avoids adding semi-transparent rects to the union
   (you can't be occluded by something see-through). Our fixture has no
   semi-transparent nodes, so we skip the heuristic entirely.
6. **`serialize_tree`'s shadow DOM handling (`Open Shadow` / `Closed
   Shadow` markers)**: upstream spends ~30 lines on shadow DOM rendering.
   We skip the entire concept. Common in real SPAs but not "serializer
   pipeline" teaching material.

**Want to read more**: start at `serializer.py#L60` (`__init__`) and look
at the `enable_bbox_filtering` / `paint_order_filtering` switches.
Production-grade serializers expose these knobs (debug mode wants no
filtering). Then follow `session_id` to the
`data-browser-use-exclude-{session_id}` attribute logic — upstream's
contract for "let the page itself opt elements out of LLM view". s12
doesn't implement this, but you should know it exists.

---

**Next up**: s09 wires s07's Session and s08's Serializer together.
`DOMService` subscribes to `NavigationEvent`, invalidates the snapshot
cache when the URL changes, and re-runs serialization on next observe.
s08 is the passive transformer; s09 is the active scheduler.
