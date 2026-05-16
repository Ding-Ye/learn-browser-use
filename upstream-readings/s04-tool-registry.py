# Source: browser_use/tools/registry/service.py#L32-L130, L290-L395
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s04.
#
# This is the REAL Registry class — the production Pydantic-flavoured cousin
# of our Go Registry + SchemaFromStruct + Dispatcher trio. Three excerpts
# below: (1) class init + special-param registry, (2) the @action decorator,
# (3) execute_action, the dispatch entry point.
#
# Reading orientation:
#   - The decorator/Pydantic combo is what Python uses *instead of* a Go
#     interface. Same job, different language idioms.
#   - The "special params" (browser_session, file_system, cdp_client, ...)
#     are a Python kwarg-injection trick: tools declare them in their
#     signature, the dispatcher fills them in at call time. Our Go version
#     postpones this to s07+, where BrowserSession arrives.
#   - Timeout policy lives in tools/service.py upstream, not here. We fold
#     it into our Go Dispatcher because s04 is intentionally compact.


# ============================================================================
# Part 1 — Class definition and special-parameter registry
# ============================================================================
# This is where browser-use lists the parameters every tool can opt into.
# Compare with our s04 Tool interface, which currently takes only the
# input JSON. s07's Dispatcher will extend the signature to inject a
# *Session and a *FileSystem; s12 will add page_extraction_llm.

class Registry(Generic[Context]):
    """Service for registering and managing actions"""

    def __init__(self, exclude_actions: list[str] | None = None):
        self.registry = ActionRegistry()
        self.telemetry = ProductTelemetry()
        # Create a new list to avoid mutable default argument issues
        self.exclude_actions = list(exclude_actions) if exclude_actions is not None else []

    def exclude_action(self, action_name: str) -> None:
        """Exclude an action from the registry after initialization."""
        # If the action is already registered, it will be removed from the registry.
        # The action is also added to the exclude_actions list to prevent re-registration.
        if action_name not in self.exclude_actions:
            self.exclude_actions.append(action_name)
        if action_name in self.registry.actions:
            del self.registry.actions[action_name]
            logger.debug(f'Excluded action "{action_name}" from registry')

    def _get_special_param_types(self) -> dict[str, type | UnionType | None]:
        """Get the expected types for special parameters from SpecialActionParameters"""
        # Manually define the expected types to avoid issues with Optional handling.
        # We should try to reduce this list to 0 if possible, give as few standardized
        # objects to all the actions but each driver should decide what is relevant to
        # expose the action methods (CDP client, 2fa code getters, sensitive_data
        # wrappers, other context, etc.).
        return {
            'context': None,                     # Context is a TypeVar, no validation
            'browser_session': BrowserSession,   # appears in s07
            'page_url': str,
            'cdp_client': None,                  # cdp-use type, not imported here
            'page_extraction_llm': BaseChatModel,# appears in s12
            'available_file_paths': list,
            'has_sensitive_data': bool,
            'file_system': FileSystem,           # appears in s11
            'extraction_schema': None,           # dict | None, skip type validation
        }


# ============================================================================
# Part 2 — The @action decorator (registration)
# ============================================================================
# This is the rough equivalent of `reg.MustRegister(SearchTool{})` in our Go
# code, except registration triggers a Pydantic model-generation step.
# `_normalize_action_function_signature` (omitted here for brevity — see
# upstream lines 74-271) is the Python answer to our SchemaFromStruct: it
# reads the function's parameter annotations and synthesizes a pydantic
# BaseModel that captures the "non-special" parameters. The resulting model's
# `.model_json_schema()` is what gets fed to the LLM.

    def action(
        self,
        description: str,
        param_model: type[BaseModel] | None = None,
        domains: list[str] | None = None,
        allowed_domains: list[str] | None = None,
        terminates_sequence: bool = False,
    ):
        """Decorator for registering actions"""
        # Handle aliases: domains and allowed_domains are the same parameter.
        # Our Go Registry doesn't have domain restrictions yet — that's a
        # post-s12 extension for the security-watchdog flow.
        if allowed_domains is not None and domains is not None:
            raise ValueError(
                "Cannot specify both 'domains' and 'allowed_domains' — they are aliases"
            )
        final_domains = allowed_domains if allowed_domains is not None else domains

        def decorator(func: Callable):
            # Skip registration if action is in exclude_actions
            if func.__name__ in self.exclude_actions:
                return func

            # Normalize the function signature — this is the BIG one. It walks
            # the function's parameters, separates "action params" from
            # "special params", and synthesises a pydantic model_class for
            # the action params. Our Go SchemaFromStruct does the same in
            # a static way: the struct already encodes the parameters, we
            # only need to walk it at runtime.
            normalized_func, actual_param_model = self._normalize_action_function_signature(
                func, description, param_model
            )

            # Store as a RegisteredAction (the upstream view-model for a row
            # in the registry). The fields map roughly onto our ToolSchema:
            #   name        ↔ ToolSchema.Name
            #   description ↔ ToolSchema.Description
            #   param_model ↔ ToolSchema.Parameters (as JSON Schema)
            #
            # terminates_sequence is the one field we don't model in s04 —
            # it's used by the dispatcher upstream to stop a multi-action
            # batch (think `done` action). We defer to s12 when the agent
            # loop starts producing multi-action turns.
            action = RegisteredAction(
                name=func.__name__,
                description=description,
                function=normalized_func,
                param_model=actual_param_model,
                domains=final_domains,
                terminates_sequence=terminates_sequence,
            )
            self.registry.actions[func.__name__] = action

            # Return the normalized function so it can be called with kwargs.
            # This is a Python-only convenience — `@reg.action` users still
            # get a callable handle after decoration.
            return normalized_func

        return decorator


# ============================================================================
# Part 3 — execute_action (dispatch)
# ============================================================================
# This is the 1:1 analog of our Go `Dispatcher.Act`. Note all the special
# params upstream injects: browser_session, file_system, etc. — we keep
# the analog Dispatcher empty of those in s04 to focus on the registry
# itself, and add them back as later sessions introduce the dependencies.

    async def execute_action(
        self,
        action_name: str,
        params: dict,
        browser_session: BrowserSession | None = None,
        page_extraction_llm: BaseChatModel | None = None,
        file_system: FileSystem | None = None,
        sensitive_data: dict[str, str | dict[str, str]] | None = None,
        available_file_paths: list[str] | None = None,
        extraction_schema: dict | None = None,
    ) -> Any:
        """Execute a registered action with simplified parameter handling"""
        # Step 1: lookup. This is the equivalent of our Registry.Lookup call.
        # Upstream raises ValueError; our Go version returns the typed
        # (Tool, bool) pair and lets the Dispatcher fmt the message.
        if action_name not in self.registry.actions:
            raise ValueError(f'Action {action_name} not found')

        action = self.registry.actions[action_name]
        try:
            # Step 2: validate input via the pydantic model that was generated
            # at registration time. This is structurally identical to our
            # json.Unmarshal(input, &args) in each Tool.Run — except pydantic
            # also coerces types (str→int, etc.) which encoding/json does not.
            try:
                validated_params = action.param_model(**params)
            except Exception as e:
                raise ValueError(
                    f'Invalid parameters {params} for action {action_name}: {type(e)}: {e}'
                ) from e

            # Step 3: sensitive-data placeholder replacement. Tools that take
            # secrets emit '<secret>github_token</secret>' tokens; the
            # registry rewrites them to actual values right before dispatch.
            # Skipped in s04; lands in a post-s12 security session. (Body
            # elided — see upstream service.py#L353-L364 for the URL-aware
            # `_replace_sensitive_data` call.)
            if sensitive_data:
                validated_params = self._replace_sensitive_data(
                    validated_params, sensitive_data, current_url=None
                )

            # Step 4: build the special_context kwargs. This is the kwarg
            # injection trick — tools that ask for `browser_session` in their
            # signature will receive it; tools that don't, won't.
            special_context = {
                'browser_session': browser_session,
                'page_extraction_llm': page_extraction_llm,
                'available_file_paths': available_file_paths,
                'has_sensitive_data': action_name == 'input' and bool(sensitive_data),
                'file_system': file_system,
                'extraction_schema': extraction_schema,
            }

            # Step 5: actually call the normalized function. The wrapper
            # produced by `_normalize_action_function_signature` knows how
            # to route `params` and the special context to the user's
            # original positional signature. Our Go Tool.Run is simpler:
            # one ctx, one json.RawMessage.
            return await action.function(params=validated_params, **special_context)

        # Step 6: error normalization. Upstream re-classifies several common
        # errors as RuntimeError with a stable message; our Dispatcher does
        # the analogous translation for context.DeadlineExceeded → "timed out".
        except ValueError as e:
            if 'requires browser_session but none provided' in str(e):
                raise RuntimeError(str(e)) from e
            raise RuntimeError(f'Error executing action {action_name}: {str(e)}') from e
        except TimeoutError as e:
            raise RuntimeError(f'Error executing action {action_name} due to timeout.') from e
        except Exception as e:
            raise RuntimeError(f'Error executing action {action_name}: {str(e)}') from e
