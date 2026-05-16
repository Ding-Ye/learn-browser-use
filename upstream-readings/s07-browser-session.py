# Source: browser_use/browser/session.py#L101-L735, L1580-L1700 (excerpts)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s07.
#
# Three excerpts below:
#   (1) BrowserSession class header — field declarations show the
#       *shape* a session ships with: an event_bus, a swarm of private
#       watchdog slots, and reconnection state.
#   (2) start() / stop() — the lifecycle entry points that our Go
#       Session.Start / Session.Stop mirror. Notice the event-driven
#       layering: start() merely dispatches BrowserStartEvent and lets
#       on_BrowserStartEvent do the real attach work.
#   (3) attach_all_watchdogs() — what s07's AutoAttach loop does once
#       per registered watchdog. Upstream instantiates 12+ watchdogs;
#       we ship one (LoggingWatchdog) to make the pattern legible.
#
# Reading orientation:
#   - The bus → handler indirection (start dispatches an event, the
#     handler does the work) decouples lifecycle from launch policy:
#     the same start() works for local Chromium, cloud browsers, and
#     external CDP URLs because each path is a different handler.
#   - "Idempotency" lives in on_BrowserStartEvent (see the comment
#     "This method is idempotent"). Our Go Session.Start checks
#     `s.Started` for the same effect.
#   - The 12 watchdogs in attach_all_watchdogs are NOT the lesson of
#     s07 — they're the surface area we'd build up *after* s07. The
#     lesson is the attach loop itself.


# ============================================================================
# Part 1 — Class header, fields, lifecycle state
# ============================================================================
# Highlights to look at:
#   - event_bus: EventBus = Field(default_factory=EventBus)
#       — the bus is owned by the session, not the agent. This is what
#         our s07 BrowserSession.Bus mirrors.
#   - private _xxx_watchdog slots — each populated by
#     attach_all_watchdogs(). We keep a single Watchdogs []Watchdog
#     field; same idea, less ceremony.
#   - RECONNECT_WAIT_TIMEOUT / _reconnect_event / _reconnect_task
#       — entire mini-state-machine for WebSocket reconnection.
#         Deliberately omitted in s07.

class BrowserSession(BaseModel):
    """Event-driven browser session with backwards compatibility.

    This class provides a 2-layer architecture:
    - High-level event handling for agents/tools
    - Direct CDP/Playwright calls for browser operations
    """

    model_config = ConfigDict(
        arbitrary_types_allowed=True,
        validate_assignment=True,
        extra='forbid',
        revalidate_instances='never',
    )

    # Main shared event bus for all browser session + all watchdogs
    event_bus: EventBus = Field(default_factory=EventBus)

    # Mutable public state - which target has agent focus
    agent_focus_target_id: TargetID | None = None

    # Mutable private state shared between watchdogs
    _cdp_client_root: CDPClient | None = PrivateAttr(default=None)
    _connection_lock: Any = PrivateAttr(default=None)

    # Watchdogs — one slot per concern. Our Go version uses a slice;
    # upstream uses named fields because each watchdog is a Pydantic
    # model that needs typed access elsewhere in the file.
    _crash_watchdog: Any | None = PrivateAttr(default=None)
    _downloads_watchdog: Any | None = PrivateAttr(default=None)
    _aboutblank_watchdog: Any | None = PrivateAttr(default=None)
    _security_watchdog: Any | None = PrivateAttr(default=None)
    _storage_state_watchdog: Any | None = PrivateAttr(default=None)
    _local_browser_watchdog: Any | None = PrivateAttr(default=None)
    _default_action_watchdog: Any | None = PrivateAttr(default=None)
    _dom_watchdog: Any | None = PrivateAttr(default=None)
    _screenshot_watchdog: Any | None = PrivateAttr(default=None)
    _permissions_watchdog: Any | None = PrivateAttr(default=None)
    _recording_watchdog: Any | None = PrivateAttr(default=None)
    _captcha_watchdog: Any | None = PrivateAttr(default=None)
    _watchdogs_attached: bool = PrivateAttr(default=False)

    # WebSocket reconnection state — omitted in s07.
    RECONNECT_WAIT_TIMEOUT: float = 54.0
    _reconnecting: bool = PrivateAttr(default=False)
    _reconnect_event: asyncio.Event = PrivateAttr(default_factory=asyncio.Event)
    _reconnect_task: asyncio.Task | None = PrivateAttr(default=None)
    _intentional_stop: bool = PrivateAttr(default=False)


# ============================================================================
# Part 2 — start() / stop()
# ============================================================================
# This is the entry-point pair our Go BrowserSession.Start / Stop
# mirror. The pattern is event-driven: start() dispatches the
# BrowserStartEvent, and a handler (registered in model_post_init)
# does the actual attach work. The same indirection lets stop() be
# implemented by every watchdog independently.

    @observe_debug(ignore_input=True, ignore_output=True, name='browser_session_start')
    async def start(self) -> None:
        """Start the browser session."""
        # ↓ Our Go equivalent: s.Client.Send("Target.attachToTarget", ...)
        #   + AutoAttach(...) + Bus.Emit(SessionStartedEvent).
        #   Upstream collapses all three into one event dispatch.
        start_event = self.event_bus.dispatch(BrowserStartEvent())
        await start_event
        # Ensure any exceptions from the event handler are propagated.
        await start_event.event_result(raise_if_any=True, raise_if_none=False)

    async def stop(self) -> None:
        """Stop the browser session without killing the browser process.

        This clears event buses and cached state but keeps the browser alive.
        Useful when you want to clean up resources but plan to reconnect later.
        """
        self._intentional_stop = True
        self.logger.debug('⏸️  stop() called - stopping browser gracefully and resetting state')

        # First save storage state while CDP is still connected.
        # We don't have a storage layer in s07; the watchdog event we
        # *do* emit (SessionStoppedEvent) is the s07 equivalent.
        save_event = self.event_bus.dispatch(SaveStorageStateEvent())
        await save_event

        # Now dispatch BrowserStopEvent to notify watchdogs.
        # ↓ Our Go equivalent: s.Bus.Emit(SessionStoppedEvent{...}).
        await self.event_bus.dispatch(BrowserStopEvent(force=False))

        # Stop the event bus — our Go equivalent: s.Bus.Clear().
        await self.event_bus.stop(clear=True, timeout=5)
        # Reset all state — our Go equivalent: s.Started = false.
        await self.reset()
        # Create fresh event bus.
        self.event_bus = EventBus()

    async def on_BrowserStartEvent(self, event: BrowserStartEvent) -> dict[str, str]:
        """Handle browser start request.

        Note: This method is idempotent - calling start() multiple times is safe.
        - If already connected, it skips reconnection
        - If you need to reset state, call stop() or kill() first
        """
        # ↓ The "attach watchdogs FIRST" line is what our Go Start()
        #   does immediately after the stub Target.attachToTarget call:
        #   loop over s.Watchdogs and AutoAttach each one.
        await self.attach_all_watchdogs()

        # The branch below picks local vs cloud vs external CDP URL.
        # s07 collapses to "stub CDP" — there's nothing to launch.
        try:
            if not self.cdp_url:
                if self.browser_profile.use_cloud or self.browser_profile.cloud_browser_params is not None:
                    cloud_browser_response = await self._cloud_browser_client.create_browser(
                        self.browser_profile.cloud_browser_params or CreateBrowserRequest()
                    )
                    self.browser_profile.cdp_url = cloud_browser_response.cdpUrl
                elif self.is_local:
                    launch_event = self.event_bus.dispatch(BrowserLaunchEvent())
                    await launch_event
                    launch_result: BrowserLaunchResult = cast(
                        BrowserLaunchResult, await launch_event.event_result(raise_if_none=True, raise_if_any=True)
                    )
                    self.browser_profile.cdp_url = launch_result.cdp_url
        except CloudBrowserError:
            raise
        ...


# ============================================================================
# Part 3 — attach_all_watchdogs()
# ============================================================================
# This is the *loop body* our Go Session.Start runs once per element
# of s.Watchdogs. Each entry: instantiate the watchdog with the shared
# event_bus + browser_session, then call attach_to_session() (which is
# where the reflection-based handler registration happens — see
# upstream's browser_use/browser/watchdog_base.py for the
# attach_handler_to_session method that's the closest analog to our
# Go AutoAttach).

    async def attach_all_watchdogs(self) -> None:
        # (Late imports omitted for brevity — the real method imports
        # all 12 watchdog classes at the top to avoid circular import
        # issues with the rest of browser_use.)

        # Initialize DownloadsWatchdog — pattern repeats for each:
        #   1. construct(event_bus=..., browser_session=self)
        #   2. attach_to_session() to register all on_XxxEvent handlers
        DownloadsWatchdog.model_rebuild()
        self._downloads_watchdog = DownloadsWatchdog(event_bus=self.event_bus, browser_session=self)
        self._downloads_watchdog.attach_to_session()

        # Initialize LocalBrowserWatchdog — same pattern.
        LocalBrowserWatchdog.model_rebuild()
        self._local_browser_watchdog = LocalBrowserWatchdog(event_bus=self.event_bus, browser_session=self)
        self._local_browser_watchdog.attach_to_session()

        # ... and 10 more watchdogs, all attached the same way ...

        # The repetition is what makes our s07 design choice obvious:
        # in Go we hide the 12-line repeated stanza behind one for-loop
        # in Session.Start. Upstream cannot do that because each
        # watchdog has its own typed slot on the BrowserSession.

        self._watchdogs_attached = True
