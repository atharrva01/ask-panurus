package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/answer"
	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/llm"
	"github.com/atharrva01/ask-panurus/internal/retrieve"
	"github.com/atharrva01/ask-panurus/internal/testdb"
)

type cannedLLM string

func (c cannedLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: string(c), Model: "fake"}, nil
}

func TestAPI(t *testing.T) {
	ctx := context.Background()
	store, err := index.Open(ctx, testdb.URL(t, "ask_panurus_server_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	c := chunk.Chunk{ID: "c#0", Kind: chunk.KindCode, Path: "evm/initiator.go", Symbol: "Collect", Receiver: "*Initiator",
		StartLine: 3, EndLine: 9, Content: "// Collect gathers endorsements into a quorum.\nfunc (i *Initiator) Collect() {}\n"}
	c.EmbedText, c.ContentHash = c.Content, chunk.Hash(c.Content)
	vid, _ := store.CreateVersion(ctx, "LFDT-Panurus/panurus", "abc123", "ast")
	if err := store.InsertChunks(ctx, vid, []chunk.Chunk{c}); err != nil {
		t.Fatal(err)
	}
	store.ComputeTermStats(ctx, vid)
	store.Finish(ctx, vid, nil, true)

	r := &retrieve.Retriever{Store: store}
	s := &Server{Store: store, Retriever: r, Pipeline: &answer.Pipeline{
		Retriever: r, LLM: cannedLLM("`Initiator.Collect` builds the quorum [1]."), RefuseBelow: 0.5, Mode: retrieve.ModeKeyword,
	}}
	h := s.Handler()

	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:5555"
		h.ServeHTTP(w, req)
		return w
	}

	w := post("/v1/ask", `{"question": "How does Initiator.Collect build a quorum of endorsements?"}`)
	var a answer.Answer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || w.Code != 200 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	if a.Refused || len(a.Citations) != 1 || a.IndexedSHA != "abc123" ||
		a.Citations[0].URL != "https://github.com/LFDT-Panurus/panurus/blob/abc123/evm/initiator.go#L3-L9" {
		t.Fatalf("answer = %+v", a)
	}

	w = post("/v1/search", `{"question": "quorum endorsements", "mode": "keyword"}`)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"id":"c#0"`)) {
		t.Fatalf("search: %d %s", w.Code, w.Body)
	}

	for body, want := range map[string]int{`{}`: 400, `not json`: 400, `{"question": "` + strings.Repeat("x", 1001) + `"}`: 400} {
		if w := post("/v1/ask", body); w.Code != want {
			t.Errorf("body %.20q: status %d, want %d", body, w.Code, want)
		}
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"indexed_sha":"abc123"`)) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("<title>Ask Panurus</title>")) {
		t.Fatalf("page: %d", w.Code)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2)
	if !l.allow("a") || !l.allow("a") || l.allow("a") {
		t.Error("third request in the window should be refused")
	}
	if !l.allow("b") {
		t.Error("limits are per client")
	}
}
