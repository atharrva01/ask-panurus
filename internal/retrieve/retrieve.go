// Package retrieve finds the chunks that answer a question (PRD section 9,
// stages 1 to 4): identifier extraction, then BM25 keyword search, exact
// symbol lookup and vector search, fused with reciprocal rank fusion.
package retrieve

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/tokenize"
)

// Mode selects which retrievers run. Each ablation run is one mode.
type Mode string

const (
	ModeKeyword Mode = "keyword" // BM25 plus symbol lookup
	ModeVector  Mode = "vector"
	ModeHybrid  Mode = "hybrid"
)

const (
	// candidates is how many results each retriever contributes to fusion.
	candidates = 30
	// rrfK is the standard reciprocal rank fusion constant.
	rrfK = 60
	// exactBoost is added for an exact identifier match. It is larger than
	// a first place in any single list (1/61), so a question that names a
	// symbol finds it first.
	exactBoost = 0.02

	// BM25 parameters, and the extra weight of name and path tokens over
	// body tokens.
	bm25K1     = 1.2
	bm25B      = 0.75
	nameWeight = 3
)

// Embedder turns a query into a vector. *embed.Client implements it.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Retriever searches one index version.
type Retriever struct {
	Store    *index.Store
	Embedder Embedder // nil disables vector search
	Model    string   // embedding model ID, as stored in the embeddings table
}

// Options configures one search.
type Options struct {
	VersionID int64 // 0 means the live version
	TopK      int
	Mode      Mode
	Kinds     []string // restrict to chunk kinds; empty means all
}

// Hit is one retrieved chunk.
type Hit struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Path      string         `json:"path"`
	Qualname  string         `json:"qualname,omitempty"`
	StartLine int            `json:"start_line"`
	EndLine   int            `json:"end_line"`
	Content   string         `json:"content"`
	Score     float64        `json:"score"`
	Ranks     map[string]int `json:"ranks"` // retriever -> 1-based rank
	Exact     bool           `json:"exact,omitempty"`
	// Signals holds absolute scores, unlike Score, which only reflects
	// ranks: "bm25" (raw BM25), "coverage" (share of the question's IDF
	// weight this chunk contains, 0 to 1; a term absent from the corpus
	// counts at full weight) and "cosine" (vector similarity). Refusal
	// decisions use these.
	Signals map[string]float64 `json:"signals,omitempty"`
}

// Search runs the configured retrievers and fuses their results.
func (r *Retriever) Search(ctx context.Context, question string, o Options) ([]Hit, error) {
	if o.TopK <= 0 {
		o.TopK = 10
	}
	if o.Mode == "" {
		o.Mode = ModeHybrid
	}
	if o.VersionID == 0 {
		v, err := r.Store.LiveVersion(ctx)
		if err != nil {
			return nil, err
		}
		o.VersionID = v
	}

	lists := map[string][]string{}
	exact := map[string]bool{}
	signals := map[string]map[string]float64{}
	setSignal := func(id, name string, v float64) {
		if signals[id] == nil {
			signals[id] = map[string]float64{}
		}
		signals[id][name] = v
	}
	if o.Mode == ModeKeyword || o.Mode == ModeHybrid {
		scored, err := r.bm25(ctx, o, tokenize.QueryTerms(question))
		if err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
		for _, s := range scored {
			lists["bm25"] = append(lists["bm25"], s.id)
			setSignal(s.id, "bm25", s.score)
			setSignal(s.id, "coverage", s.coverage)
		}
		ids, err := r.symbols(ctx, o, Identifiers(question))
		if err != nil {
			return nil, fmt.Errorf("symbol lookup: %w", err)
		}
		lists["symbol"] = ids
		for _, id := range ids {
			exact[id] = true
		}
	}
	if o.Mode == ModeVector || o.Mode == ModeHybrid {
		if r.Embedder == nil || r.Model == "" {
			if o.Mode == ModeVector {
				return nil, fmt.Errorf("vector search needs an embedding server")
			}
		} else {
			scored, err := r.vector(ctx, o, question)
			if err != nil {
				return nil, fmt.Errorf("vector search: %w", err)
			}
			for _, s := range scored {
				lists["vector"] = append(lists["vector"], s.id)
				setSignal(s.id, "cosine", s.score)
			}
		}
	}

	scores := map[string]float64{}
	ranks := map[string]map[string]int{}
	for name, ids := range lists {
		for i, id := range ids {
			scores[id] += 1.0 / float64(rrfK+i+1)
			if ranks[id] == nil {
				ranks[id] = map[string]int{}
			}
			ranks[id][name] = i + 1
		}
	}
	for id := range exact {
		scores[id] += exactBoost
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > o.TopK {
		ids = ids[:o.TopK]
	}
	hits, err := r.load(ctx, o.VersionID, ids)
	if err != nil {
		return nil, err
	}
	for i := range hits {
		hits[i].Score = scores[hits[i].ID]
		hits[i].Ranks = ranks[hits[i].ID]
		hits[i].Exact = exact[hits[i].ID]
		hits[i].Signals = signals[hits[i].ID]
	}
	return hits, nil
}

type scored struct {
	id       string
	score    float64
	coverage float64
}

// bm25 scores chunks with Okapi BM25 over the code-aware tokens. Term
// frequency counts a name or path occurrence nameWeight times (a simple
// BM25F), so a chunk whose symbol matches outranks one that only mentions
// the term in passing. Postgres caps positions at 256 per lexeme, which is
// far past BM25's saturation point.
func (r *Retriever) bm25(ctx context.Context, o Options, terms []string) ([]scored, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	// The question's total IDF weight. A term that no chunk contains gets
	// the largest possible IDF, so a question about something the index
	// has never seen ("kafka", "solana") has low coverage everywhere.
	var total float64
	if err := r.Store.Pool.QueryRow(ctx, `
		SELECT coalesce(sum(coalesce(ln(1 + (v.n_docs - ts.df + 0.5) / (ts.df + 0.5)),
		                             ln(1 + (v.n_docs + 0.5) / 0.5))), 0)
		FROM unnest($2::text[]) t(term)
		CROSS JOIN (SELECT n_docs FROM index_versions WHERE id = $1) v
		LEFT JOIN term_stats ts ON ts.version_id = $1 AND ts.term = t.term`,
		o.VersionID, terms).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := r.Store.Pool.Query(ctx, `
		WITH v AS (SELECT n_docs, avg_tokens FROM index_versions WHERE id = $1),
		q AS (
			SELECT ts.term, ln(1 + (v.n_docs - ts.df + 0.5) / (ts.df + 0.5)) AS idf
			FROM term_stats ts, v
			WHERE ts.version_id = $1 AND ts.term = ANY($2)
		),
		cand AS (
			SELECT c.id, c.tsv, c.ntokens FROM chunks c
			WHERE c.version_id = $1
			  AND c.tsv @@ to_tsquery('simple', $3)
			  AND (cardinality($4::text[]) = 0 OR c.kind = ANY($4))
		),
		tf AS (
			SELECT cand.id, cand.ntokens, u.lexeme,
			       (SELECT count(*) FILTER (WHERE w = 'A') * $6 + count(*) FILTER (WHERE w <> 'A')
			        FROM unnest(u.weights) w)::float8 AS tf
			FROM cand, unnest(cand.tsv) u
			WHERE u.lexeme = ANY($2)
		)
		SELECT tf.id,
		       sum(q.idf * tf.tf * ($7 + 1) / (tf.tf + $7 * (1 - $8 + $8 * tf.ntokens / v.avg_tokens))) AS score,
		       sum(q.idf) AS matched
		FROM tf JOIN q ON q.term = tf.lexeme, v
		GROUP BY tf.id
		ORDER BY score DESC, tf.id
		LIMIT $5`,
		o.VersionID, terms, strings.Join(terms, " | "), kinds(o), candidates, nameWeight, bm25K1, bm25B)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (scored, error) {
		var s scored
		var matched float64
		err := row.Scan(&s.id, &s.score, &matched)
		if total > 0 {
			s.coverage = matched / total
		}
		return s, err
	})
}

// symbols finds chunks whose qualified name or symbol equals an identifier
// from the question, best matches first: an exact qualified name (Driver.New)
// before a bare symbol, code before tests and docs.
func (r *Retriever) symbols(ctx context.Context, o Options, idents []string) ([]string, error) {
	if len(idents) == 0 {
		return nil, nil
	}
	lower := make([]string, len(idents))
	for i, s := range idents {
		lower[i] = strings.ToLower(s)
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT id FROM chunks
		WHERE version_id = $1
		  AND (lower(qualname) = ANY($2) OR lower(symbol) = ANY($2) OR path = ANY($3))
		  AND (cardinality($4::text[]) = 0 OR kind = ANY($4))
		ORDER BY (lower(qualname) = ANY($2)) DESC, (kind = 'code') DESC, id
		LIMIT $5`, o.VersionID, lower, idents, kinds(o), candidates)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Retriever) vector(ctx context.Context, o Options, question string) ([]scored, error) {
	vecs, err := r.Embedder.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT c.id, 1 - (e.embedding <=> $3) AS cosine FROM chunks c
		JOIN embeddings e ON e.content_hash = c.content_hash AND e.model = $2
		WHERE c.version_id = $1 AND (cardinality($4::text[]) = 0 OR c.kind = ANY($4))
		ORDER BY e.embedding <=> $3, c.id
		LIMIT $5`, o.VersionID, r.Model, pgvector.NewVector(vecs[0]), kinds(o), candidates)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (scored, error) {
		var s scored
		err := row.Scan(&s.id, &s.score)
		return s, err
	})
}

func (r *Retriever) load(ctx context.Context, versionID int64, ids []string) ([]Hit, error) {
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT id, kind, path, coalesce(qualname, ''), start_line, end_line, content
		FROM chunks WHERE version_id = $1 AND id = ANY($2)`, versionID, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.Kind, &h.Path, &h.Qualname, &h.StartLine, &h.EndLine, &h.Content); err != nil {
			rows.Close()
			return nil, err
		}
		byID[h.ID] = h
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(ids))
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		}
	}
	return out, nil
}

func kinds(o Options) []string {
	if o.Kinds == nil {
		return []string{}
	}
	return o.Kinds
}

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	// identLike matches words that are clearly identifiers rather than
	// prose: camelCase or PascalCase with an inner capital, snake_case,
	// dotted names (Driver.New) and file paths.
	identLike = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+\b|\b[a-z]+[A-Z][A-Za-z0-9]*\b|\b[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*\b|\b[A-Za-z0-9]+_[A-Za-z0-9_]+\b|\b[\w.-]+/[\w./-]+\b`)
)

// Identifiers extracts the names in a question that should be looked up
// exactly (PRD section 9, stage 1).
func Identifiers(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.Trim(s, ".,;:?!()")
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range backticked.FindAllStringSubmatch(q, -1) {
		add(m[1])
	}
	for _, m := range identLike.FindAllString(q, -1) {
		add(m)
		// Also try the last element of a dotted name or path, so
		// "evm.NewDriver" finds NewDriver.
		if i := strings.LastIndexAny(m, "./"); i >= 0 && i+1 < len(m) {
			add(m[i+1:])
		}
	}
	return out
}
