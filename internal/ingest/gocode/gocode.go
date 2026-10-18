// Package gocode turns Go source into one chunk per function, method, type
// and package-level const/var block (PRD section 8, "Go code chunker").
//
// Milestone 1 works from syntax alone (go/parser), one package directory at a
// time, so it never depends on the target module building. Type information
// from go/packages arrives with the symbol graph in Milestone 3.
package gocode

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

const (
	// maxDeclLines is the size above which a declaration is split.
	maxDeclLines = 150
	// targetPartLines is the size each part of a split declaration aims for.
	targetPartLines = 100
	// minTailLines stops a split from leaving a tiny last part.
	minTailLines = 20
)

// File is one Go source file in a package directory.
type File struct {
	Path string // repository-relative, slash separated
	Src  []byte
}

// Result is what ChunkDir produced for one directory.
type Result struct {
	Chunks    []chunk.Chunk
	Generated []string // paths skipped as generated code
	Errors    []error  // files that failed to parse; they are skipped
}

// ChunkDir chunks all files of one package directory. pkgPath is the import
// path of the directory. Test files are skipped unless includeTests is set.
func ChunkDir(pkgPath string, files []File, includeTests bool) Result {
	var res Result
	fset := token.NewFileSet()

	type parsed struct {
		file File
		ast  *ast.File
	}
	var keep []parsed
	for _, f := range files {
		isTest := strings.HasSuffix(f.Path, "_test.go")
		if isTest && !includeTests {
			continue
		}
		af, err := parser.ParseFile(fset, f.Path, f.Src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			res.Errors = append(res.Errors, err)
			continue
		}
		if ast.IsGenerated(af) {
			res.Generated = append(res.Generated, f.Path)
			continue
		}
		keep = append(keep, parsed{f, af})
	}

	// Method sets are collected across all files of the same package so a
	// type chunk can list methods declared in other files.
	methods := make(map[string]map[string][]string) // package name -> type -> methods
	for _, p := range keep {
		pkg := p.ast.Name.Name
		for _, d := range p.ast.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			base := baseTypeName(fd.Recv.List[0].Type)
			if methods[pkg] == nil {
				methods[pkg] = make(map[string][]string)
			}
			methods[pkg][base] = append(methods[pkg][base], fd.Name.Name)
		}
	}
	for _, byType := range methods {
		for _, m := range byType {
			sort.Strings(m)
		}
	}

	for _, p := range keep {
		fc := fileChunker{
			fset:    fset,
			tf:      fset.File(p.ast.Pos()),
			src:     p.file.Src,
			path:    p.file.Path,
			pkgPath: pkgPath,
			methods: methods[p.ast.Name.Name],
		}
		if strings.HasSuffix(p.ast.Name.Name, "_test") {
			fc.pkgPath += "_test"
		}
		fc.kind = chunk.KindCode
		if strings.HasSuffix(p.file.Path, "_test.go") {
			fc.kind = chunk.KindTest
		}
		fc.buildTags = buildTags(p.ast)
		fc.imports = imports(p.ast)
		res.Chunks = append(res.Chunks, fc.chunks(p.ast)...)
	}
	return res
}

type fileChunker struct {
	fset      *token.FileSet
	tf        *token.File
	src       []byte
	path      string
	pkgPath   string
	kind      chunk.Kind
	buildTags string
	imports   []string
	methods   map[string][]string
}

func (fc *fileChunker) chunks(f *ast.File) []chunk.Chunk {
	var out []chunk.Chunk
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			out = append(out, fc.funcChunks(d)...)
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				out = append(out, fc.typeChunks(d)...)
			case token.CONST, token.VAR:
				out = append(out, fc.valueChunks(d)...)
			}
		}
	}
	return out
}

func (fc *fileChunker) funcChunks(d *ast.FuncDecl) []chunk.Chunk {
	symbol, receiver := d.Name.Name, ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		receiver = fc.print(d.Recv.List[0].Type)
		symbol = baseTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
	}
	start, end := fc.declLines(d.Doc, d.Pos(), d.End())

	sig := *d
	sig.Doc, sig.Body = nil, nil
	signature := fc.print(&sig)

	var boundaries []int
	if d.Body != nil {
		for _, s := range d.Body.List {
			boundaries = append(boundaries, fc.line(s.End()))
		}
	}
	base := chunk.Chunk{
		Symbol:     d.Name.Name,
		Receiver:   receiver,
		DocComment: docText(d.Doc),
	}
	return fc.emit(base, symbol, signature, start, end, boundaries)
}

func (fc *fileChunker) typeChunks(d *ast.GenDecl) []chunk.Chunk {
	var out []chunk.Chunk
	grouped := d.Lparen.IsValid()
	for _, s := range d.Specs {
		ts := s.(*ast.TypeSpec)
		doc := ts.Doc
		pos, endPos := ts.Pos(), ts.End()
		if !grouped {
			doc, pos, endPos = d.Doc, d.Pos(), d.End()
		}
		start, end := fc.declLines(doc, pos, endPos)
		extra := ""
		if m := fc.methods[ts.Name.Name]; len(m) > 0 {
			extra = "// methods: " + strings.Join(m, ", ") + "\n"
		}
		base := chunk.Chunk{Symbol: ts.Name.Name, DocComment: docText(doc)}
		c := fc.emit(base, ts.Name.Name, "", start, end, nil)
		for i := range c {
			c[i].EmbedText += extra
		}
		out = append(out, c...)
	}
	return out
}

func (fc *fileChunker) valueChunks(d *ast.GenDecl) []chunk.Chunk {
	var names []string
	var boundaries []int
	for _, s := range d.Specs {
		vs := s.(*ast.ValueSpec)
		for _, n := range vs.Names {
			names = append(names, n.Name)
		}
		boundaries = append(boundaries, fc.line(vs.End()))
	}
	symbol := d.Tok.String() + " " + strings.Join(names, ", ")
	if len(names) > 4 {
		symbol = d.Tok.String() + " " + strings.Join(names[:4], ", ") + ", ..."
	}
	start, end := fc.declLines(d.Doc, d.Pos(), d.End())
	base := chunk.Chunk{Symbol: symbol, DocComment: docText(d.Doc)}
	return fc.emit(base, symbol, "", start, end, boundaries)
}

// emit builds one chunk for a declaration, or several sub-chunks if it is
// longer than maxDeclLines. Sub-chunks cover the declaration contiguously and
// each carries the signature in its embedded text.
func (fc *fileChunker) emit(base chunk.Chunk, key, signature string, start, end int, boundaries []int) []chunk.Chunk {
	parts := splitRanges(start, end, boundaries)
	out := make([]chunk.Chunk, 0, len(parts))
	for i, r := range parts {
		c := base
		c.Kind = fc.kind
		c.Path = fc.path
		c.Package = fc.pkgPath
		c.BuildTags = fc.buildTags
		c.Imports = fc.imports
		c.StartLine, c.EndLine = r[0], r[1]
		c.Content = fc.lines(r[0], r[1])

		var hdr strings.Builder
		fmt.Fprintf(&hdr, "// package %s\n// file %s\n// symbol %s\n", fc.pkgPath, fc.path, key)
		if fc.buildTags != "" {
			fmt.Fprintf(&hdr, "// build %s\n", fc.buildTags)
		}
		if len(parts) > 1 {
			fmt.Fprintf(&hdr, "// part %d of %d\n", i+1, len(parts))
			if i > 0 && signature != "" {
				hdr.WriteString(signature + "\n")
			}
		}
		c.EmbedText = hdr.String() + c.Content
		c.SetKey(fmt.Sprintf("%s:%s:%s", fc.kind, fc.path, key))
		out = append(out, c)
	}
	return out
}

// splitRanges splits the line range [start, end] at unit boundaries (the last
// line of each statement or spec) into parts of about targetPartLines. A unit
// longer than a part is never cut.
func splitRanges(start, end int, boundaries []int) [][2]int {
	if end-start+1 <= maxDeclLines || len(boundaries) < 2 {
		return [][2]int{{start, end}}
	}
	var parts [][2]int
	segStart := start
	for _, b := range boundaries {
		if b-segStart+1 >= targetPartLines && end-b >= minTailLines {
			parts = append(parts, [2]int{segStart, b})
			segStart = b + 1
		}
	}
	return append(parts, [2]int{segStart, end})
}

// declLines returns the first and last line of a declaration, including its
// doc comment.
func (fc *fileChunker) declLines(doc *ast.CommentGroup, pos, end token.Pos) (int, int) {
	if doc != nil {
		pos = doc.Pos()
	}
	return fc.line(pos), fc.line(end)
}

func (fc *fileChunker) line(p token.Pos) int { return fc.tf.Line(p) }

// lines returns the source text of lines a through b, inclusive.
func (fc *fileChunker) lines(a, b int) string {
	from := fc.tf.Offset(fc.tf.LineStart(a))
	to := len(fc.src)
	if b < fc.tf.LineCount() {
		to = fc.tf.Offset(fc.tf.LineStart(b + 1))
	}
	return strings.TrimRight(string(fc.src[from:to]), "\n") + "\n"
}

func (fc *fileChunker) print(node any) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fc.fset, node); err != nil {
		return ""
	}
	return buf.String()
}

// baseTypeName strips pointers and type parameters: *Store[K] -> Store.
func baseTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}

func buildTags(f *ast.File) string {
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if rest, ok := strings.CutPrefix(c.Text, "//go:build "); ok {
				return strings.TrimSpace(rest)
			}
		}
	}
	return ""
}

func imports(f *ast.File) []string {
	var out []string
	for _, s := range f.Imports {
		if p, err := strconv.Unquote(s.Path.Value); err == nil {
			out = append(out, p)
		}
	}
	return out
}
