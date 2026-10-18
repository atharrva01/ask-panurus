// Package ingest walks a snapshot of the Panurus repository and produces the
// chunks for one indexed commit.
package ingest

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atharrva01/ask-panurus/internal/chunk"
	"github.com/atharrva01/ask-panurus/internal/ingest/build"
	"github.com/atharrva01/ask-panurus/internal/ingest/docs"
	"github.com/atharrva01/ask-panurus/internal/ingest/fixed"
	"github.com/atharrva01/ask-panurus/internal/ingest/gocode"
	"github.com/atharrva01/ask-panurus/internal/ingest/secrets"
)

const (
	// maxFileBytes skips anything that is unlikely to be hand-written source.
	maxFileBytes = 1 << 20
	// maxChunkTokens caps every chunk; see chunk.Cap.
	maxChunkTokens = 1500
)

// Options configures one ingestion run.
type Options struct {
	Root         string // directory holding the snapshot
	Repo         string // GitHub owner/name, used in citation links
	Commit       string // commit SHA the snapshot was taken at
	IncludeTests bool
	// Chunker is "ast" (the default) or "fixed", the run A baseline. Both
	// read exactly the same files.
	Chunker string
}

// Stats summarises a run so every index build is auditable.
type Stats struct {
	Files       map[string]int     `json:"files"`
	Chunks      map[chunk.Kind]int `json:"chunks"`
	Generated   int                `json:"generated_files_skipped"`
	TooLarge    []string           `json:"too_large_skipped,omitempty"`
	ParseErrors []string           `json:"parse_errors,omitempty"`
	SecretDrops []string           `json:"secret_drops,omitempty"`
}

// Run ingests the snapshot at opts.Root.
func Run(opts Options) ([]chunk.Chunk, Stats, error) {
	st := Stats{Files: map[string]int{}, Chunks: map[chunk.Kind]int{}}
	goDirs := map[string][]gocode.File{} // dir -> files
	modules := map[string]string{}       // dir containing go.mod -> module path
	var out []chunk.Chunk

	err := filepath.WalkDir(opts.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(opts.Root, abs)
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			if rel != "." && (secrets.SkipDir(name) || (strings.HasPrefix(name, ".") && name != ".github")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || secrets.SkipFile(rel) {
			return nil
		}
		isGo := strings.HasSuffix(name, ".go")
		isDoc := strings.HasSuffix(name, ".md")
		isBuild := build.IsBuildFile(rel)
		if !isGo && !isDoc && !isBuild {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			st.TooLarge = append(st.TooLarge, rel)
			return nil
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		if name == "go.mod" {
			if mod := modulePath(src); mod != "" {
				modules[path.Dir(rel)] = mod
			}
		}
		if opts.Chunker == "fixed" {
			out = append(out, fixedChunks(opts, &st, rel, src, isGo, isDoc)...)
			return nil
		}
		switch {
		case isGo:
			st.Files["go"]++
			goDirs[path.Dir(rel)] = append(goDirs[path.Dir(rel)], gocode.File{Path: rel, Src: src})
		case isDoc:
			st.Files["markdown"]++
			out = append(out, docs.Chunk(rel, src)...)
		default:
			st.Files["build"]++
			out = append(out, build.Chunk(rel, src)...)
		}
		return nil
	})
	if err != nil {
		return nil, st, fmt.Errorf("walk %s: %w", opts.Root, err)
	}

	dirs := make([]string, 0, len(goDirs))
	for dir := range goDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		res := gocode.ChunkDir(importPath(dir, modules), goDirs[dir], opts.IncludeTests)
		st.Generated += len(res.Generated)
		for _, e := range res.Errors {
			st.ParseErrors = append(st.ParseErrors, e.Error())
		}
		out = append(out, res.Chunks...)
	}

	var capped []chunk.Chunk
	for _, c := range out {
		capped = append(capped, chunk.Cap(c, maxChunkTokens)...)
	}

	var kept []chunk.Chunk
	for _, c := range capped {
		if hits := secrets.Find(c.Content); hits != nil {
			// Log by location and rule only, never by content.
			st.SecretDrops = append(st.SecretDrops, fmt.Sprintf("%s:%d-%d %v", c.Path, c.StartLine, c.EndLine, hits))
			continue
		}
		c.Repo, c.CommitSHA = opts.Repo, opts.Commit
		c.ContentHash = chunk.Hash(c.EmbedText)
		st.Chunks[c.Kind]++
		kept = append(kept, c)
	}
	chunk.AssignIDs(kept)
	return kept, st, nil
}

// fixedChunks applies the run A baseline to one file, skipping the same
// generated and test files the AST chunker skips.
func fixedChunks(opts Options, st *Stats, rel string, src []byte, isGo, isDoc bool) []chunk.Chunk {
	switch {
	case isGo:
		st.Files["go"]++
		isTest := strings.HasSuffix(rel, "_test.go")
		if (isTest && !opts.IncludeTests) || gocode.IsGenerated(rel, src) {
			if !isTest {
				st.Generated++
			}
			return nil
		}
		kind := chunk.KindCode
		if isTest {
			kind = chunk.KindTest
		}
		return fixed.Chunk(rel, src, kind)
	case isDoc:
		st.Files["markdown"]++
		return fixed.Chunk(rel, src, chunk.KindDoc)
	default:
		st.Files["build"]++
		return fixed.Chunk(rel, src, chunk.KindBuild)
	}
}

// importPath resolves a directory to its Go import path using the nearest
// enclosing go.mod. Panurus has several nested modules.
func importPath(dir string, modules map[string]string) string {
	for d := dir; ; d = path.Dir(d) {
		if mod, ok := modules[d]; ok {
			if d == dir {
				return mod
			}
			if d == "." {
				return mod + "/" + dir
			}
			return mod + "/" + strings.TrimPrefix(dir, d+"/")
		}
		if d == "." || d == "/" {
			return dir
		}
	}
}

func modulePath(gomod []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(gomod))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}
