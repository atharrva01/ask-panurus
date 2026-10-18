# Ask Panurus

Ask questions about the [Panurus](https://github.com/LFDT-Panurus/panurus)
codebase and get answers grounded in its code and docs. Every answer cites
the exact files and lines it used, with links to GitHub at the indexed
commit. When the index does not cover a question, the service says so and
points to the closest sources instead of guessing.

The design, scope and milestones are in the
[product requirements document](Ask%20Panurus%20Product%20Requirements%20Document.md).
This repository is at Milestone 1: code, docs and build files at one pinned
commit.

## How it works

1. **Snapshot.** `scripts/snapshot.sh` exports Panurus at one commit with
   `git archive`, so the index and the eval set refer to exactly the same code.
2. **Chunk.** Go files are split by declaration with `go/ast` (large ones at
   about 150 lines), Markdown by heading, and Makefiles, workflows and protos
   by target, job or block. Files that look generated, or that contain
   secrets, are skipped.
3. **Index.** Chunks go into Postgres as a new index version. Each chunk gets
   a BM25 keyword entry with a code-aware tokenizer and an embedding from a
   local [TEI](https://github.com/huggingface/text-embeddings-inference)
   server (`jinaai/jina-embeddings-v2-base-code`). A version goes live only
   when it is fully embedded, and switching versions is atomic.
4. **Retrieve.** Hybrid search: exact symbol and path lookup, BM25 and vector
   similarity, fused with reciprocal rank fusion.
5. **Answer.** If the best match covers too little of the question, the
   service refuses before calling a model. Otherwise up to 8 chunks go to the
   LLM, and any answer without a valid citation is turned into a refusal.

## Eval results

There are 30 questions, all still drafts, at Panurus commit `6cf946a9b519`
(`eval/v1.yaml`): 21 answerable and scorable, 1 waiting for issue sources,
and 8 that should be refused. A hit is a retrieved chunk that overlaps a gold
line range.

| Run | Chunks | Retrieval | Recall@5 | Recall@10 | MRR |
|---|---|---|---|---|---|
| K-fixed-keyword | 1,000-char fixed | keyword | 57% | 81% | 0.42 |
| v4-ast-keyword | AST | keyword + symbols | 71% | 76% | 0.56 |
| v4-ast-vector | AST | vector | 48% | 62% | 0.27 |
| **v4-ast-hybrid** | AST | hybrid | **81%** | **86%** | 0.53 |

Refusals: in hybrid mode, refusing when coverage is below 0.674 refuses all 8
unanswerable questions, and also refuses 19% of answerable ones. The sample is
small, so treat these numbers as early signals. Full per-question reports are
in `eval/reports/`.

## Running it locally

You need Go, Docker and about 8 GB of RAM (TEI embedding needs about 4 GB
while indexing). Everything runs locally and costs nothing.

```sh
# Postgres (pgvector) and the embedding server
docker compose -f deploy/docker-compose.yml up -d postgres tei

export ASK_PANURUS_DB=postgres://ask:ask@localhost:5433/ask_panurus
export ASK_PANURUS_TEI=http://localhost:8081

# Build. The Anthropic SDK takes a lot of memory to compile; on a small
# machine, add GOGC=25 and -p 1.
go build -o ask-panurus ./cmd/ask-panurus
go build -o ask-panurus-server ./cmd/server

# Snapshot Panurus and index it (about 10,000 chunks, under 2 hours on CPU)
scripts/snapshot.sh ~/panurus 6cf946a9b519
./ask-panurus index --repo .data/panurus@6cf946a9b519
./ask-panurus status

# Search without an LLM
./ask-panurus search "Where is the fabtoken transfer validator defined?"
```

### Answers with a free local model

```sh
# After indexing, TEI can serve queries with less memory
TEI_MAX_BATCH_TOKENS=512 docker compose -f deploy/docker-compose.yml up -d tei

docker compose -f deploy/docker-compose.yml --profile llm up -d ollama
docker compose -f deploy/docker-compose.yml exec ollama ollama pull qwen2.5-coder:3b

./ask-panurus-server -addr 127.0.0.1:8090 -tei http://localhost:8081 \
  -llm openai -model qwen2.5-coder:3b

export ASK_PANURUS_SERVER=http://127.0.0.1:8090
./ask-panurus ask "Where is the fabtoken transfer validator defined?"
```

The web page is at http://127.0.0.1:8090. A 3B model on CPU takes a few
minutes per answer. Refusals take seconds, because they never reach the model.

Other providers:

- **Any OpenAI-compatible API**, such as a hosted free tier: use
  `-llm openai -llm-url <base URL> -model <id>`, and pass the key in
  `ASK_PANURUS_LLM_KEY`.
- **Claude:** use `-llm anthropic` and set `ANTHROPIC_API_KEY`.
- **Search only:** use `-llm none`. `/v1/ask` then returns 503.

### API

| Method | Path | |
|---|---|---|
| POST | `/v1/ask` | `{"question": "..."}`, returns the answer with citations or a refusal with pointers |
| POST | `/v1/search` | retrieval only |
| GET | `/v1/status` | indexed commit, index version and chunk counts |
| GET | `/healthz` | liveness |

Requests are rate limited per IP, and questions are limited to 1,000
characters.

## Development

```sh
ASK_PANURUS_TEST_DB=postgres://ask:ask@localhost:5433/postgres go test ./...

# Retrieval eval against an index version
python3 eval/harness/evalset.py eval/v1.yaml
python3 eval/harness/retrieval.py eval/v1.yaml --run my-run --mode hybrid \
  --tei http://localhost:8081
```

Tests that need Postgres each create their own throwaway database, and are
skipped when `ASK_PANURUS_TEST_DB` is not set.

| Path | |
|---|---|
| `cmd/ask-panurus` | CLI: index, search, status, activate, ask |
| `cmd/server` | HTTP API and web page |
| `internal/ingest` | walking, chunking, secret and generated-file filters |
| `internal/index` | Postgres store, index versions, embedding cache |
| `internal/retrieve` | BM25, symbol lookup, vector search, fusion |
| `internal/answer` | refusal gate, prompt, citation checks |
| `internal/llm` | Anthropic and OpenAI-compatible providers |
| `migrations/` | SQL schema |
| `eval/` | eval set, harness and reports |
