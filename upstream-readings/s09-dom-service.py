# Source: browser_use/dom/service.py#L1-L70, L385-L460, L556-L620 (excerpts)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s09.
#
# Three excerpts below:
#   (1) Class header + __init__ — the configuration surface that maps
#       directly onto our Go DOMService struct fields.
#   (2) _get_all_trees() opening — the snapshot pipeline that our
#       SnapshotFunc stubs out (5+ CDP round-trips in production).
#   (3) Task scheduling + timeout retry logic — the production-grade
#       fault handling we omit in s09's teaching scope.
#
# Reading orientation:
#   - Upstream has NO explicit DOM cache. Every step calls the full
#     pipeline. Our Go service adds a Cache + Invalidate-on-Navigation
#     because the teaching scope wants the caller to see the cost of
#     ignoring it. In production you'd add the same cache as soon as
#     you stop calling the LLM on every keystroke.
#   - The "async context manager" pattern (`__aenter__/__aexit__`)
#     exists for symmetry — DomService is owned by the BrowserSession
#     life-cycle even though it does no cleanup itself. We collapse
#     that into a plain struct since our snapshot func has no
#     resources to release.
#   - The timeout + retry plumbing (~50 lines around `asyncio.wait`)
#     is the *operational* heart of the real service. We omit it
#     because s09 teaches the structural skeleton; s12 would add it
#     back as the agent loop becomes resilient.


# ============================================================================
# Part 1 — Class header, configuration knobs
# ============================================================================
# Highlights to look at:
#   - max_iframes / max_iframe_depth — the two iframe-limit knobs.
#     We keep depth (IframeMaxDepth field on DOMService); count is a
#     production safety net with no teaching value here.
#   - viewport_threshold — pixel-distance from the viewport edge in
#     upstream; we simplify to "bbox area cap" so our fixtures don't
#     need a viewport rect.
#   - cross_origin_iframes + paint_order_filtering — two booleans we
#     drop because s09 doesn't model cross-origin and trusts the
#     serializer to filter paint order.


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
        # ↓ Corresponds to our Go DOMService.IframeMaxDepth.
        #   Upstream ships TWO knobs (count + depth); we keep depth.
        max_iframes: int = 100,
        max_iframe_depth: int = 5,
        # ↓ Corresponds to our ViewportThreshold, though the unit
        #   differs: upstream is "pixels beyond viewport edge", we
        #   simplified to "bbox area cap" so test fixtures are self-
        #   contained without a viewport rect.
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
        # ↓ Our Go DOMService has no equivalent because the SnapshotFunc
        #   owns no resources. In s12 with real CDP this becomes
        #   service.Start(ctx).
        return self

    async def __aexit__(self, exc_type, exc_value, traceback):
        pass  # no need to cleanup anything, browser_session auto handles cleaning up session cache


# ============================================================================
# Part 2 — _get_all_trees opening: what our SnapshotFunc stubs out
# ============================================================================
# Highlights to look at:
#   - asyncio.gather of 4 concurrent CDP requests (snapshot, document,
#     AX tree, viewport ratio). In Go you'd use goroutines + a
#     channel join for the same parallelism.
#   - The Runtime.evaluate to detect JS click listeners (lines ~440-535
#     in the real file) is the most production-specific piece — it
#     literally reads getEventListeners() out of DevTools internals.
#     We don't model it; our stub treats every visible node as
#     potentially interactive.

async def _get_all_trees(self, target_id: TargetID) -> TargetAllTrees:
    cdp_session = await self.browser_session.get_or_create_cdp_session(target_id=target_id, focus=False)

    # Wait for the page to be ready first
    try:
        ready_state = await cdp_session.cdp_client.send.Runtime.evaluate(
            params={'expression': 'document.readyState'}, session_id=cdp_session.session_id
        )
    except Exception as e:
        pass  # Page might not be ready yet

    # Get actual scroll positions for all iframes before capturing snapshot
    # ↓ ~50 lines of iframe-scroll-collection elided here. Not in s09.

    # Detect elements with JavaScript click event listeners (without mutating DOM)
    # ↓ ~100 lines of JS-listener detection elided here. Not in s09.

    # Define CDP request factories to avoid duplication
    def create_snapshot_request():
        # ↓ THIS is the line our SnapshotFunc stubs. In production it's
        #   one CDP round-trip returning ~50 fields per node across
        #   N documents (one per iframe).
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

    def create_dom_tree_request():
        return cdp_session.cdp_client.send.DOM.getDocument(
            params={'depth': -1, 'pierce': True}, session_id=cdp_session.session_id
        )


# ============================================================================
# Part 3 — Concurrent task scheduling + retry logic (omitted from s09)
# ============================================================================
# Highlights to look at:
#   - asyncio.wait with a 10s timeout, then a 2s retry — the kind of
#     real-world reliability plumbing that any production service
#     needs. s09 omits it because the lesson is the structural skeleton,
#     not the operational hardening.
#   - The retry_map pattern is interesting: each task gets a lambda
#     that re-creates it. In Go this would be a `func() Result` per
#     task in a map, with the same retry semantics.

    start_cdp_calls = time.time()

    # ↓ Four parallel CDP requests. Our Go SnapshotFunc collapses these
    #   into a single function call because the stub doesn't model
    #   parallelism. A real Go port would use goroutines + a sync.WaitGroup.
    tasks = {
        'snapshot': create_task_with_error_handling(create_snapshot_request(), name='get_snapshot'),
        'dom_tree': create_task_with_error_handling(create_dom_tree_request(), name='get_dom_tree'),
        'ax_tree': create_task_with_error_handling(self._get_ax_tree_for_all_frames(target_id), name='get_ax_tree'),
        'device_pixel_ratio': create_task_with_error_handling(self._get_viewport_ratio(target_id), name='get_viewport_ratio'),
    }

    # ↓ Operational reliability — 10s primary timeout, 2s retry timeout.
    #   s09 has no equivalent because the stub never times out.
    done, pending = await asyncio.wait(tasks.values(), timeout=10.0)

    if pending:
        for task in pending:
            task.cancel()

        retry_map = {
            tasks['snapshot']: lambda: create_task_with_error_handling(create_snapshot_request(), name='get_snapshot_retry'),
            # ... three more retry lambdas ...
        }

        for key, task in tasks.items():
            if task in pending and task in retry_map:
                tasks[key] = retry_map[task]()

        done2, pending2 = await asyncio.wait([t for t in tasks.values() if not t.done()], timeout=2.0)

        if pending2:
            for task in pending2:
                task.cancel()

    # ↓ Failed-task tracking + selective re-raise. s09 just propagates
    #   any SnapshotFunc error to the caller — production wants
    #   per-task fault isolation.
    results = {}
    failed = []
    for key, task in tasks.items():
        if task.done() and not task.cancelled():
            try:
                results[key] = task.result()
            except Exception as e:
                self.logger.warning(f'CDP request {key} failed with exception: {e}')
                failed.append(key)
        else:
            self.logger.warning(f'CDP request {key} timed out')
            failed.append(key)

    if failed:
        raise TimeoutError(f'CDP requests failed or timed out: {", ".join(failed)}')
