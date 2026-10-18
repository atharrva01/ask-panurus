-- Statistics for BM25 keyword scoring. Postgres ts_rank has no inverse
-- document frequency, so it ranks any chunk whose path matches a common term
-- near the top; BM25 needs per-version document frequencies and lengths.

ALTER TABLE chunks ADD COLUMN ntokens int NOT NULL DEFAULT 0;

ALTER TABLE index_versions
    ADD COLUMN n_docs     int,
    ADD COLUMN avg_tokens real;

CREATE TABLE term_stats (
    version_id bigint NOT NULL REFERENCES index_versions (id) ON DELETE CASCADE,
    term       text   NOT NULL,
    df         int    NOT NULL,
    PRIMARY KEY (version_id, term)
);
