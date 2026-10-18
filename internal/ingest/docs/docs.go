// Package docs splits Markdown by heading (levels 1 to 3), keeping the full
// heading path on every chunk (PRD section 8, "Docs chunker").
package docs

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

const (
	maxHeadingLevel = 3
	// maxSectionTokens is the size above which a section is split by block.
	maxSectionTokens = 800
)

type heading struct {
	line  int // 1-based
	level int
	title string
}

// Chunk splits one Markdown file into chunks.
func Chunk(path string, src []byte) []chunk.Chunk {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	heads := findHeadings(src)

	type section struct {
		start, end int // 1-based, inclusive
		headPath   string
	}
	var sections []section
	if first := firstHeadingLine(heads, len(lines)+1); first > 1 {
		sections = append(sections, section{1, first - 1, ""})
	}
	var stack []heading
	for i, h := range heads {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, h)
		end := len(lines)
		if i+1 < len(heads) {
			end = heads[i+1].line - 1
		}
		titles := make([]string, len(stack))
		for j, s := range stack {
			titles[j] = s.title
		}
		sections = append(sections, section{h.line, end, strings.Join(titles, " > ")})
	}

	var out []chunk.Chunk
	for _, s := range sections {
		for _, r := range splitSection(lines, s.start, s.end) {
			body := strings.Join(lines[r[0]-1:r[1]], "\n")
			if strings.TrimSpace(body) == "" {
				continue
			}
			c := chunk.Chunk{
				Kind:        chunk.KindDoc,
				Path:        path,
				HeadingPath: s.headPath,
				StartLine:   r[0],
				EndLine:     r[1],
				Content:     body + "\n",
			}
			c.EmbedText = fmt.Sprintf("file: %s\nsection: %s\n\n%s", path, s.headPath, c.Content)
			c.SetKey(fmt.Sprintf("doc:%s:%s", path, s.headPath))
			out = append(out, c)
		}
	}
	return out
}

// findHeadings uses goldmark so that '#' lines inside code fences and HTML
// blocks are not mistaken for headings.
func findHeadings(src []byte) []heading {
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	var out []heading
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok || h.Level > maxHeadingLevel || h.Lines().Len() == 0 {
			continue
		}
		seg := h.Lines().At(0)
		line := 1 + strings.Count(string(src[:seg.Start]), "\n")
		out = append(out, heading{line: line, level: h.Level, title: strings.TrimSpace(string(seg.Value(src)))})
	}
	return out
}

func firstHeadingLine(heads []heading, fallback int) int {
	if len(heads) == 0 {
		return fallback
	}
	return heads[0].line
}

// splitSection returns the section as one range, or, if it is longer than
// maxSectionTokens, as consecutive groups of blocks with one block of
// overlap. Blocks are blank-line separated, and a fenced code block is always
// a single block so it is never cut.
func splitSection(lines []string, start, end int) [][2]int {
	if estimateTokens(lines[start-1:end]) <= maxSectionTokens {
		return [][2]int{{start, end}}
	}
	blocks := blocksOf(lines, start, end)
	var out [][2]int
	i := 0
	for i < len(blocks) {
		j := i
		tokens := 0
		for j < len(blocks) {
			t := estimateTokens(lines[blocks[j][0]-1 : blocks[j][1]])
			if j > i && tokens+t > maxSectionTokens {
				break
			}
			tokens += t
			j++
		}
		out = append(out, [2]int{blocks[i][0], blocks[j-1][1]})
		if j >= len(blocks) {
			break
		}
		// Start the next group on the last block of this one (overlap),
		// unless that would make no progress.
		if j-1 > i {
			i = j - 1
		} else {
			i = j
		}
	}
	return out
}

func blocksOf(lines []string, start, end int) [][2]int {
	var blocks [][2]int
	inFence := false
	fence := ""
	blockStart := 0
	for n := start; n <= end; n++ {
		l := strings.TrimSpace(lines[n-1])
		if f := fenceMarker(l); f != "" {
			if !inFence {
				inFence, fence = true, f
			} else if strings.HasPrefix(l, fence) {
				inFence = false
			}
		}
		if l == "" && !inFence {
			if blockStart != 0 {
				blocks = append(blocks, [2]int{blockStart, n - 1})
				blockStart = 0
			}
			continue
		}
		if blockStart == 0 {
			blockStart = n
		}
	}
	if blockStart != 0 {
		blocks = append(blocks, [2]int{blockStart, end})
	}
	return blocks
}

func fenceMarker(l string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(l, f) {
			return f
		}
	}
	return ""
}

// estimateTokens uses the usual four characters per token approximation.
func estimateTokens(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	return n / 4
}
