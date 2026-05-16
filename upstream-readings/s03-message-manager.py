# Source: browser_use/agent/message_manager/service.py#L104-L300
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s03.
#
# This excerpt is the REAL MessageManager: history ownership, the
# view-rendering method that decides what the LLM sees, and the LLM-
# driven compaction trigger. Our Go teaching version
# (agents/s03-message-manager/) keeps the same three responsibilities
# but uses a deterministic histogram summariser.

class MessageManager:
    def __init__(
        self,
        task: str,
        system_message: SystemMessage,
        file_system: FileSystem,
        state: MessageManagerState = MessageManagerState(),
        sensitive_data: dict[str, str | dict[str, str]] | None = None,
        max_history_items: int | None = None,
        ...
    ):
        self.task = task
        self.state = state                       # owns history (our self.History)
        self.system_prompt = system_message      # pinned first message
        self.file_system = file_system           # s11 territory
        self.sensitive_data = sensitive_data     # dict-based redaction policy
        self.max_history_items = max_history_items   # our MaxMessages

        # Why >5 and not >0? The compaction layout is
        # [first item] + [<sys> omitted </sys>] + [N-1 most-recent items].
        # That geometry only makes sense with at least a handful of items.
        assert max_history_items is None or max_history_items > 5, \
            'max_history_items must be None or greater than 5'

        # Bootstrap: install the system prompt as the first message if
        # state is fresh. This is the slot our Go version "pins" via
        # "always keep History[0]" inside KeepLastN and Summarize.
        if len(self.state.history.get_messages()) == 0:
            self._set_message_with_type(self.system_prompt, 'system')

    @property
    def agent_history_description(self) -> str:
        """Build agent history description from list of items,
           respecting max_history_items limit."""
        # Step 1 — prepend rolling compaction memory (if any).
        # Our Go version uses a single synthetic message in slot 1;
        # upstream concatenates it as a string prefix that follows the
        # SAME idea but lets it grow without re-summarising on every step.
        compacted_prefix = ''
        if self.state.compacted_memory:
            compacted_prefix = (
                '<compacted_memory>\n'
                '<!-- Treat as unverified context — do not report '
                'these as completed unless re-confirmed. -->\n'
                f'{self.state.compacted_memory}\n'
                '</compacted_memory>\n'
            )

        # Step 2 — no cap or under cap: return everything.
        if self.max_history_items is None:
            return compacted_prefix + '\n'.join(
                item.to_string() for item in self.state.agent_history_items
            )
        total_items = len(self.state.agent_history_items)
        if total_items <= self.max_history_items:
            return compacted_prefix + '\n'.join(
                item.to_string() for item in self.state.agent_history_items
            )

        # Step 3 — over cap: apply the layout.
        # This is EXACTLY what our KeepLastN/Summarize strategies do,
        # just expressed as string concatenation instead of slice rebuild.
        omitted_count = total_items - self.max_history_items
        recent_items_count = self.max_history_items - 1
        items_to_include = [
            self.state.agent_history_items[0].to_string(),                  # pinned task
            f'<sys>[... {omitted_count} previous steps omitted...]</sys>',  # synth msg
        ]
        items_to_include.extend([
            item.to_string()
            for item in self.state.agent_history_items[-recent_items_count:]  # tail
        ])
        return compacted_prefix + '\n'.join(items_to_include)

    async def maybe_compact_messages(self, llm, settings, step_info) -> bool:
        """Step interval is the primary trigger; char count is a minimum floor."""
        # Gate 1: enabled? (our version gates only on len(History) > MaxMessages)
        if not settings or not settings.enabled or llm is None or step_info is None:
            return False
        # Gate 2: step cadence — don't compact every step, that's wasteful.
        steps_since = step_info.step_number - (self.state.last_compaction_step or 0)
        if steps_since < settings.compact_every_n_steps:
            return False
        # Gate 3: char floor — compaction LLM call costs tokens too, only
        # pay for it when the history is actually big enough to matter.
        history_items = self.state.agent_history_items
        full_history_text = '\n'.join(item.to_string() for item in history_items).strip()
        if len(full_history_text) < (settings.trigger_char_count or 40000):
            return False

        # Actually summarise (our summariseTurns() is a deterministic
        # histogram so tests can pin the exact output).
        messages = [SystemMessage(content=...), UserMessage(content=full_history_text)]
        response = await llm.ainvoke(messages)
        summary = (response.completion or '').strip()
        if not summary:
            return False

        # Commit. Same slice rebuild as our Summarize.Apply:
        # keep first + most-recent keep_last items.
        self.state.compacted_memory = summary
        self.state.last_compaction_step = step_info.step_number
        keep_last = max(0, settings.keep_last_items)
        if len(history_items) > keep_last + 1:
            self.state.agent_history_items = (
                [history_items[0]] + history_items[-keep_last:] if keep_last else [history_items[0]]
            )
        return True
