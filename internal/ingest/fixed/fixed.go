// Package fixed is the baseline chunker for ablation run A (PRD section 11):
// fixed windows of about 1,000 characters, cut only at line ends, with no
// structure, metadata or context header.
package fixed

import (
	"fmt"
	"strings"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

// Size is the target window in characters.
const Size = 1000

// Chunk splits one file into consecutive windows. A single line longer than
// Size becomes its own window.
func Chunk(path string, src []byte, kind chunk.Kind) []chunk.Chunk {
	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []chunk.Chunk
	start, size := 0, 0
	flush := func(end int) {
		body := strings.Join(lines[start:end], "")
		if strings.TrimSpace(body) != "" {
			c := chunk.Chunk{
				Kind:      kind,
				Path:      path,
				StartLine: start + 1,
				EndLine:   end,
				Content:   body,
				EmbedText: body,
			}
			c.SetKey(fmt.Sprintf("fixed:%s", path))
			out = append(out, c)
		}
		start, size = end, 0
	}
	for i, l := range lines {
		if i > start && size+len(l) > Size {
			flush(i)
		}
		size += len(l)
	}
	if start < len(lines) {
		flush(len(lines))
	}
	return out
}
