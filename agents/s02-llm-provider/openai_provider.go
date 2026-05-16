package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider talks to any OpenAI-compatible /chat/completions endpoint
// using nothing but net/http. No SDK dependency by design — the entire
// surface area we use (request body shape, response shape, error codes) is
// stable and small enough that hand-rolling it makes the wire format
// pedagogically visible.
//
// Set BaseURL = "" to default to OpenAI itself. Compatible third-party
// servers (vLLM, LM Studio, Ollama's OpenAI mode, etc.) work by pointing
// BaseURL at their /v1 endpoint.
type OpenAIProvider struct {
	APIKey  string
	Model   string
	BaseURL string // default: https://api.openai.com/v1

	// Tuning knobs ---------------------------------------------------------
	HTTPClient *http.Client // default: http.DefaultClient with 60s timeout
	MaxRetries int          // retries on 429. default: 1 (so total 2 attempts)
	RetryDelay time.Duration // delay between attempts. default: 1s
	// Temperature is *float64 so callers can explicitly set 0; nil = omit
	Temperature *float64
}

const defaultBaseURL = "https://api.openai.com/v1"

// ---------------------------------------------------------------------------
// Wire-format types — these mirror the OpenAI /chat/completions JSON. We
// only model the subset we use. Compare to upstream
// browser_use/llm/openai/chat.py which delegates to the openai SDK
// (which defines hundreds of typed param classes).
// ---------------------------------------------------------------------------

type openAIRequest struct {
	Model       string            `json:"model"`
	Messages    []openAIMessage   `json:"messages"`
	Tools       []openAITool      `json:"tools,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
}

type openAIMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []openAIToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	Name       string             `json:"name,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"` // always "function"
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"` // "stop"|"length"|"tool_calls"
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	// Error envelope — OpenAI returns 4xx/5xx with this shape
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ---------------------------------------------------------------------------
// Invoke — the only public method. Builds wire JSON, POSTs, parses.
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
	url := p.BaseURL
	if url == "" {
		url = defaultBaseURL
	}
	url = url + "/chat/completions"

	body, err := p.buildRequestBody(msgs, tools)
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	maxRetries := p.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}
	retryDelay := p.RetryDelay
	if retryDelay == 0 {
		retryDelay = 1 * time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		respBody, status, err := p.doRequest(ctx, client, url, body)
		if err != nil {
			lastErr = err
			// network-level error — retry once after backoff
			if attempt < maxRetries {
				if sleepErr := sleepCtx(ctx, retryDelay); sleepErr != nil {
					return Response{}, sleepErr
				}
				continue
			}
			return Response{}, lastErr
		}

		// 429 → retry-eligible.
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

// buildRequestBody serializes our generic []Message + []ToolSchema into
// the exact JSON the /chat/completions endpoint expects.
func (p *OpenAIProvider) buildRequestBody(msgs []Message, tools []ToolSchema) ([]byte, error) {
	oaMsgs := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		converted := convertMessage(m)
		oaMsgs = append(oaMsgs, converted...)
	}

	oaTools := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		oaTools = append(oaTools, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	req := openAIRequest{
		Model:       p.Model,
		Messages:    oaMsgs,
		Tools:       oaTools,
		Temperature: p.Temperature,
	}
	return json.Marshal(req)
}

// convertMessage turns one of our Messages into one or more OpenAI wire
// messages. A single Message with mixed tool_use + tool_result blocks
// must be split because OpenAI's `tool` role needs one wire message per
// tool_call_id (unlike Anthropic which packs them together).
func convertMessage(m Message) []openAIMessage {
	switch m.Role {
	case "system", "user":
		// Concatenate text blocks; ignore non-text.
		var text string
		for _, b := range m.Content {
			if b.Type == "text" {
				text += b.Text
			}
		}
		return []openAIMessage{{Role: m.Role, Content: text}}

	case "assistant":
		var text string
		var calls []openAIToolCall
		for i, b := range m.Content {
			switch b.Type {
			case "text":
				text += b.Text
			case "tool_use":
				calls = append(calls, openAIToolCall{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
					Function: openAIToolCallFunction{
						Name:      b.Name,
						Arguments: b.Input,
					},
				})
			}
		}
		out := openAIMessage{Role: "assistant", Content: text}
		if len(calls) > 0 {
			out.ToolCalls = calls
		}
		return []openAIMessage{out}

	case "tool":
		// One wire message per tool_result block. The Name field carries
		// the call id from the assistant turn.
		out := make([]openAIMessage, 0, len(m.Content))
		for i, b := range m.Content {
			if b.Type == "tool_result" {
				out = append(out, openAIMessage{
					Role:       "tool",
					Content:    b.Result,
					ToolCallID: fmt.Sprintf("call_%d", i),
				})
			}
		}
		return out

	default:
		// Unknown role — pass through best-effort.
		return []openAIMessage{{Role: m.Role}}
	}
}

func (p *OpenAIProvider) doRequest(ctx context.Context, client *http.Client, url string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}

func parseResponse(respBody []byte) (Response, error) {
	var oaResp openAIResponse
	if err := json.Unmarshal(respBody, &oaResp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	if oaResp.Error != nil {
		return Response{}, fmt.Errorf("openai api error: %s (%s)", oaResp.Error.Message, oaResp.Error.Type)
	}
	if len(oaResp.Choices) == 0 {
		return Response{}, fmt.Errorf("openai response has no choices: %s", string(respBody))
	}
	choice := oaResp.Choices[0]

	// Map OpenAI's finish_reason → our StopReason.
	var stop string
	switch choice.FinishReason {
	case "stop":
		stop = "end_turn"
	case "tool_calls":
		stop = "tool_use"
	case "length":
		stop = "length"
	default:
		stop = choice.FinishReason
	}

	actions := make([]ActionCall, 0, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		actions = append(actions, ActionCall{
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	return Response{
		Text:         choice.Message.Content,
		Actions:      actions,
		StopReason:   stop,
		InputTokens:  oaResp.Usage.PromptTokens,
		OutputTokens: oaResp.Usage.CompletionTokens,
		Model:        oaResp.Model,
	}, nil
}

// sleepCtx sleeps for d but returns early if ctx is cancelled. This makes
// the retry loop responsive to caller cancellation — important for tests
// that cancel ctx and don't want to wait the full 1s backoff.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
