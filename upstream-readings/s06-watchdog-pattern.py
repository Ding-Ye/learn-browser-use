# Source: browser_use/browser/watchdog_base.py#L15-L38, L243-L281
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s06.
#
# Two excerpts from the upstream BaseWatchdog: (1) the class shell and
# its class-level event declarations, (2) the auto-discovery loop in
# attach_to_session() which is what our Go AutoAttach() literally
# translates.
#
# Reading orientation:
#   - Upstream is a Pydantic model; we strip that to a Go marker
#     interface. The behaviour-relevant part is the shape of handler
#     methods, not the BaseModel machinery.
#   - LISTENS_TO / EMITS are class-vars that exist for *humans reading
#     the code at runtime*. They are NOT what the reflection loop uses
#     to register handlers — that's purely method-name driven. We
#     intentionally omit LISTENS_TO from the Go port: Go has no class
#     vars, and the method-name signal is enough for teaching.
#   - The actual circuit-breaker logic (LIFECYCLE_EVENT_NAMES, the
#     unique_handler wrapper, CDP reconnect on handler error) lives in
#     attach_handler_to_session(), L55-L222. We skip that wrapper entirely
#     in the Go port — it's noise relative to the core pattern. See
#     "What this is NOT" in agents/s06-watchdog-pattern/README.md.


# ============================================================================
# Part 1 — Class shell. Pydantic BaseModel with class-var declarations.
# ============================================================================
# In Go we lose Pydantic; ConfigDict shrinks to "nothing"; LISTENS_TO and
# EMITS disappear (no class vars). All that remains is the contract: any
# struct value with OnEventName methods is a watchdog. We encode that in
# Go via an empty interface{} marker — see s06/watchdog.go.

class BaseWatchdog(BaseModel):
    """Base class for all browser watchdogs.

    Watchdogs monitor browser state and emit events based on changes.
    They automatically register event handlers based on method names.

    Handler methods should be named: on_EventTypeName(self, event: EventTypeName)
    """

    model_config = ConfigDict(
        arbitrary_types_allowed=True,  # allow non-serializable objects like EventBus/BrowserSession in fields
        extra='forbid',                # dont allow implicit class/instance state, everything must be a properly typed Field or PrivateAttr
        validate_assignment=False,     # avoid re-triggering  __init__ / validators on values on every assignment
        revalidate_instances='never',  # avoid re-triggering __init__ / validators and erasing private attrs
    )

    # Class variables to statically define the list of events relevant to each watchdog
    # (not enforced, just to make it easier to understand the code and debug watchdogs at runtime)
    LISTENS_TO: ClassVar[list[type[BaseEvent[Any]]]] = []  # Events this watchdog listens to
    EMITS: ClassVar[list[type[BaseEvent[Any]]]] = []  # Events this watchdog emits

    # Core dependencies
    event_bus: EventBus = Field()
    browser_session: BrowserSession = Field()


# ============================================================================
# Part 2 — attach_to_session: the reflection loop our Go AutoAttach mirrors.
# ============================================================================
# The Python pattern is: walk dir(self) for method names starting with
# 'on_', extract the suffix as an event name, look up the matching event
# class, and register. In Go we walk reflect.Type.NumMethod() and match
# names starting with "On" + ending with "Event". The conceptual algorithm
# is identical; only the metaprogramming primitives differ.

    def attach_to_session(self) -> None:
        """Attach watchdog to its browser session and start monitoring.

        This method handles event listener registration. The watchdog is already
        bound to a browser session via self.browser_session from initialization.
        """
        # Register event handlers automatically based on method names
        assert self.browser_session is not None, 'Root CDP client not initialized - browser may not be connected yet'

        from browser_use.browser import events

        event_classes = {}
        for name in dir(events):
            obj = getattr(events, name)
            if inspect.isclass(obj) and issubclass(obj, BaseEvent) and obj is not BaseEvent:
                event_classes[name] = obj

        # Find all handler methods (on_EventName)
        registered_events = set()
        for method_name in dir(self):
            if method_name.startswith('on_') and callable(getattr(self, method_name)):
                # Extract event name from method name (on_EventName -> EventName)
                event_name = method_name[3:]  # Remove 'on_' prefix

                if event_name in event_classes:
                    event_class = event_classes[event_name]

                    # ASSERTION: If LISTENS_TO is defined, enforce it
                    if self.LISTENS_TO:
                        assert event_class in self.LISTENS_TO, (
                            f'[{self.__class__.__name__}] Handler {method_name} listens to {event_name} '
                            f'but {event_name} is not declared in LISTENS_TO: {[e.__name__ for e in self.LISTENS_TO]}'
                        )

                    handler = getattr(self, method_name)

                    # Use the static helper to attach the handler
                    self.attach_handler_to_session(self.browser_session, event_class, handler)
                    registered_events.add(event_class)


# Reading notes:
#  1. `dir(events)` is the Python sibling of Go reflect's NumMethod walk over a
#     type — both produce a name-sorted list. The string-name pivot is the same.
#  2. `method_name[3:]` strips the "on_" prefix. Our Go port uses
#     strings.TrimPrefix(name, "On") to strip "On". CamelCase-vs-snake_case is
#     the only visible delta.
#  3. The `if event_name in event_classes` check guards against typos: a method
#     called on_DownlaodEvent (note typo) is silently skipped. Our Go port hardens
#     that further by also checking the pointer-arg's struct type name matches
#     the method's suffix — catches typos like OnFooEvent(*BarEvent) at attach.
#  4. `attach_handler_to_session()` wraps the bound method in a unique_handler
#     closure that adds circuit-breaker logic. We strip the wrapper in Go; the
#     bus dispatches the bound method directly.
#  5. The LISTENS_TO assertion is a *documentation* enforcement — runtime checks
#     that the human-readable declaration matches the discovered methods. Go's
#     equivalent would be a `// LISTENS_TO: [FooEvent]` comment that nobody
#     reads — we drop it.
#  6. Note what is NOT here: no manual `event_bus.on("DownloadStartedEvent", ...)`
#     calls anywhere in the codebase. The contract is "name your method
#     on_XxxEvent and you're subscribed". That low-ceremony shape is the entire
#     reason the pattern earns its place — see docs/{zh,en}/s06 §3 point 1.
