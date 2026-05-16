---
title: "s02 · LLM Provider abstraction"
chapter: 2
slug: s02-llm-provider
est_read_min: 14
---

# s02 · LLM Provider abstraction

> Teaching focus: s01's `FakeProvider` fakes an LLM with `if`/`else` and only works on demos. This session turns "the LLM" from a concept into an interface and ships two implementations — a real OpenAI client via plain `net/http`, and a deterministic `MockProvider` for tests. The point is: **once the interface shape is locked in, none of the remaining 10 sessions will touch the loop again**.

---

## Problem / 问题

s01 got `loop(observe → think → act)` running, but the "think" step was a hard-coded `FakeProvider`: it greps the task string for "search"/"navigate" and picks an action by keyword. Great for teaching the skeleton; useless once the task is even slightly non-trivial ("find the GitHub repo with the fastest star growth in the past 7 days") — it can't compose queries, understand context, or notice "I already searched 3 times and got nothing".

Before swapping it for a real LLM, three questions need answers:

1. **What's the interface shape?** OpenAI and Anthropic have different APIs; later we'll add Google, Groq, Ollama, etc. We can't edit the agent loop every time a new provider joins. There has to be a unified shape across providers.
2. **How do we test?** Real LLM calls cost money, need an API key, and are non-deterministic (same prompt → different answers). CI can't depend on a real API.
3. **How do we handle 429s and network blips?** Real APIs throttle occasionally; a single bare `client.Do` that errors out is hostile to production callers.

s02's goal: define the `Provider` interface, ship two implementations (real OpenAI + Mock for tests), and validate everything with 6 unit tests that use `httptest` to stub OpenAI's behaviour.

## Solution / 解决方案

Split s01's `FakeProvider` into an interface + implementations:

1. **`Provider` interface** (`provider.go`): one method, `Invoke(ctx, msgs []Message, tools []ToolSchema) (Response, error)`. Every provider implements this shape. `ToolSchema` keeps its JSON Schema as `json.RawMessage` so callers can hand-write strict schemas without a reflection round-trip.
2. **`OpenAIProvider`** (`openai_provider.go`): straight `net/http` + `encoding/json` POST to `/chat/completions`. Turns the response's `tool_calls` array into `[]ActionCall`, maps `finish_reason` back to our `StopReason` (`stop` → `end_turn`, `tool_calls` → `tool_use`, `length` → `length`). **Zero SDK dependency** — OpenAI's protocol has a small handful of fields; rolling it by hand puts every byte on the wire in plain sight.
3. **`MockProvider`** (`mock_provider.go`): an internal `Queue []Response`; each call pops one; queue exhaustion returns `error` (not nil, not a panic — surfaces "test forgot to queue enough responses" instantly). `Reset()` lets table-driven tests reuse one instance.

Key decisions:

1. **`ToolSchema.Parameters` is `json.RawMessage`, not `any`/`map[string]any`** — lets callers hand-write strict JSON Schemas without bouncing through Go reflection. s04 will reverse the direction (struct tags → JSON Schema via reflection).
2. **`Response` extends s01's shape with three new fields**: `InputTokens`, `OutputTokens`, `Model` (the server-resolved version, e.g. client sent `gpt-4o-mini`, server returns `gpt-4o-mini-2024-07-18`). FakeProvider can't produce these — only a real API responds with them. s10's token cost tracking reuses these fields verbatim.
3. **429 retry uses `time.NewTimer` + `<-ctx.Done()` in a `select`**, not `time.Sleep(1*time.Second)`. When the caller cancels ctx, the sleep exits immediately instead of waiting the full second — this is what keeps tests under 100ms.
4. **Wire-format types are unexported**: `openAIRequest`, `openAIMessage`, `openAIToolCall`, etc. They're an implementation detail of one provider and must not leak into the `Provider` interface. When Anthropic joins, it'll have its own parallel unexported set.

## How It Works / 工作原理

```
       Agent.Run                           OpenAIProvider.Invoke
       (s01 already)                        (this session)
        │                                  │
        │ Provider.Invoke(ctx, msgs, tools)│
        ├──────────────────────────────────┤
        │                                  │ 1. buildRequestBody
        │                                  │    convertMessage:
        │                                  │    - system/user  → 1 wire msg
        │                                  │    - assistant    → 1 msg + tool_calls[]
        │                                  │    - tool         → N msgs, one tool_call_id each
        │                                  │
        │                                  │ 2. http.NewRequestWithContext(POST)
        │                                  │    Authorization: Bearer $KEY
        │                                  │    Content-Type:  application/json
        │                                  │    Body: {model, messages, tools, temperature}
        │                                  │
        │                                  │ 3. client.Do(req)
        │                                  │    if 429 && retries left:
        │                                  │      sleepCtx(retryDelay); continue
        │                                  │    if other 4xx/5xx:
        │                                  │      return err with body
        │                                  │
        │                                  │ 4. parseResponse
        │                                  │    choices[0].message.content    → Response.Text
        │                                  │    choices[0].message.tool_calls → []ActionCall
        │                                  │    finish_reason → StopReason
        │                                  │    usage → InputTokens / OutputTokens
        │                                  │
        │ Response{Text, Actions, StopReason, InputTokens, OutputTokens, Model}
        │◄─────────────────────────────────┤
```

Core 50 lines (excerpted from `agents/s02-llm-provider/openai_provider.go`):

```go
func (p *OpenAIProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
    url := p.BaseURL
    if url == "" { url = defaultBaseURL }
    url = url + "/chat/completions"

    body, err := p.buildRequestBody(msgs, tools)
    if err != nil { return Response{}, fmt.Errorf("build request: %w", err) }

    client := p.HTTPClient
    if client == nil { client = &http.Client{Timeout: 60 * time.Second} }

    maxRetries := p.MaxRetries
    if maxRetries == 0 { maxRetries = 1 }
    retryDelay := p.RetryDelay
    if retryDelay == 0 { retryDelay = 1 * time.Second }

    var lastErr error
    for attempt := 0; attempt <= maxRetries; attempt++ {
        respBody, status, err := p.doRequest(ctx, client, url, body)
        if err != nil {
            lastErr = err
            if attempt < maxRetries {
                if sleepErr := sleepCtx(ctx, retryDelay); sleepErr != nil {
                    return Response{}, sleepErr
                }
                continue
            }
            return Response{}, lastErr
        }
        if status == http.StatusTooManyRequests && attempt < maxRetries {
            if sleepErr := sleepCtx(ctx, retryDelay); sleepErr != nil {
                return Response{}, sleepErr
            }
            continue
        }
        if status < 200 || status >= 300 {
            return Response{}, fmt.Errorf("openai http %d: %s", status, string(respBody))
        }
        return parseResponse(respBody)
    }
    return Response{}, lastErr
}
```

**Four non-obvious points**:

1. **An assistant message with `tool_use` blocks must be sent with `tool_calls[]` containing stable `id`s**: OpenAI uses `tool_call_id` to pair subsequent `role:"tool"` result messages with the prior tool_calls. We use `call_<idx>` numbering (see `convertMessage`) — simplistic but correct.
2. **`Temperature *float64`, not `float64`**: zero (greedy sampling) is a legal value, so we can't use Go's zero value to mean "unset". A pointer is the cleanest Go encoding of an optional numeric.
3. **`sleepCtx` instead of `time.Sleep`**: if ctx is cancelled during a 429 backoff, the sleep must exit immediately. Tests crank `RetryDelay` to 10ms; the cancellability matters even more — otherwise a request whose context times out in 5s could sit in a 429 backoff for 6.
4. **`openAIMessage` is unexported**: exporting it would lock the wire shape into the public API. Hide it now, and the next provider (Anthropic) gets its own parallel unexported types without forcing a refactor.

## What Changed / 与上一节的变化

```diff
- // s01: FakeProvider is a concrete type; the loop depends on it directly
- type Agent struct {
-     Provider *FakeProvider   // ← concrete type, no swap possible
-     ...
- }

+ // s02: Provider is now an interface; the loop is interface-only
+ type Provider interface {
+     Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error)
+ }
+
+ // Both implementations satisfy it:
+ var _ Provider = (*MockProvider)(nil)
+ var _ Provider = (*OpenAIProvider)(nil)
```

`Response` grows three new fields:

```diff
  type Response struct {
      Text       string
      Actions    []ActionCall
      StopReason string
+     InputTokens  int   // prompt tokens (s10 cost tracking reuses)
+     OutputTokens int   // completion tokens
+     Model        string // server-resolved model id
  }
```

New `ToolSchema` type: tool catalog and message history travel separately — most LLM APIs require `tools` at the top level and will reject tool_use blocks if no tools were declared.

## Try It / 动手试一试

```bash
cd agents/s02-llm-provider

# Mock mode (no API key, deterministic)
go run . -mock "search hacker news"

# Real OpenAI (requires $OPENAI_API_KEY)
OPENAI_API_KEY=sk-... go run . "what is the capital of France?"
OPENAI_API_KEY=sk-... go run . -model gpt-4o-mini "navigate https://example.com"

# Any OpenAI-compatible local server (Ollama, vLLM, LM Studio)
OPENAI_API_KEY=ignored go run . -base-url http://localhost:11434/v1 "hello"

# All 6 tests (httptest-stubbed, no real network)
go test -v ./...
```

Expected output shape:

```
$ go run . -mock "search hacker news"
model: mock-llm
stop_reason: tool_use
tokens: in=12 out=8
text: I will search for "search hacker news".
action[0]: search({"query":"search hacker news"})
```

MockProvider is deterministic, so the mock output is **byte-reproducible**. Real OpenAI output varies due to sampling, but the field shape is identical.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/llm/openai/chat.py` is 306 lines (verifiable with `wc -l`). Below is the core of `ainvoke()` (lines 152-222) plus a handful of dataclass fields — the moral equivalent of our ~250-line Go `openai_provider.go`:

```python
# Source: browser_use/llm/openai/chat.py#L24-L222
# License: MIT (Copyright 2024 Gregor Zunic)
@dataclass
class ChatOpenAI(BaseChatModel):
    """A wrapper around AsyncOpenAI that implements the BaseLLM protocol."""

    # Our Go version keeps only Model/APIKey/BaseURL. The upstream
    # frequency_penalty=0.3 is a workaround for 4.1-mini's "infinite \t" bug.
    model: ChatModel | str
    temperature: float | None = 0.2
    frequency_penalty: float | None = 0.3
    reasoning_effort: ReasoningEffort = 'low'
    api_key: str | None = None
    base_url: str | None = None
    timeout: float | None = None
    max_retries: int = 5  # SDK retries internally; we hand-roll in Go.

    # Reasoning models (o-series, gpt-5) reject temperature/frequency_penalty
    # and instead accept reasoning_effort. Our Go version omits this — out of scope.
    reasoning_models: list[str] | None = field(default_factory=lambda: ['o3', 'o3-mini', 'gpt-5'])

    async def ainvoke(self, messages, output_format=None, **kwargs):
        # Phase 1: serialize our Message into OpenAI wire format.
        # Maps to Go convertMessage() — split assistant turn's tool_use,
        # each tool_result becomes its own role:"tool" message.
        openai_messages = OpenAIMessageSerializer.serialize_messages(messages)

        try:
            # Phase 2: build model_params on-demand. Principle: only include
            # set fields, the API rejects null values.
            model_params: dict[str, Any] = {}
            if self.temperature is not None: model_params['temperature'] = self.temperature
            if self.frequency_penalty is not None: model_params['frequency_penalty'] = self.frequency_penalty

            # Model hack: reasoning models take a different set of params.
            if self.reasoning_models and any(m in str(self.model).lower() for m in self.reasoning_models):
                model_params['reasoning_effort'] = self.reasoning_effort
                model_params.pop('temperature', None)
                model_params.pop('frequency_penalty', None)

            # Phase 3: dispatch. The output_format=None branch is what our Go Invoke() does in full.
            if output_format is None:
                response = await self.get_client().chat.completions.create(
                    model=self.model, messages=openai_messages, **model_params,
                )
                choice = response.choices[0] if response.choices else None
                if choice is None:
                    # Defensive: third-party proxies occasionally return empty choices.
                    raise ModelProviderError(message='no choices', status_code=502)
                return ChatInvokeCompletion(
                    completion=choice.message.content or '',
                    usage=self._get_usage(response),
                    stop_reason=choice.finish_reason,
                )
            # ... else branch (structured output) — we defer to s04.

        # Phase 4: error classification. SDK exceptions become a uniform
        # ModelProviderError family.
        except RateLimitError as e:
            raise ModelRateLimitError(message=e.message) from e
        except APIConnectionError as e:
            raise ModelProviderError(message=str(e)) from e
        except APIStatusError as e:
            raise ModelProviderError(message=e.message, status_code=e.status_code) from e
```

**Reading notes**:

- **`BaseChatModel` is a `Protocol`, not a base class**: see `browser_use/llm/base.py#L17-L60`. Python's `Protocol` is structural typing — equivalent to Go's interfaces. Mapping the shape to a Go interface is nearly a line-by-line translation.
- **`OpenAIMessageSerializer` is provider-specific**: every upstream provider has its own. When Anthropic arrives in Phase G, its serializer sits parallel to our Go `convertMessage`.
- **Retry lives in the SDK**: `max_retries=5` is handled inside `AsyncOpenAI`. We hand-roll retry-on-429 — easier to read, harder to get right (we don't yet honour `Retry-After`; the upstream SDK does).
- **The reasoning-models branch = protocol leakage in disguise**: `if any(m in self.model for m in reasoning_models)` is brittle name matching. Any real provider abstraction grows N such special-case branches over time. We deliberately don't implement it in Go — left as an open question for "can our interface absorb special cases gracefully".
- **`output_format` uses `@overload` to give type-checkers a precise signature**: Go has no equivalent — we'd need generics (`Invoke[T any]`) or a separate `InvokeStructured` method. s04 makes that call.
- **`finish_reason` has more than three values**: real APIs also emit `function_call` (legacy), `content_filter` (safety filter), and aliases like `max_tokens`. Our Go `parseResponse` passes unknown values through verbatim and lets the loop layer cope.

**Read further**: start at `browser_use/llm/base.py#L17-L60` for the Protocol, then jump to `browser_use/llm/openai/chat.py` for the OpenAI implementation, then diff against `browser_use/llm/anthropic/chat.py` for the Anthropic one — that path is the code map for Phase G's multi-provider work.

---

**Next up**: s03 upgrades the unbounded `[]Message` into a `MessageManager` — supporting old-turn compaction, last-N retention, and sensitive-data redaction. s02's `Provider.Invoke(ctx, msgs, tools)` shape stays put; the loop still doesn't move.
