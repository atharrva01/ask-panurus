// Command ask-panurus is the CLI for Ask Panurus (PRD section 10).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/embed"
	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/ingest"
)

// commitFile is written into a snapshot by scripts/snapshot.sh.
const commitFile = ".ask-panurus-commit"

// embedGroup is how many texts are embedded and stored per round trip, so an
// interrupted run keeps most of its work.
const embedGroup = 256

func main() {
	root := &cobra.Command{
		Use:          "ask-panurus",
		Short:        "Grounded, cited answers about the Panurus codebase",
		SilenceUsage: true,
	}
	root.AddCommand(indexCmd(), statusCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func dbFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "db", os.Getenv("ASK_PANURUS_DB"), "Postgres URL (env ASK_PANURUS_DB)")
}

func indexCmd() *cobra.Command {
	var (
		opts     ingest.Options
		out      string
		db, tei  string
		activate bool
	)
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Chunk a Panurus snapshot, load it into Postgres and embed it",
		Long: `Chunk a Panurus snapshot (made with scripts/snapshot.sh) into a new index
version. With --db the chunks are loaded into Postgres; with --tei every chunk
text without an embedding is embedded. The new version goes live only when it
is fully embedded; otherwise it is left ready for inspection.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if opts.Commit == "" {
				b, err := os.ReadFile(filepath.Join(opts.Root, commitFile))
				if err != nil {
					return fmt.Errorf("no --commit given and no %s in %s; create the snapshot with scripts/snapshot.sh", commitFile, opts.Root)
				}
				opts.Commit = strings.TrimSpace(string(b))
			}
			chunks, st, err := ingest.Run(opts)
			if err != nil {
				return err
			}
			printStats(cmd.OutOrStdout(), opts, st, len(chunks))
			if out != "" {
				if err := writeJSONL(out, chunks); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote    %s\n", out)
			}
			if db == "" {
				return nil
			}
			return load(ctx, cmd.OutOrStdout(), db, tei, activate, opts, st, chunks)
		},
	}
	cmd.Flags().StringVar(&opts.Root, "repo", "", "path to the Panurus snapshot (required)")
	cmd.Flags().StringVar(&opts.Commit, "commit", "", "commit SHA of the snapshot (default: read from the snapshot)")
	cmd.Flags().StringVar(&opts.Repo, "github-repo", "LFDT-Panurus/panurus", "GitHub owner/name used in citation links")
	cmd.Flags().BoolVar(&opts.IncludeTests, "tests", false, "also index _test.go files (P1)")
	cmd.Flags().StringVar(&out, "out", "", "also write the chunks as JSON lines to this file")
	dbFlag(cmd, &db)
	cmd.Flags().StringVar(&tei, "tei", os.Getenv("ASK_PANURUS_TEI"), "TEI embedding server URL (env ASK_PANURUS_TEI)")
	cmd.Flags().BoolVar(&activate, "activate", true, "make the version live once it is fully embedded")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func load(ctx context.Context, w io.Writer, db, tei string, activate bool, opts ingest.Options, st ingest.Stats, chunks []chunk.Chunk) error {
	store, err := index.Open(ctx, db)
	if err != nil {
		return err
	}
	defer store.Close()

	vid, err := store.CreateVersion(ctx, opts.Repo, opts.Commit, "ast")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		if ferr := store.Fail(context.WithoutCancel(ctx), vid); ferr != nil {
			return fmt.Errorf("%w (and marking version %d failed: %v)", err, vid, ferr)
		}
		return err
	}
	if err := store.InsertChunks(ctx, vid, chunks); err != nil {
		return fail(fmt.Errorf("insert chunks: %w", err))
	}
	fmt.Fprintf(w, "version  %d loaded (%d chunks)\n", vid, len(chunks))

	if tei == "" {
		fmt.Fprintln(w, "embed    skipped (no --tei); version left ready, not live")
		return store.Finish(ctx, vid, st, false)
	}
	if err := embedMissing(ctx, w, store, embed.New(tei), vid); err != nil {
		return fail(err)
	}
	if err := store.Finish(ctx, vid, st, activate); err != nil {
		return err
	}
	if activate {
		fmt.Fprintf(w, "live     version %d\n", vid)
	}
	return nil
}

func embedMissing(ctx context.Context, w io.Writer, store *index.Store, client *embed.Client, vid int64) error {
	info, err := client.Info(ctx)
	if err != nil {
		return err
	}
	pending, err := store.MissingEmbeddings(ctx, vid, info.ModelID)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "embed    %s: %d new texts (others cached)\n", info.ModelID, len(pending))
	start := time.Now()
	for i := 0; i < len(pending); i += embedGroup {
		group := pending[i:min(i+embedGroup, len(pending))]
		texts := make([]string, len(group))
		hashes := make([]string, len(group))
		for j, p := range group {
			texts[j], hashes[j] = p.Text, p.Hash
		}
		vecs, err := client.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed: %w", err)
		}
		if err := store.PutEmbeddings(ctx, info.ModelID, hashes, vecs); err != nil {
			return fmt.Errorf("store embeddings: %w", err)
		}
		done := i + len(group)
		rate := float64(done) / time.Since(start).Seconds()
		fmt.Fprintf(w, "         %d/%d (%.1f/s, about %s left)\n", done, len(pending), rate,
			(time.Duration(float64(len(pending)-done)/rate) * time.Second).Round(time.Second))
	}
	return nil
}

func statusCmd() *cobra.Command {
	var db string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List index versions and their embedding coverage",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := index.Open(cmd.Context(), db)
			if err != nil {
				return err
			}
			defer store.Close()
			vs, err := store.Versions(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%-4s %-9s %-12s %-7s %8s  %s\n", "ID", "STATUS", "COMMIT", "CHUNKER", "CHUNKS", "EMBEDDED")
			for _, v := range vs {
				var emb []string
				for m, n := range v.Embedded {
					emb = append(emb, fmt.Sprintf("%s %d/%d", m, n, v.Chunks))
				}
				sort.Strings(emb)
				fmt.Fprintf(w, "%-4d %-9s %-12.12s %-7s %8d  %s\n", v.ID, v.Status, v.Commit, v.Chunker, v.Chunks, strings.Join(emb, ", "))
			}
			return nil
		},
	}
	dbFlag(cmd, &db)
	return cmd
}

func writeJSONL(path string, chunks []chunk.Chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	for i := range chunks {
		if err := enc.Encode(&chunks[i]); err != nil {
			f.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func printStats(w io.Writer, opts ingest.Options, st ingest.Stats, n int) {
	fmt.Fprintf(w, "commit   %s\n", opts.Commit)
	fmt.Fprintf(w, "files    go=%d markdown=%d build=%d (generated skipped: %d, too large: %d)\n",
		st.Files["go"], st.Files["markdown"], st.Files["build"], st.Generated, len(st.TooLarge))
	var kinds []string
	for k, v := range st.Chunks {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(kinds)
	fmt.Fprintf(w, "chunks   %d (%s)\n", n, strings.Join(kinds, " "))
	fmt.Fprintf(w, "secrets  %d chunks dropped\n", len(st.SecretDrops))
	for _, s := range st.SecretDrops {
		fmt.Fprintf(w, "         %s\n", s)
	}
	fmt.Fprintf(w, "errors   %d files failed to parse\n", len(st.ParseErrors))
	for _, e := range st.ParseErrors {
		fmt.Fprintf(w, "         %s\n", e)
	}
}
