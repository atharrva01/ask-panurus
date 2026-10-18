// Package llm is the provider interface behind answer generation (PRD
// section 7): a hosted API in production, a local model for reproducible
// runs. The answer pipeline depends only on Provider.
package llm

import (
	"context"
	"errors"
)

// Request is one single-turn completion.
type Request struct {
	System    string
	User      string
	MaxTokens int64
}

// Response is the model's reply.
type Response struct {
	Text string
	// Refused is true when the provider's safety classifier declined the
	// request (after any server-side fallback).
	Refused         bool
	RefusalCategory string
	Model           string // the model that actually answered
	InputTokens     int64
	OutputTokens    int64
}

// Provider generates text.
type Provider interface {
	Complete(ctx context.Context, r Request) (Response, error)
}

// ErrNoCredentials means the provider has nothing to authenticate with.
var ErrNoCredentials = errors.New("no LLM credentials: set ANTHROPIC_API_KEY")
