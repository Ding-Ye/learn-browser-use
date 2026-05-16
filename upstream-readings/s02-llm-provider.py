# Source: browser_use/llm/openai/chat.py#L1-L222 (annotated excerpt)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s02.
#
# This is the REAL OpenAI chat client used inside browser-use. Our Go
# OpenAIProvider in agents/s02-llm-provider/openai_provider.go is the
# structural skeleton; this Python class is the same idea plus: async
# (httpx), structured JSON-schema output, reasoning-effort flag for
# o-series models, and defensive error envelope mapping.

from dataclasses import dataclass, field
from typing import Any, TypeVar
import httpx
from openai import APIConnectionError, APIStatusError, AsyncOpenAI, RateLimitError

# Our Go version's "Message" type maps to BaseMessage; serializer is what
# we re-implement inline in convertMessage().
from browser_use.llm.base import BaseChatModel
from browser_use.llm.exceptions import ModelProviderError, ModelRateLimitError
from browser_use.llm.openai.serializer import OpenAIMessageSerializer
from browser_use.llm.views import ChatInvokeCompletion, ChatInvokeUsage


@dataclass
class ChatOpenAI(BaseChatModel):
    """A wrapper around AsyncOpenAI that implements the BaseLLM protocol."""

    # NOTE: in Go we collapse Model + URL + APIKey into the OpenAIProvider
    # struct. The full Python knobs (frequency_penalty, top_p, seed, etc.)
    # we elide — teaching value-per-line drops sharply past 2 knobs.
    model: str
    temperature: float | None = 0.2
    frequency_penalty: float | None = 0.3  # 4.1-mini infinite-\t workaround
    api_key: str | None = None
    base_url: str | None = None
    timeout: float | None = None
    max_retries: int = 5  # SDK does the retries; we hand-roll it in Go.

    # The reasoning-models special-case: o-series models reject
    # temperature/frequency_penalty and instead take reasoning_effort.
    # We do NOT implement this in Go — out of scope.
    reasoning_models: list[str] | None = field(default_factory=lambda: ['o3', 'o3-mini', 'gpt-5'])

    @property
    def provider(self) -> str:
        return 'openai'

    def get_client(self) -> AsyncOpenAI:
        return AsyncOpenAI(api_key=self.api_key, base_url=self.base_url, timeout=self.timeout, max_retries=self.max_retries)

    async def ainvoke(self, messages, output_format=None, **kwargs):
        # ──────────────────────────────────────────────────────────────
        # Phase 1: serialize messages.
        # Maps to our convertMessage(). Both must split assistant turns
        # with tool_use blocks into one wire message with tool_calls,
        # AND each tool_result into a separate role:"tool" message with
        # matching tool_call_id.
        openai_messages = OpenAIMessageSerializer.serialize_messages(messages)

        try:
            # Phase 2: build model_params from set fields. Our Go version
            # has just `Temperature *float64`; Python handles ~7 knobs.
            # Principle: only include set values, the API rejects nulls.
            model_params: dict[str, Any] = {}
            if self.temperature is not None: model_params['temperature'] = self.temperature
            if self.frequency_penalty is not None: model_params['frequency_penalty'] = self.frequency_penalty

            # Reasoning-models branch: brittle name-matching that mutates
            # the params dict. Real-world abstraction always grows these.
            if self.reasoning_models and any(m in str(self.model).lower() for m in self.reasoning_models):
                model_params['reasoning_effort'] = 'low'
                model_params.pop('temperature', None)
                model_params.pop('frequency_penalty', None)

            # Phase 3: dispatch. The output_format=None branch matches our
            # Go Invoke() exactly (free-form text + optional tool_calls).
            if output_format is None:
                response = await self.get_client().chat.completions.create(
                    model=self.model, messages=openai_messages, **model_params,
                )
                choice = response.choices[0] if response.choices else None
                if choice is None:
                    # Defensive: third-party proxies sometimes return empty choices.
                    raise ModelProviderError(message='no choices', status_code=502, model=self.name)
                return ChatInvokeCompletion(
                    completion=choice.message.content or '',
                    usage=self._get_usage(response),
                    stop_reason=choice.finish_reason,
                )
            # ... [structured-output branch elided — we defer to s04]

        # Phase 4: error mapping. Each SDK exception class becomes a typed
        # ModelProviderError so the agent loop sees a uniform error shape.
        # Our Go version is rougher — we return the http body verbatim.
        except RateLimitError as e:
            raise ModelRateLimitError(message=e.message, model=self.name) from e
        except APIConnectionError as e:
            raise ModelProviderError(message=str(e), model=self.name) from e
        except APIStatusError as e:
            raise ModelProviderError(message=e.message, status_code=e.status_code, model=self.name) from e


# ─── Reading notes ─────────────────────────────────────────────────────
#
# 1. **Protocol, not base class.** `BaseChatModel` (browser_use/llm/base.py
#    #L17-L60) is a `Protocol` — duck-typed, like a Go interface. Going
#    from Python Protocol to Go interface, we get the same shape for free.
#
# 2. **The serializer is provider-specific.** openai/serializer.py vs
#    anthropic/serializer.py. In Go, our convertMessage() is the OpenAI
#    serializer; Phase G adds a parallel anthropicSerializer.
#
# 3. **Retry lives in the SDK** (`max_retries=5`). We don't use the SDK,
#    so we hand-roll retry-on-429 in Invoke. Simpler to read; harder to
#    get right — we don't yet honour `Retry-After`; browser-use does.
#
# 4. **Reasoning-models branch = protocol leakage in disguise.** OpenAI
#    added `reasoning_effort` mutually-exclusive with older sampling knobs.
#    The `if any(m in self.model for m in reasoning_models)` check is
#    brittle name-matching. Any real provider abstraction accretes N such
#    branches over time.
#
# 5. **Structured output uses `response_format=ResponseFormatJSONSchema`**
#    in the elided `else` branch. We defer to s04 where we generate JSON
#    Schema from Go structs via reflection.
#
# 6. **`@overload` on ainvoke gives type-checkers a precise signature.**
#    Go has no equivalent — we'd need generics or a separate Invoke-
#    Structured method. Phase G / s04 makes that call.
