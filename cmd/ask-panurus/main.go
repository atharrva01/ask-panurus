// Command ask-panurus is the CLI for Ask Panurus (PRD section 10).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/atharrva01/ask-panurus/internal/ingest"
)

// commitFile is written into a snapshot by scripts/snapshot.sh.
const commitFile = ".ask-panurus-commit"

func main() {
	root := &cobra.Command{
		Use:          "ask-panurus",
		Short:        "Grounded, cited answers about the Panurus codebase",
		SilenceUsage: true,
	}
	root.AddCommand(indexCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func indexCmd() *cobra.Command {
	var opts ingest.Options
	var out string
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Chunk a Panurus snapshot and write the chunks as JSON lines",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			f, err := os.Create(out)
			if err != nil {
				return err
			}
			w := bufio.NewWriter(f)
			enc := json.NewEncoder(w)
			for i := range chunks {
				if err := enc.Encode(&chunks[i]); err != nil {
					f.Close()
					return err
				}
			}
			if err := w.Flush(); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			printStats(cmd, opts, st, len(chunks), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Root, "repo", "", "path to the Panurus snapshot (required)")
	cmd.Flags().StringVar(&opts.Commit, "commit", "", "commit SHA of the snapshot (default: read from the snapshot)")
	cmd.Flags().StringVar(&opts.Repo, "github-repo", "LFDT-Panurus/panurus", "GitHub owner/name used in citation links")
	cmd.Flags().BoolVar(&opts.IncludeTests, "tests", false, "also index _test.go files (P1)")
	cmd.Flags().StringVar(&out, "out", "chunks.jsonl", "output file")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func printStats(cmd *cobra.Command, opts ingest.Options, st ingest.Stats, n int, out string) {
	p := cmd.OutOrStdout()
	fmt.Fprintf(p, "commit   %s\n", opts.Commit)
	fmt.Fprintf(p, "files    go=%d markdown=%d build=%d (generated skipped: %d, too large: %d)\n",
		st.Files["go"], st.Files["markdown"], st.Files["build"], st.Generated, len(st.TooLarge))
	var kinds []string
	for k, v := range st.Chunks {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(kinds)
	fmt.Fprintf(p, "chunks   %d (%s) -> %s\n", n, strings.Join(kinds, " "), out)
	fmt.Fprintf(p, "secrets  %d chunks dropped\n", len(st.SecretDrops))
	for _, s := range st.SecretDrops {
		fmt.Fprintf(p, "         %s\n", s)
	}
	fmt.Fprintf(p, "errors   %d files failed to parse\n", len(st.ParseErrors))
	for _, e := range st.ParseErrors {
		fmt.Fprintf(p, "         %s\n", e)
	}
}
