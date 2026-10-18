package index

import (
	"context"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/testdb"
	"github.com/atharrva01/ask-panurus/internal/tokenize"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), testdb.URL(t, "ask_panurus_index_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func mk(id, text string) chunk.Chunk {
	c := chunk.Chunk{ID: id, Kind: chunk.KindCode, Path: "p.go", Symbol: "New", Receiver: "*Driver",
		StartLine: 1, EndLine: 2, Content: text, EmbedText: text}
	c.ContentHash = chunk.Hash(c.EmbedText)
	return c
}

func TestVersionsEmbeddingCacheAndLiveSwitch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v1, _ := s.CreateVersion(ctx, "r", "sha1", "ast")
	if err := s.InsertChunks(ctx, v1, []chunk.Chunk{mk("a", "func TokenStateFactory()"), mk("b", "two")}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.MissingEmbeddings(ctx, v1, "m")
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if err := s.PutEmbeddings(ctx, "m", []string{pending[0].Hash, pending[1].Hash}, [][]float32{{1, 0}, {0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, v1, map[string]int{"n": 2}, true); err != nil {
		t.Fatal(err)
	}

	// A rebuild with one changed chunk only needs one new embedding.
	v2, _ := s.CreateVersion(ctx, "r", "sha2", "ast")
	s.InsertChunks(ctx, v2, []chunk.Chunk{mk("a", "func TokenStateFactory()"), mk("b", "changed")})
	pending, _ = s.MissingEmbeddings(ctx, v2, "m")
	if len(pending) != 1 || pending[0].Text != "changed" {
		t.Fatalf("pending after rebuild = %v", pending)
	}
	if err := s.Finish(ctx, v2, nil, true); err != nil {
		t.Fatal(err)
	}

	vs, err := s.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vs[0].Status != "live" || vs[1].Status != "retired" {
		t.Errorf("statuses = %s, %s; want live, retired", vs[0].Status, vs[1].Status)
	}
	if vs[1].Embedded["m"] != 2 || vs[0].Embedded["m"] != 1 {
		t.Errorf("coverage = %v / %v", vs[0].Embedded, vs[1].Embedded)
	}

	// Keyword index: the camelCase identifier is findable by a part, and the
	// qualified method name is stored for exact lookup.
	var n int
	term := tokenize.QueryTerms("factory")[0] // queries are tokenized like the index
	s.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE version_id = $1 AND tsv @@ to_tsquery('simple', $2)`, v2, term).Scan(&n)
	if n != 1 {
		t.Errorf("keyword match on camelCase part = %d, want 1", n)
	}
	var q string
	s.Pool.QueryRow(ctx, `SELECT qualname FROM chunks WHERE version_id = $1 AND id = 'a'`, v2).Scan(&q)
	if q != "Driver.New" {
		t.Errorf("qualname = %q", q)
	}
}
