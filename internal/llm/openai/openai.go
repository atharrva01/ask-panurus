// Package openai is an llm.Provider for any server that speaks the OpenAI
// chat completions API. That covers a local Ollama (the free, reproducible
// path in PRD section 7) and several free hosted tiers, so one client serves
// all of them; only the base URL, model and key differ.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/atharrva01/ask-panurus/internal/llm"
)

// OllamaURL is the default local Ollama endpoint.
const OllamaURL = "http://localhost:11434/v1"

// Provider calls POST {BaseURL}/chat/completions.
type Provider struct {
	BaseURL string
	Model   string
	APIKey  string // empty for a local Ollama
	HTTP    *http.Client
}

// New returns a provider. A local model on a CPU can take minutes per
// answer, so the timeout is generous.
func New(baseURL, model, apiKey string) *Provider {
	if baseURL == "" {
		baseURL = OllamaURL
	}
	return &Provider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	MaxTokens   int64     `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends one request, retrying rate limits and server errors, which
// free tiers return often.
func (p *Provider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	if r.MaxTokens == 0 {
		r.MaxTokens = 2048
	}
	body, err := json.Marshal(chatRequest{
		Model:       p.Model,
		Messages:    []message{{Role: "system", Content: r.System}, {Role: "user", Content: r.User}},
		MaxTokens:   r.MaxTokens,
		Temperature: 0, // answers should be repeatable for the eval
	})
	if err != nil {
		return llm.Response{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-time.After(time.Duration(2<<attempt) * time.Second):
			}
		}
		resp, retry, err := p.do(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return llm.Response{}, lastErr
}

func (p *Provider) do(ctx context.Context, body []byte) (llm.Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	res, err := p.HTTP.Do(req)
	if err != nil {
		return llm.Response{}, true, fmt.Errorf("llm request: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return llm.Response{}, true, err
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return llm.Response{}, false, fmt.Errorf("%w: %s", llm.ErrNoCredentials, res.Status)
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return llm.Response{}, true, fmt.Errorf("llm: %s: %s", res.Status, bytes.TrimSpace(data))
	case res.StatusCode != http.StatusOK:
		return llm.Response{}, false, fmt.Errorf("llm: %s: %s", res.Status, bytes.TrimSpace(data))
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return llm.Response{}, false, fmt.Errorf("llm: decode: %w", err)
	}
	if cr.Error != nil {
		return llm.Response{}, false, fmt.Errorf("llm: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return llm.Response{}, false, fmt.Errorf("llm: response has no choices")
	}
	c := cr.Choices[0]
	out := llm.Response{
		Text:         c.Message.Content,
		Model:        cr.Model,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
	}
	switch c.FinishReason {
	case "content_filter":
		out.Refused, out.RefusalCategory, out.Text = true, "content_filter", ""
	case "length":
		return out, false, fmt.Errorf("answer cut off at max_tokens")
	}
	return out, false, nil
}
