// Package index writes chunks and embeddings to Postgres and manages index
// versions (PRD sections 6 and 12).
package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/tokenize"
	"github.com/atharrva01/ask-panurus/migrations"
)

// Store is the Postgres index.
type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to Postgres. The vector extension must exist before
// pgvector types can be registered, so Migrate is run on a plain connection
// first.
func Open(ctx context.Context, url string) (*Store, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	err = migrate(ctx, conn)
	conn.Close(ctx)
	if err != nil {
		return nil, err
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, c)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.Pool.Close() }

// migrate applies every embedded migration that has not been applied yet,
// in file name order, each in its own transaction. An advisory lock keeps
// two indexers from migrating at once.
func migrate(ctx context.Context, conn *pgx.Conn) error {
	const lockID = 7_320_001
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return err
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// CreateVersion starts a new index version in the building state.
func (s *Store) CreateVersion(ctx context.Context, repo, commit, chunker string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO index_versions (repo, commit_sha, chunker) VALUES ($1, $2, $3) RETURNING id`,
		repo, commit, chunker).Scan(&id)
	return id, err
}

var chunkColumns = []string{
	"version_id", "id", "kind", "path", "package", "symbol", "receiver", "qualname",
	"build_tags", "start_line", "end_line", "heading_path", "doc_comment", "imports",
	"content", "embed_text", "content_hash", "has_type_info", "name_tokens", "body_tokens", "ntokens",
}

// InsertChunks bulk-loads chunks into a version with COPY.
func (s *Store) InsertChunks(ctx context.Context, versionID int64, chunks []chunk.Chunk) error {
	rows := make([][]any, len(chunks))
	for i := range chunks {
		c := &chunks[i]
		q := Qualname(c)
		name := tokenize.Tokens(q + " " + c.Path)
		body := tokenize.Tokens(c.DocComment + " " + c.Content)
		rows[i] = []any{
			versionID, c.ID, string(c.Kind), c.Path, null(c.Package), null(c.Symbol), null(c.Receiver), null(q),
			null(c.BuildTags), c.StartLine, c.EndLine, null(c.HeadingPath), null(c.DocComment), c.Imports,
			c.Content, c.EmbedText, c.ContentHash, c.HasTypeInfo,
			strings.Join(name, " "), strings.Join(body, " "), len(name) + len(body),
		}
	}
	_, err := s.Pool.CopyFrom(ctx, pgx.Identifier{"chunks"}, chunkColumns, pgx.CopyFromRows(rows))
	return err
}

// ComputeTermStats records the document frequency of every keyword token in
// a version, and its document count and average length, for BM25.
func (s *Store) ComputeTermStats(ctx context.Context, versionID int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM term_stats WHERE version_id = $1`, versionID); err != nil {
			return err
		}
		// ts_stat takes the query as text; the version ID is an integer, so
		// formatting it in is safe.
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO term_stats (version_id, term, df)
			SELECT %d, word, ndoc FROM ts_stat('SELECT tsv FROM chunks WHERE version_id = %d')`, versionID, versionID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE index_versions v SET n_docs = s.n, avg_tokens = s.avg
			FROM (SELECT count(*) AS n, avg(ntokens)::real AS avg FROM chunks WHERE version_id = $1) s
			WHERE v.id = $1`, versionID)
		return err
	})
}

// VersionMeta returns the repository, commit and build time of a version.
func (s *Store) VersionMeta(ctx context.Context, id int64) (repo, commit string, finished time.Time, err error) {
	var f *time.Time
	err = s.Pool.QueryRow(ctx, `SELECT repo, commit_sha, finished_at FROM index_versions WHERE id = $1`, id).
		Scan(&repo, &commit, &f)
	if f != nil {
		finished = *f
	}
	return repo, commit, finished, err
}

// LiveVersion returns the ID of the live version.
func (s *Store) LiveVersion(ctx context.Context) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `SELECT id FROM index_versions WHERE status = 'live'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("no live index version; pass a version explicitly or activate one")
	}
	return id, err
}

// Qualname is the name a person would search for: Driver.New for methods,
// the heading path for docs, and the section name for build files.
func Qualname(c *chunk.Chunk) string {
	switch {
	case c.Kind == chunk.KindDoc:
		return c.HeadingPath
	case c.Receiver != "":
		r := strings.TrimLeft(c.Receiver, "*")
		if i := strings.IndexByte(r, '['); i >= 0 {
			r = r[:i]
		}
		return r + "." + c.Symbol
	default:
		return c.Symbol
	}
}

// Pending is a chunk text that has no embedding for a model yet.
type Pending struct {
	Hash string
	Text string
}

// MissingEmbeddings lists the distinct texts of a version that have no
// embedding for model. This is the whole of incremental re-embedding: a
// rebuild finds only the chunks whose text changed.
func (s *Store) MissingEmbeddings(ctx context.Context, versionID int64, model string) ([]Pending, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (c.content_hash) c.content_hash, c.embed_text
		FROM chunks c
		LEFT JOIN embeddings e ON e.content_hash = c.content_hash AND e.model = $2
		WHERE c.version_id = $1 AND e.content_hash IS NULL
		ORDER BY c.content_hash`, versionID, model)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Pending, error) {
		var p Pending
		err := r.Scan(&p.Hash, &p.Text)
		return p, err
	})
}

// PutEmbeddings stores vectors for the given hashes. Existing rows are left
// alone, so concurrent or repeated runs are harmless.
func (s *Store) PutEmbeddings(ctx context.Context, model string, hashes []string, vecs [][]float32) error {
	if len(hashes) != len(vecs) {
		return errors.New("hashes and vectors differ in length")
	}
	batch := &pgx.Batch{}
	for i := range hashes {
		batch.Queue(`INSERT INTO embeddings (content_hash, model, dims, embedding) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, hashes[i], model, len(vecs[i]), pgvector.NewVector(vecs[i]))
	}
	return s.Pool.SendBatch(ctx, batch).Close()
}

// Finish records a version's stats and marks it ready. With activate, it
// also becomes the live version and the previous live version is retired,
// in one transaction, so a reader never sees two versions or none.
func (s *Store) Finish(ctx context.Context, versionID int64, stats any, activate bool) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if activate {
			if _, err := tx.Exec(ctx, `UPDATE index_versions SET status = 'retired' WHERE status = 'live'`); err != nil {
				return err
			}
		}
		status := "ready"
		if activate {
			status = "live"
		}
		_, err := tx.Exec(ctx, `UPDATE index_versions SET status = $2, stats = $3, finished_at = now() WHERE id = $1`,
			versionID, status, b)
		return err
	})
}

// Activate makes a finished version live and retires the previous one, in
// one transaction.
func (s *Store) Activate(ctx context.Context, versionID int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM index_versions WHERE id = $1`, versionID).Scan(&status); err != nil {
			return fmt.Errorf("version %d: %w", versionID, err)
		}
		if status == "building" || status == "failed" {
			return fmt.Errorf("version %d is %s and cannot be activated", versionID, status)
		}
		if _, err := tx.Exec(ctx, `UPDATE index_versions SET status = 'retired' WHERE status = 'live'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE index_versions SET status = 'live' WHERE id = $1`, versionID)
		return err
	})
}

// Fail marks a version as failed. The live version is untouched.
func (s *Store) Fail(ctx context.Context, versionID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE index_versions SET status = 'failed', finished_at = now() WHERE id = $1`, versionID)
	return err
}

// Version is one row of the version listing.
type Version struct {
	ID       int64
	Commit   string
	Chunker  string
	Status   string
	Chunks   int64
	Embedded map[string]int64 // model -> chunks with an embedding
}

// Versions lists index versions, newest first, with embedding coverage.
func (s *Store) Versions(ctx context.Context) ([]Version, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT v.id, v.commit_sha, v.chunker, v.status,
		       (SELECT count(*) FROM chunks c WHERE c.version_id = v.id)
		FROM index_versions v ORDER BY v.id DESC`)
	if err != nil {
		return nil, err
	}
	vs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Version, error) {
		var v Version
		err := r.Scan(&v.ID, &v.Commit, &v.Chunker, &v.Status, &v.Chunks)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	for i := range vs {
		vs[i].Embedded = map[string]int64{}
		rows, err := s.Pool.Query(ctx, `
			SELECT e.model, count(*) FROM chunks c
			JOIN embeddings e ON e.content_hash = c.content_hash
			WHERE c.version_id = $1 GROUP BY e.model`, vs[i].ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var model string
			var n int64
			if err := rows.Scan(&model, &n); err != nil {
				rows.Close()
				return nil, err
			}
			vs[i].Embedded[model] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return vs, nil
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}
