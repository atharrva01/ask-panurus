package index

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

// The store tests need a real Postgres with pgvector, for example:
//
//	docker compose -f deploy/docker-compose.yml up -d postgres
//	ASK_PANURUS_TEST_DB=postgres://ask:ask@localhost:5433/postgres go test ./internal/index/
//
// Each run creates and drops its own database.
func testStore(t *testing.T) *Store {
	t.Helper()
	admin := os.Getenv("ASK_PANURUS_TEST_DB")
	if admin == "" {
		t.Skip("ASK_PANURUS_TEST_DB not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	const name = "ask_panurus_test"
	conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(admin)
	cfg.Database = name
	s, err := Open(ctx, cfg.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
		conn.Close(ctx)
	})
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
	s.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE version_id = $1 AND tsv @@ to_tsquery('simple', 'factory')`, v2).Scan(&n)
	if n != 1 {
		t.Errorf("keyword match on camelCase part = %d, want 1", n)
	}
	var q string
	s.Pool.QueryRow(ctx, `SELECT qualname FROM chunks WHERE version_id = $1 AND id = 'a'`, v2).Scan(&q)
	if q != "Driver.New" {
		t.Errorf("qualname = %q", q)
	}
}
