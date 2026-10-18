-- Ask Panurus schema, Milestone 1: index versions, chunks, embeddings.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- One row per build of the index. A build is a commit plus a chunker
-- configuration, so ablation runs (for example fixed-size chunks, run A) can
-- sit next to the AST index for the same commit. Exactly one version is
-- live, and it is switched in one transaction.
CREATE TABLE index_versions (
    id           bigserial PRIMARY KEY,
    repo         text        NOT NULL,
    commit_sha   text        NOT NULL,
    chunker      text        NOT NULL,
    status       text        NOT NULL DEFAULT 'building'
                 CHECK (status IN ('building', 'ready', 'live', 'retired', 'failed')),
    stats        jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);

CREATE UNIQUE INDEX index_versions_one_live ON index_versions ((true)) WHERE status = 'live';

CREATE TABLE chunks (
    version_id    bigint  NOT NULL REFERENCES index_versions (id) ON DELETE CASCADE,
    id            text    NOT NULL,
    kind          text    NOT NULL,
    path          text    NOT NULL,
    package       text,
    symbol        text,
    receiver      text,
    qualname      text,   -- Driver.New, a heading path, or a make target
    build_tags    text,
    start_line    int     NOT NULL,
    end_line      int     NOT NULL,
    heading_path  text,
    doc_comment   text,
    imports       text[],
    content       text    NOT NULL,
    embed_text    text    NOT NULL,
    content_hash  text    NOT NULL,
    has_type_info boolean NOT NULL DEFAULT false,
    -- Code-aware tokens produced in Go (internal/tokenize): identifiers are
    -- kept whole and also split at camelCase and snake_case boundaries.
    name_tokens   text    NOT NULL DEFAULT '',
    body_tokens   text    NOT NULL DEFAULT '',
    tsv           tsvector GENERATED ALWAYS AS (
                      setweight(to_tsvector('simple', name_tokens), 'A') ||
                      setweight(to_tsvector('simple', body_tokens), 'B')
                  ) STORED,
    PRIMARY KEY (version_id, id)
);

CREATE INDEX chunks_tsv           ON chunks USING gin (tsv);
CREATE INDEX chunks_qualname      ON chunks (version_id, lower(qualname));
CREATE INDEX chunks_qualname_trgm ON chunks USING gin (lower(qualname) gin_trgm_ops);
CREATE INDEX chunks_path          ON chunks (version_id, path);
CREATE INDEX chunks_content_hash  ON chunks (content_hash);

-- Embeddings are keyed by the hash of the embedded text and the model, and
-- shared by all versions: a rebuild only embeds text it has not seen, and
-- several models can be compared side by side. Dimensions vary by model, so
-- the column is untyped. At about 10k chunks an exact scan is fast and keeps
-- eval recall free of approximate-index error, so there is no vector index
-- yet.
CREATE TABLE embeddings (
    content_hash text        NOT NULL,
    model        text        NOT NULL,
    dims         int         NOT NULL,
    embedding    vector      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_hash, model)
);
