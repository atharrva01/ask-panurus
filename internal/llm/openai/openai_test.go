package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/llm"
)

func TestComplete(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if calls == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Temperature != 0 {
			t.Errorf("request = %+v", req)
		}
		w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"x [1]"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	p := New(srv.URL+"/v1", "m", "k")
	resp, err := p.Complete(context.Background(), llm.Request{System: "s", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "x [1]" || resp.OutputTokens != 2 || calls != 2 {
		t.Fatalf("resp = %+v calls = %d", resp, calls)
	}
}

func TestBadKeyIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "m", "bad").Complete(context.Background(), llm.Request{})
	if !errors.Is(err, llm.ErrNoCredentials) || calls != 1 {
		t.Fatalf("err = %v calls = %d", err, calls)
	}
}
