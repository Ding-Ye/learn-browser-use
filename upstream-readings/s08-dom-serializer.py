# Source: browser_use/dom/serializer/serializer.py#L43-L150, L882-L935;
#         browser_use/dom/serializer/paint_order.py#L146-L213
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s08.
#
# Three excerpts:
#   (1) DOMTreeSerializer class header + serialize_accessible_elements
#       — the 4-step pipeline our Go Serializer.Serialize mirrors.
#   (2) PaintOrderRemover.calculate_paint_order — upstream uses a
#       disjoint-rect union over paint_order DESC; our Go uses pairwise
#       containment (single-occluder only). Documented in the doc.
#   (3) serialize_tree — the writer. Note the "non-interactive wrappers
#       skip their tag-line but forward children" pattern our writeNode
#       replicates verbatim.


# ─── (1) DOMTreeSerializer header + pipeline ──────────────────────────────

class DOMTreeSerializer:
    """Serializes enhanced DOM trees to string format."""

    # bbox-propagation list. We don't model propagation in s08 (deliberate
    # simplification); the writer's `ancestorIndexed` flag gives equivalent
    # merging for interactive-nested-inside-interactive cases.
    PROPAGATING_ELEMENTS = [
        {'tag': 'a',      'role': None},
        {'tag': 'button', 'role': None},
        {'tag': 'div',    'role': 'button'},
        {'tag': 'span',   'role': 'button'},
        {'tag': 'input',  'role': 'combobox'},
    ]
    # We use 1.0 (must fully contain). Upstream allows 99% to tolerate
    # sub-pixel layout noise.
    DEFAULT_CONTAINMENT_THRESHOLD = 0.99

    def __init__(self, root_node, previous_cached_state=None,
                 enable_bbox_filtering=True, paint_order_filtering=True):
        # 1-based counter == our writer.nextIndex.
        self._interactive_counter = 1
        self._selector_map: DOMSelectorMap = {}
        # "Did this node exist last frame?" drives the `*[N]` "new" prefix.
        # Not in s08; s09 introduces snapshot caching.
        self._previous_cached_selector_map = (
            previous_cached_state.selector_map if previous_cached_state else None
        )

    def serialize_accessible_elements(self):
        """The 4-step pipeline our Serialize() mirrors."""
        self._interactive_counter = 1
        self._selector_map = {}

        # Step 1: hidden / SVG-decorative / disabled subtrees, keep shadow
        # DOM, flag scrollable. Our markHiddenSubtrees only does visibility.
        simplified = self._create_simplified_tree(self.root_node)

        # Step 2: paint-order. Early because step 3 may rearrange the tree.
        if self.paint_order_filtering and simplified:
            PaintOrderRemover(simplified).calculate_paint_order()

        # Step 3: structural optimize — remove non-meaningful wrappers.
        # We achieve the same outcome at write time by NOT emitting
        # tag-lines for non-interactive wrappers (same effect, later
        # stage).
        optimized = self._optimize_tree(simplified)

        # Step 3': bbox filter. Upstream propagates parent bounds to
        # children; we filter only by viewport intersection.
        if self.enable_bbox_filtering and optimized:
            optimized = self._apply_bounding_box_filtering(optimized)

        # Step 4: assign indices. Our writer does this inline.
        self._assign_interactive_indices_and_mark_new_nodes(optimized)

        return SerializedDOMState(_root=optimized,
                                  selector_map=self._selector_map)


# ─── (2) PaintOrderRemover — disjoint-rect-union strategy ─────────────────

class PaintOrderRemover:
    def calculate_paint_order(self):
        # Upstream maintains a UNION of already-painted rectangles. Walking
        # paint_order DESC (top-most first), each node is checked: if
        # fully covered → ignored_by_paint_order. Otherwise add the node
        # to the union to occlude lower layers.
        #
        # Our Go FilterByPaintOrder uses pairwise containment: for each
        # victim, scan later siblings for one that fully contains it.
        # That misses cases where multiple non-overlapping siblings
        # *collectively* cover a node. Trade: ~30 lines vs ~200.
        rect_union = RectUnionPure()
        for _, nodes in sorted(grouped_by_paint_order.items(), key=lambda x: -x[0]):
            to_add = []
            for node in nodes:
                rect = Rect(...)  # build from snapshot_node.bounds
                if rect_union.contains(rect):
                    node.ignored_by_paint_order = True
                # Skip semi-transparent / transparent-bg nodes: they can't
                # occlude what's behind. Our fixture has no such nodes.
                styles = node.original_node.snapshot_node.computed_styles or {}
                if (styles.get('background-color') == 'rgba(0, 0, 0, 0)' or
                        float(styles.get('opacity', '1')) < 0.8):
                    continue
                to_add.append(rect)
            for r in to_add:
                rect_union.add(r)


# ─── (3) serialize_tree — the writer ──────────────────────────────────────

@staticmethod
def serialize_tree(node, include_attributes, depth=0):
    if not node:
        return ''

    # excluded_by_parent → skip self but recurse children.
    # Our writeNode handles this at the top with the same intent.
    if node.excluded_by_parent:
        return '\n'.join(serialize_tree(c, include_attributes, depth)
                         for c in node.children)

    depth_str = depth * '\t'   # upstream tabs; we use 2 spaces
    next_depth = depth

    if node.original_node.node_type == NodeType.ELEMENT_NODE:
        # KEY: only emit a tag-line for nodes the LLM might act on —
        # interactive, scrollable, IFRAME, FRAME. Otherwise forward
        # children at the current depth. Our writeNode does the exact
        # same gating (minus scrollable/iframe).
        if (node.is_interactive or is_any_scrollable
                or node.original_node.tag_name.upper() in ('IFRAME', 'FRAME')):
            next_depth += 1
            # The index format. Upstream uses backend_node_id (`[37291]`);
            # we use a 1-based counter (`[3]`) for token economy.
            if node.is_interactive:
                new_prefix = '*' if node.is_new else ''
                line = (f'{depth_str}{new_prefix}'
                        f'[{node.original_node.backend_node_id}]'
                        f'<{node.original_node.tag_name}')
            # ... build attributes, append ' />' to line, push to output ...
