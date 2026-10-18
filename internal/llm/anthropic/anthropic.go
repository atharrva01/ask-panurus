// Package anthropic is the Claude implementation of llm.Provider. It is kept
// apart from package llm so that only the binaries that call Claude compile
// the SDK.
package anthropic

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/atharrva01/ask-panurus/internal/llm"
)

// DefaultModel is the default answer model.
const DefaultModel = "claude-opus-5-5"

// Provider calls the Claude Messages API.
type Provider struct {
	client anthropic.Client
	Model  string
	// Effort is the output effort level. Claude Opus 5.5 defaults to
	// medium, so it is always sent explicitly.
	Effort anthropic.BetaOutputConfigEffort
}

// New returns a Claude provider. The SDK reads ANTHROPIC_API_KEY
// and retries 408, 409, 429 and 5xx responses itself.
func New(model string, effort string, opts ...option.RequestOption) *Provider {
	if model == "" {
		model = DefaultModel
	}
	if effort == "" {
		effort = string(anthropic.BetaOutputConfigEffortMedium)
	}
	return &Provider{
		client: anthropic.NewClient(opts...),
		Model:  model,
		Effort: anthropic.BetaOutputConfigEffort(effort),
	}
}

// Complete sends one request. A safety-classifier refusal is retried
// server-side on Anthropic's recommended fallback model for that refusal
// category (fallbacks: "default"); only if that also declines does the
// response come back Refused.
func (a *Provider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	if r.MaxTokens == 0 {
		r.MaxTokens = 16000
	}
	msg, err := a.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        a.Model,
		MaxTokens:    r.MaxTokens,
		System:       []anthropic.BetaTextBlockParam{{Text: r.System}},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User))},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: a.Effort},
		Fallbacks:    anthropic.BetaFallbacksParamOfDefault(),
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			return llm.Response{}, fmt.Errorf("%w (%v)", llm.ErrNoCredentials, err)
		}
		return llm.Response{}, err
	}
	out := llm.Response{
		Model:        string(msg.Model),
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}
	// Check the stop reason before reading content.
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		out.Refused = true
		out.RefusalCategory = string(msg.StopDetails.Category)
		return out, nil
	}
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			out.Text += t.Text
		}
	}
	if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
		return out, fmt.Errorf("answer cut off at max_tokens (%d)", r.MaxTokens)
	}
	return out, nil
}
