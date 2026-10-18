// Package embed is the client for Hugging Face Text Embeddings Inference
// (TEI). Keeping models behind HTTP lets the ablations swap them without
// touching Go code.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one TEI server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// BatchSize is the number of texts per request. TEI's default
	// --max-client-batch-size is 32.
	BatchSize int
}

// New returns a client for the TEI server at baseURL.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		BatchSize: 16,
	}
}

// Info describes the model a TEI server is serving.
type Info struct {
	ModelID        string `json:"model_id"`
	MaxInputLength int    `json:"max_input_length"`
}

// Info asks the server which model it serves. Embeddings are stored under
// this ID, so they can never be attributed to the wrong model.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var info Info
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/info", nil)
	if err != nil {
		return info, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return info, fmt.Errorf("tei info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("tei info: %s", resp.Status)
	}
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// Embed returns one normalised vector per text, in order. Texts longer than
// the model's limit are truncated by the server; the ingester's chunk cap
// keeps that rare.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += c.BatchSize {
		end := min(start+c.BatchSize, len(texts))
		vecs, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"inputs": texts, "truncate": true, "normalize": true})
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embed", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			var vecs [][]float32
			if err := json.Unmarshal(data, &vecs); err != nil {
				return nil, fmt.Errorf("tei embed: decode: %w", err)
			}
			if len(vecs) != len(texts) {
				return nil, fmt.Errorf("tei embed: got %d vectors for %d texts", len(vecs), len(texts))
			}
			return vecs, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("tei embed: %s: %s", resp.Status, bytes.TrimSpace(data))
		default:
			return nil, fmt.Errorf("tei embed: %s: %s", resp.Status, bytes.TrimSpace(data))
		}
	}
	return nil, lastErr
}
