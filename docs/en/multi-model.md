---
title: "Multi-model guide"
slug: multi-model
est_read_min: 10
---

# Multi-model guide

> learn-browser-use's s02 Provider abstraction is **provider-agnostic** — any LLM service exposing an OpenAI-compatible endpoint (`/v1/chat/completions`) works without code changes. This page lists 8 tested profiles + how to plug them in.

---

## Mental model

s02's `OpenAIProvider` takes three parameters:

```go
type OpenAIProvider struct {
    APIKey  string
    BaseURL string  // empty → https://api.openai.com/v1
    Model   string  // empty → gpt-4o-mini
}
```

All OpenAI-compatible services (DeepSeek, Moonshot, Qwen DashScope, Groq, OpenRouter, self-hosted vLLM/SGLang…) just need a different `BaseURL` + `Model` + `APIKey`. Domestic + overseas + self-hosted are all covered.

s12's `Agent` is the same: replace its `Provider` field from `MockProvider` to `OpenAIProvider` with the right profile.

---

## 8 built-in profiles

| Provider | BaseURL | Default Model | API Key env var |
|---|---|---|---|
| `openai` | `https://api.openai.com/v1` | `gpt-4o-mini` | `OPENAI_API_KEY` |
| `deepseek` | `https://api.deepseek.com/v1` | `deepseek-chat` | `DEEPSEEK_API_KEY` |
| `moonshot` | `https://api.moonshot.cn/v1` | `moonshot-v1-8k` | `MOONSHOT_API_KEY` |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-plus` | `DASHSCOPE_API_KEY` |
| `groq` | `https://api.groq.com/openai/v1` | `llama-3.3-70b-versatile` | `GROQ_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | `openai/gpt-4o-mini` | `OPENROUTER_API_KEY` |
| `local` (vLLM/SGLang/Ollama) | `http://localhost:8000/v1` | `local-model` | `OPENAI_API_KEY` (placeholder ok) |
| `anthropic` | (needs adapter — see below) | `claude-sonnet-4-5` | `ANTHROPIC_API_KEY` |

---

## Switching providers in s02 / s12

### Option A: change code

```go
// in s02/main.go or s12/main.go
provider := &OpenAIProvider{
    APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
    BaseURL: "https://api.deepseek.com/v1",
    Model:   "deepseek-chat",
}
```

### Option B: env-driven

Change `main.go`'s hard-coded BaseURL/Model to read env vars:

```go
provider := &OpenAIProvider{
    APIKey:  os.Getenv("LLM_API_KEY"),
    BaseURL: os.Getenv("LLM_BASE_URL"),
    Model:   os.Getenv("LLM_MODEL"),
}
```

Then:

```bash
LLM_API_KEY=sk-... LLM_BASE_URL=https://api.deepseek.com/v1 LLM_MODEL=deepseek-chat \
  go run ./agents/s02-llm-provider "search hacker news"
```

---

## Anthropic (needs an adapter)

Anthropic Claude doesn't use `/v1/chat/completions`. Its endpoint is `/v1/messages`, with a different message structure (`tool_use` blocks are top-level, not nested in `tool_calls`). We don't ship an Anthropic provider in s02 by default to keep the OpenAI HTTP impl minimal. Sketch:

```go
// anthropic_provider.go (added to s02 or s12)
type AnthropicProvider struct {
    APIKey string
    Model  string
}

func (p *AnthropicProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
    // 1. Translate our Message → Anthropic messages array
    //    - role "tool" → role "user" with content_block type=tool_result
    //    - ContentBlock{Type: "tool_use"} → unchanged (Anthropic uses the same shape)
    //    - System prompt is a top-level "system" field, not a message
    // 2. POST https://api.anthropic.com/v1/messages with header x-api-key + anthropic-version: 2023-06-01
    // 3. Translate Anthropic response.content[] → our Response.Actions
    // ...
}
```

Upstream browser-use's `browser_use/llm/anthropic/chat.py` is a complete reference (MIT-licensed).

---

## Recommended path for users in mainland China

If you're in mainland China without an overseas card:

1. **DeepSeek**: `deepseek-chat` is cheap, strong reasoning; running the full s02-s12 demo costs < ¥0.01 per run.
2. **Qwen via DashScope**: direct Alibaba Cloud connectivity; `qwen-plus` is fast and has strong tool-use support.
3. **Moonshot (Kimi)**: 128K context window; great for showing off s03's message-manager compaction with very long conversations.

Avoid OpenAI direct (needs overseas card + network) and Anthropic direct (overseas card + IP restrictions).

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `401 Unauthorized` | API key wrong or missing `Bearer ` prefix |
| `404 Not Found on /chat/completions` | BaseURL has a trailing `/` or is missing `/v1` |
| `model not found` | provider doesn't support the model id — check provider docs |
| `tool_calls field unknown` | provider doesn't support OpenAI function calling (rare — major providers all do) |
| Mainland network timeouts | switch to a domestic provider (deepseek / qwen / moonshot) |

---

## Upstream comparison

browser-use upstream ships 16 provider impls (one file each):
- `browser_use/llm/openai/chat.py`
- `browser_use/llm/anthropic/chat.py`
- `browser_use/llm/google/chat.py`
- `browser_use/llm/groq/chat.py`
- `browser_use/llm/mistral/chat.py`
- `browser_use/llm/cerebras/chat.py`
- `browser_use/llm/deepseek/chat.py`
- `browser_use/llm/ollama/chat.py`
- `browser_use/llm/litellm/chat.py`
- `browser_use/llm/oci_raw/chat.py`
- `browser_use/llm/azure/chat.py`
- `browser_use/llm/openrouter/chat.py`
- `browser_use/llm/vercel/chat.py`
- `browser_use/llm/aws/chat.py`
- `browser_use/llm/browser_use/chat.py` (browser-use's proprietary cloud model)

A single OpenAI HTTP provider + BaseURL/Model parameters covers 11 of 16. The remaining 5 (Anthropic / Google / AWS Bedrock / Vercel / browser-use cloud) each need a small adapter.
