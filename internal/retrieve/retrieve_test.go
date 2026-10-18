package retrieve

import (
	"context"
	"reflect"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/testdb"
)

func TestIdentifiers(t *testing.T) {
	cases := map[string][]string{
		"Where is `NewDriver` defined?":                    {"NewDriver"},
		"What does Request.AuditRecord do?":                {"Request.AuditRecord", "AuditRecord"},
		"How does selectInternal pick tokens?":             {"selectInternal"},
		"Is eth_subscribe used?":                           {"eth_subscribe"},
		"What is in token/services/ttx/db.go?":             {"token/services/ttx/db.go", "go"},
		"How does the EVM driver collect endorsements?":    nil,
		"What does the TokenStateFactory contract deploy?": {"TokenStateFactory"},
	}
	for q, want := range cases {
		if got := Identifiers(q); !reflect.DeepEqual(got, want) {
			t.Errorf("Identifiers(%q) = %q, want %q", q, got, want)
		}
	}
}

// fakeEmbedder maps each text to a fixed vector.
type fakeEmbedder map[string][]float32

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f[t]
	}
	return out, nil
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	store, err := index.Open(ctx, testdb.URL(t, "ask_panurus_retrieve_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	mk := func(id, path, symbol, recv, content string) chunk.Chunk {
		c := chunk.Chunk{ID: id, Kind: chunk.KindCode, Path: path, Symbol: symbol, Receiver: recv,
			StartLine: 1, EndLine: 5, Content: content, EmbedText: content}
		c.ContentHash = chunk.Hash(c.EmbedText)
		return c
	}
	chunks := []chunk.Chunk{
		mk("collect", "evm/endorsement/initiator.go", "Collect", "*Initiator",
			"// Collect gathers endorsements until a quorum of signatures agrees.\nfunc (i *Initiator) Collect() {}"),
		mk("logger", "evm/endorsement/logger.go", "var logger", "", "var logger = logging.MustGetLogger()"),
		mk("finality", "evm/finality/manager.go", "Manager", "", "type Manager struct { endorsements int }"),
		mk("digest", "evm/eip712/digest.go", "Digest", "", "func Digest(d Domain, s StateDelta) [32]byte"),
	}
	for i := 0; i < 20; i++ { // filler so document frequencies mean something
		chunks = append(chunks, mk("filler"+string(rune('a'+i)), "other/x.go", "F", "", "func F() { return }"))
	}
	vid, _ := store.CreateVersion(ctx, "r", "sha", "ast")
	if err := store.InsertChunks(ctx, vid, chunks); err != nil {
		t.Fatal(err)
	}
	if err := store.ComputeTermStats(ctx, vid); err != nil {
		t.Fatal(err)
	}
	emb := fakeEmbedder{"hashing for signers": {0, 1}}
	var hashes []string
	var vecs [][]float32
	for _, c := range chunks {
		hashes = append(hashes, c.ContentHash)
		if c.ID == "digest" {
			vecs = append(vecs, []float32{0, 1})
		} else {
			vecs = append(vecs, []float32{1, 0})
		}
	}
	if err := store.PutEmbeddings(ctx, "m", hashes, vecs); err != nil {
		t.Fatal(err)
	}
	r := &Retriever{Store: store, Embedder: emb, Model: "m"}

	// BM25 puts the chunk about endorsements first, not the one that merely
	// shares the path.
	hits, err := r.Search(ctx, "How does the EVM driver collect endorsements?", Options{VersionID: vid, Mode: ModeKeyword, TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "collect" {
		t.Fatalf("keyword top hit = %v", ids(hits))
	}
	if c := hits[0].Signals["coverage"]; c <= 0 || c > 1 {
		t.Errorf("coverage = %v, want in (0, 1]", c)
	}

	// A term the corpus has never seen pulls coverage down.
	hits, _ = r.Search(ctx, "collect endorsements over kafka", Options{VersionID: vid, Mode: ModeKeyword, TopK: 1})
	if c := hits[0].Signals["coverage"]; c >= 0.6 {
		t.Errorf("coverage with an unknown term = %v, want < 0.6", c)
	}

	// An exact identifier wins even with no other overlap.
	hits, _ = r.Search(ctx, "What does Initiator.Collect return?", Options{VersionID: vid, Mode: ModeKeyword, TopK: 3})
	if hits[0].ID != "collect" || !hits[0].Exact {
		t.Fatalf("symbol top hit = %v exact=%v", ids(hits), hits[0].Exact)
	}

	// Vector search finds a chunk with no shared words.
	hits, _ = r.Search(ctx, "hashing for signers", Options{VersionID: vid, Mode: ModeVector, TopK: 1})
	if hits[0].ID != "digest" || hits[0].Signals["cosine"] < 0.99 {
		t.Fatalf("vector top hit = %v signals=%v", ids(hits), hits[0].Signals)
	}
	hits, _ = r.Search(ctx, "hashing for signers", Options{VersionID: vid, Mode: ModeHybrid, TopK: 3})
	if hits[0].ID != "digest" || hits[0].Ranks["vector"] != 1 {
		t.Fatalf("hybrid top hit = %v ranks=%v", ids(hits), hits[0].Ranks)
	}
}

func ids(hs []Hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.ID)
	}
	return out
}
