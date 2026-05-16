# Source: browser_use/agent/service.py#L1023-L1200 (excerpts)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s12.
#
# Three excerpts from the canonical Agent.step orchestration:
#   (1) step() — the 7-phase per-step entry point
#   (2) _prepare_context (head only) — observation + message construction
#   (3) _get_next_action — LLM call with timeout (our invokeWithFallback analog)
#
# Orientation: upstream uses asyncio + try/except for cancellation;
# our Go agent uses context.WithTimeout + error returns. Same shape,
# different idioms. The 7-phase split (captcha → prepare → invoke →
# execute → post) maps onto our 7-phase split (planner → observe →
# invoke → ledger → append → switch → dispatch). Upstream has six
# nudges in prepare_context we collapse to one (planner). Fallback
# Provider is NOT in upstream — production deployments wrap with
# retry middleware. We promote it to a struct field so s12 teaches
# the policy as code.


# ── (1) step() — the 7-phase per-step entry point ──

async def step(self, step_info: AgentStepInfo | None = None) -> None:
    """Execute one step of the task"""
    # Initialize timing first, before any exceptions can occur
    self.step_start_time = time.time()
    browser_state_summary = None

    try:
        if self.browser_session:
            try:
                captcha_wait = await self.browser_session.wait_if_captcha_solving()
                if captcha_wait and captcha_wait.waited:
                    self.step_start_time = time.time()
                    msg = f'Waited {captcha_wait.duration_ms/1000:.1f}s for CAPTCHA. Result: {captcha_wait.result}.'
                    self.logger.info(f'🔒 {msg}')
                    captcha_result = ActionResult(long_term_memory=msg)
                    if self.state.last_result:
                        self.state.last_result.append(captcha_result)
                    else:
                        self.state.last_result = [captcha_result]
            except Exception as e:
                self.logger.warning(f'Phase 0 captcha wait failed (non-fatal): {e}')

        # Phase 1: Prepare context and timing
        browser_state_summary = await self._prepare_context(step_info)

        # Clear previous step state after context preparation
        self.state.last_model_output = None
        self.state.last_result = None

        # Phase 2: Get model output and execute actions
        await self._get_next_action(browser_state_summary)
        await self._execute_actions()

        # Phase 3: Post-processing
        await self._post_process()

    except Exception as e:
        await self._handle_step_error(e)

    finally:
        await self._finalize(browser_state_summary)


# ── (2) _prepare_context — observation + message construction ──

async def _prepare_context(self, step_info: AgentStepInfo | None = None) -> BrowserStateSummary:
    """Prepare the context for the step: browser state, action models, page actions"""
    assert self.browser_session is not None, 'BrowserSession is not set up'

    # Always take screenshots (even if use_vision=False, for cloud sync)
    browser_state_summary = await self.browser_session.get_browser_state_summary(
        include_screenshot=True,
        include_recent_events=self.include_recent_events,
    )
    await self._check_and_update_downloads(f'Step {self.state.n_steps}: after getting browser state')
    self._log_step_context(browser_state_summary)
    await self._check_stop_or_pause()

    # Page-specific filtered actions go into the state message
    await self._update_action_models_for_page(browser_state_summary.url)
    page_filtered_actions = self.tools.registry.get_prompt_description(browser_state_summary.url)
    plan_description = self._render_plan_description()

    # COMPACTION CALL SITE — analogous to our MessageManager.Get() lazy compaction
    await self._maybe_compact_messages(step_info)

    self._message_manager.create_state_messages(
        browser_state_summary=browser_state_summary,
        page_filtered_actions=page_filtered_actions or None,
        plan_description=plan_description,
        # ... 8 more arguments elided ...
        skip_state_update=True,
    )

    # SIX nudge injections — we collapse to one (planner) for teaching.
    await self._inject_budget_warning(step_info)
    self._inject_replan_nudge()
    self._inject_exploration_nudge()
    self._update_loop_detector_page_state(browser_state_summary)
    self._inject_loop_detection_nudge()
    await self._force_done_after_last_step(step_info)
    return browser_state_summary


# ── (3) _get_next_action — LLM call with timeout (our invokeWithFallback analog) ──

@observe_debug(ignore_input=True, name='get_next_action')
async def _get_next_action(self, browser_state_summary: BrowserStateSummary) -> None:
    """Execute LLM interaction with retry logic and handle callbacks"""
    input_messages = self._message_manager.get_messages()

    try:
        model_output = await asyncio.wait_for(
            self._get_model_output_with_retry(input_messages),
            timeout=self.settings.llm_timeout,
        )
    except TimeoutError:
        # NOTE: upstream raises here. No Fallback Provider — production
        # wraps _get_next_action with its own retry middleware.
        raise TimeoutError(
            f'LLM call timed out after {self.settings.llm_timeout} seconds.'
        )

    self.state.last_model_output = model_output
    await self._check_stop_or_pause()
    await self._handle_post_llm_processing(browser_state_summary, input_messages)
    await self._check_stop_or_pause()


# Notes on the Go translation:
#
# 1. step() is 220 lines wrapping every Phase in try/except. Our
#    agent.Run returns errors via `return "", err`. Same control
#    flow, shorter — Go doesn't need per-phase try-wrappers.
#
# 2. _prepare_context builds the user message with screenshot,
#    serialized DOM, plus six nudge injections. We compress to one
#    `Messages.Add(user, browser_state)` + the planner injection.
#
# 3. _maybe_compact_messages is the compaction call site. Upstream
#    lazily compacts inside prepare_context; our MessageManager.Get()
#    does the same lazy timing on the read path.
#
# 4. asyncio.wait_for cancels the underlying coroutine on timeout.
#    Our Go version uses context.WithTimeout + errors.Is check. Same
#    semantics: bound per-call cost so a hung LLM doesn't wedge the loop.
#
# 5. No fallback Provider upstream. Production retry middleware wraps
#    _get_next_action externally. Our s12 promotes Fallback to a
#    struct field — the teaching point is "fallback is a concept,
#    not a magic plugin".
#
# 6. _handle_step_error is the single error-handling site upstream,
#    branching on exception type. Our Go version returns errors
#    directly; the caller decides recovery. Both shapes are
#    defensible — Python centralizes recovery, Go centralizes dispatch.
#
# 7. The fact that 11 chapters of Go can mirror Python's step() one-
#    to-one is the strongest evidence that our curriculum's
#    decomposition matches the actual code structure of the upstream
#    project.
