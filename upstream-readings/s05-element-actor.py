# Source: browser_use/actor/element.py#L62-L100, L268-L325, L521-L526
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s05.
#
# This is the REAL Element class — the production CDP-driven cousin of
# our Go Element + RecordingCDPClient pair. Three excerpts below:
#   (1) class init — where backend_node_id lives
#   (2) click() — the modifier bitmask + 3-frame dispatch core
#   (3) focus() — the 5-line method we mirror almost verbatim in Go
#
# Reading orientation:
#   - upstream uses cdp-use's typed-method shape:
#       self._client.send.Input.dispatchMouseEvent(params=..., session_id=...)
#     Our Go collapses every CDP method into one string-keyed Send(),
#     trading per-domain type-safety for stdlib-only simplicity.
#   - upstream Element holds a back-reference to BrowserSession; we drop
#     that in s05 because the session lifecycle doesn't arrive until s07.
#   - The "geometry" code (getContentQuads / getBoxModel / scrollIntoView /
#     viewport clamping) is elided here; you'll find it at L101-L267. s09
#     is where layout enters the curriculum.


# ============================================================================
# Part 1 — Class definition + backend_node_id field
# ============================================================================
# Compare to our Go:
#
#   type Element struct {
#       Client CDPClient
#       NodeID BackendNodeID
#   }
#
# upstream carries an extra back-reference (browser_session) and a session_id
# for multi-target CDP routing. We elide both: the recorder doesn't need
# back-pressure, and s05 is one notional target.

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


# ============================================================================
# Part 2 — click() — the modifier bitmask + 3-frame mouse-event dispatch
# ============================================================================
# Compare to our Go: element.go::Click + element.go::modifiersToBitmask.
#
# Three things to notice while reading:
#
#  1. modifier_map = {'Alt': 1, 'Control': 2, 'Meta': 4, 'Shift': 8}
#     This dict is byte-identical to our Go switch. CDP itself defines
#     this packing; both implementations just respect the wire spec.
#
#  2. The three dispatchMouseEvent calls (mouseMoved → mousePressed →
#     mouseReleased) are the *protocol* contract — every working click
#     against Chromium emits this triplet. Our Go produces the same
#     three frames in the same order; the RecordingCDPClient captures
#     each one into Frames.
#
#  3. The `params` dict shape (type / x / y / button / clickCount /
#     modifiers) is exactly what we build in our Go pressParams helper.
#     We add a 'backendNodeId' field that upstream doesn't put on the
#     mouse event because upstream already moved to (center_x, center_y)
#     coordinates above — our recorder never resolves geometry, so
#     attaching the node id keeps the recording structurally useful.

async def click(
	self,
	button: 'MouseButton' = 'left',
	click_count: int = 1,
	modifiers: list[ModifierType] | None = None,
) -> None:
	"""Click the element using the advanced watchdog implementation."""
	# ... ~190 lines of geometry + scroll-into-view elided here ...
	# Elided: Page.getLayoutMetrics, DOM.getContentQuads / getBoxModel
	# fallback chain, viewport intersection, scrollIntoViewIfNeeded.
	# s09 introduces layout; s05 just pretends the element is at (0, 0).

	# Calculate modifier bitmask for CDP
	modifier_value = 0
	if modifiers:
		modifier_map = {'Alt': 1, 'Control': 2, 'Meta': 4, 'Shift': 8}
		for mod in modifiers:
			modifier_value |= modifier_map.get(mod, 0)

	# Perform the click using CDP
	# Move mouse to element
	await self._client.send.Input.dispatchMouseEvent(
		params={
			'type': 'mouseMoved',
			'x': center_x,
			'y': center_y,
		},
		session_id=self._session_id,
	)

	# Mouse down — note the wait_for() timeout that upstream wraps
	# around every press/release. s05 has no timeout layer because
	# the recorder is synchronous; s12 will reintroduce per-frame
	# deadlines once the real CDP client lands.
	await asyncio.wait_for(
		self._client.send.Input.dispatchMouseEvent(
			params={
				'type': 'mousePressed',
				'x': center_x,
				'y': center_y,
				'button': button,
				'clickCount': click_count,
				'modifiers': modifier_value,
			},
			session_id=self._session_id,
		),
		timeout=1.0,  # 1 second timeout for mousePressed
	)

	# Mouse up
	await asyncio.wait_for(
		self._client.send.Input.dispatchMouseEvent(
			params={
				'type': 'mouseReleased',
				'x': center_x,
				'y': center_y,
				'button': button,
				'clickCount': click_count,
				'modifiers': modifier_value,
			},
			session_id=self._session_id,
		),
		timeout=3.0,  # 3 second timeout for mouseReleased
	)


# ============================================================================
# Part 3 — focus() — the simplest CDP method in upstream
# ============================================================================
# Compare to our Go: element.go::Focus.
#
# Differences worth noticing:
#
#  - upstream first translates backendNodeId → nodeId via
#    pushNodesByBackendIdsToFrontend (the "_get_node_id" call), then
#    sends DOM.focus by nodeId. Our s05 sends backendNodeId directly
#    to DOM.focus because the recorder doesn't enforce that mapping.
#    In reality both are valid CDP shapes — DOM.focus accepts either a
#    nodeId or an objectId or a backendNodeId, and the upstream choice
#    is mostly historical.
#  - This is the only method in our s05 Element that maps 1:1 in size:
#    upstream is 5 lines, we are 5 lines. Every other method is much
#    larger upstream because it handles geometry and fallbacks we elide.

async def focus(self) -> None:
	"""Focus the element."""
	node_id = await self._get_node_id()
	params: 'FocusParameters' = {'nodeId': node_id}
	await self._client.send.DOM.focus(params, session_id=self._session_id)
