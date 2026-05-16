# Source: browser_use/agent/service.py#L1023-L1142
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s01.
#
# This is the REAL Agent.step() method from browser-use. Our 60-line Go
# loop.go is the structural skeleton; this 120-line method is what one
# iteration looks like when you add: captcha awareness, screenshot capture,
# DOM snapshot, message compaction, planner injection, telemetry hooks,
# unified error handling, and finalize/cleanup phases.
#
# Reading orientation:
#   - "Phase 0..3" are explicit pedagogical names inside the method body.
#   - Our learn-version compresses Phase 0+1 into 'latestUserText' (no real
#     observation) and Phase 2+3 into the switch StopReason block.
#   - The watchdog plumbing (BrowserSession.wait_if_captcha_solving, etc.)
#     is the entry point of the event-bus pattern we teach in s06+s07.

async def step(self, step_info: AgentStepInfo | None = None) -> None:
    """Execute one step of the task"""
    # Initialize timing first, before any exceptions can occur.
    # Why before the try? So _finalize() can compute step duration even
    # if Phase 0 explodes.
    self.step_start_time = time.time()

    browser_state_summary = None

    try:
        if self.browser_session:
            # ------------------------------------------------------------
            # Phase 0: CAPTCHA awareness
            # ------------------------------------------------------------
            # If a watchdog detected an in-progress captcha, wait for it.
            # Our learn-version (s01) skips this entirely — we have no
            # browser. In a real agent this is critical: without it, the
            # LLM sees the page mid-captcha and either gives up or loops.
            try:
                captcha_wait = await self.browser_session.wait_if_captcha_solving()
                if captcha_wait and captcha_wait.waited:
                    # Reset step timing to exclude the captcha wait from
                    # step duration metrics (otherwise human delay distorts
                    # cost/perf telemetry).
                    self.step_start_time = time.time()
                    duration_s = captcha_wait.duration_ms / 1000
                    outcome = captcha_wait.result  # 'success' | 'failed' | 'timeout'
                    msg = (
                        f'Waited {duration_s:.1f}s for {captcha_wait.vendor} '
                        f'CAPTCHA to be solved. Result: {outcome}.'
                    )
                    self.logger.info(f'🔒 {msg}')
                    # Inject the outcome so the LLM sees what happened.
                    # This is the same "feed result back as tool message"
                    # pattern our learn-version uses, except here the
                    # "result" is a synthetic observation, not an action.
                    captcha_result = ActionResult(long_term_memory=msg)
                    if self.state.last_result:
                        self.state.last_result.append(captcha_result)
                    else:
                        self.state.last_result = [captcha_result]
            except Exception as e:
                # Non-fatal: captcha detection is best-effort.
                self.logger.warning(f'Phase 0 captcha wait failed (non-fatal): {e}')

        # ----------------------------------------------------------------
        # Phase 1: PREPARE CONTEXT
        # ----------------------------------------------------------------
        # _prepare_context() does the heavy lifting that arrives in s07-s09
        # of our learn-version:
        #   - screenshot + DOM snapshot via the browser session
        #   - serialize DOM into LLM-friendly text + selector_map
        #   - inject system prompt, plan_description, sensitive_data masks
        #   - maybe-compact the message history (s03)
        # Returns a BrowserStateSummary that's the "observation" half of
        # observe → think → act.
        browser_state_summary = await self._prepare_context(step_info)

        # Clear previous step state AFTER context preparation (which
        # needed last_result for the "previous action result" prompt)
        # but BEFORE the LLM call, so a timeout during _get_next_action
        # or _execute_actions won't leave stale data.
        self.state.last_model_output = None
        self.state.last_result = None

        # ----------------------------------------------------------------
        # Phase 2: GET MODEL OUTPUT and EXECUTE ACTIONS
        # ----------------------------------------------------------------
        # This is the exact analog of our learn-version's:
        #     resp, _ := a.Provider.Invoke(ctx, msgs)
        #     for _, act := range resp.Actions { tool.Run(...) }
        # Upstream splits it into two awaits so each can have its own
        # timeout (`self.settings.llm_timeout` vs `self.settings.action_timeout`).
        await self._get_next_action(browser_state_summary)
        await self._execute_actions()

        # ----------------------------------------------------------------
        # Phase 3: POST-PROCESSING
        # ----------------------------------------------------------------
        # _post_process() updates the AgentHistoryList with this step's
        # outcome, emits telemetry events, and may invoke the judge LLM
        # to score the agent's decision. Our learn-version doesn't have
        # this — we just append to msgs and loop.
        await self._post_process()

    except Exception as e:
        # ----------------------------------------------------------------
        # All exceptions funneled to one handler.
        # ----------------------------------------------------------------
        # Our learn-version returns errors directly via `return "", err`.
        # Upstream centralizes so it can:
        #   - decide whether to retry (rate limit? recover)
        #   - emit error telemetry once (not per-phase)
        #   - update agent state consistently regardless of which phase failed
        await self._handle_step_error(e)

    finally:
        # ----------------------------------------------------------------
        # Finalize: update history, advance step counter, write summary.
        # ----------------------------------------------------------------
        # Runs regardless of success/failure. The browser_state_summary
        # is passed even when it's None (Phase 1 didn't complete).
        await self._finalize(browser_state_summary)


# Reading map for s01 → next sessions:
#   - Phase 1 ("_prepare_context") body goes to: s07 (browser_session),
#     s08 (dom_serializer), s09 (dom_service), s03 (message_manager).
#   - Phase 2 first half ("_get_next_action") goes to: s02 (llm-provider).
#   - Phase 2 second half ("_execute_actions") goes to: s04 (tool-registry).
#   - Phase 0 captcha goes to: s06 (watchdog-pattern).
#   - Telemetry & cost tracking in Phase 3 goes to: s10 (token-cost).
#   - Sensitive_data redaction parameter goes to: s03 (message-manager redaction).
#
# All 12 of our learn-sessions, composed, equal this 120-line upstream method
# (and the ~4000 lines of helpers it pulls in). s_full retraces the path.
