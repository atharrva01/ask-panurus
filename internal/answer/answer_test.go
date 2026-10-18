package answer

import (
	"context"
	"strings"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/llm"
	"github.com/atharrva01/ask-panurus/internal/retrieve"
)

type fakeSearch []retrieve.Hit

func (f fakeSearch) Search(context.Context, string, retrieve.Options) ([]retrieve.Hit, error) {
	return f, nil
}

type fakeLLM struct {
	reply   string
	refused bool
	got     llm.Request
	calls   int
}

func (f *fakeLLM) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	f.calls++
	f.got = r
	return llm.Response{Text: f.reply, Refused: f.refused, Model: "fake"}, nil
}

var v = VersionInfo{ID: 2, Repo: "LFDT-Panurus/panurus", Commit: "6cf946a9b519"}

func hits(coverage float64) fakeSearch {
	return fakeSearch{
		{ID: "a", Kind: "code", Path: "x/evm/initiator.go", Qualname: "Initiator.Collect", StartLine: 119, EndLine: 208,
			Content: "func (i *Initiator) Collect() {}\n", Signals: map[string]float64{"coverage": coverage}},
		{ID: "b", Kind: "doc", Path: "docs/evm.md", StartLine: 1, EndLine: 9, Content: "# EVM\n", Signals: map[string]float64{"coverage": 0.2}},
	}
}

func TestCitedAnswer(t *testing.T) {
	f := &fakeLLM{reply: "`Initiator.Collect` groups signatures by digest [1]. See also [7]."}
	p := &Pipeline{Retriever: hits(0.95), LLM: f, RefuseBelow: 0.6}
	a, err := p.Ask(context.Background(), "How are endorsements collected?", v)
	if err != nil {
		t.Fatal(err)
	}
	if a.Refused || len(a.Citations) != 1 || a.Citations[0].N != 1 {
		t.Fatalf("answer = %+v", a)
	}
	if strings.Contains(a.Text, "[7]") {
		t.Error("citation to a chunk that does not exist was kept")
	}
	if want := "https://github.com/LFDT-Panurus/panurus/blob/6cf946a9b519/x/evm/initiator.go#L119-L208"; a.Citations[0].URL != want {
		t.Errorf("url = %s", a.Citations[0].URL)
	}
	if a.Confidence != "high" || a.IndexedSHA != v.Commit || a.RequestID == "" {
		t.Errorf("metadata = %+v", a)
	}
	for _, want := range []string{`<chunk n="1" kind="code" path="x/evm/initiator.go" lines="119-208" name="Initiator.Collect">`, "Question: How are endorsements collected?"} {
		if !strings.Contains(f.got.User, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string]struct {
		coverage float64
		llm      *fakeLLM
		calls    int
	}{
		"low coverage skips the model": {0.3, &fakeLLM{reply: "x [1]"}, 0},
		"model says not found":         {0.95, &fakeLLM{reply: NotFound}, 1},
		"answer without citations":     {0.95, &fakeLLM{reply: "It uses a digest."}, 1},
		"provider safety refusal":      {0.95, &fakeLLM{refused: true}, 1},
	}
	for name, c := range cases {
		p := &Pipeline{Retriever: hits(c.coverage), LLM: c.llm, RefuseBelow: 0.6}
		a, err := p.Ask(context.Background(), "q", v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !a.Refused || len(a.Pointers) != 2 || len(a.Citations) != 0 || a.Confidence != "low" {
			t.Errorf("%s: answer = %+v", name, a)
		}
		if c.llm.calls != c.calls {
			t.Errorf("%s: model called %d times, want %d", name, c.llm.calls, c.calls)
		}
	}
}
