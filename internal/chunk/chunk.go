// Package chunk defines the unit of retrieval shared by every ingester, the
// index and the answer pipeline.
package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Kind is the source type of a chunk.
type Kind string

const (
	KindCode  Kind = "code"
	KindTest  Kind = "test"
	KindDoc   Kind = "doc"
	KindBuild Kind = "build"
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
)

// Chunk mirrors the chunk schema in PRD section 8.
//
// Content is what users see and what citations point at. EmbedText is
// Content plus a context header and is only ever used for retrieval.
type Chunk struct {
	ID          string   `json:"id"`
	Kind        Kind     `json:"kind"`
	Repo        string   `json:"repo"`
	CommitSHA   string   `json:"commit_sha"`
	Path        string   `json:"path"`
	Package     string   `json:"package,omitempty"`
	Symbol      string   `json:"symbol,omitempty"`
	Receiver    string   `json:"receiver,omitempty"`
	BuildTags   string   `json:"build_tags,omitempty"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	HeadingPath string   `json:"heading_path,omitempty"`
	DocComment  string   `json:"doc_comment,omitempty"`
	Imports     []string `json:"imports,omitempty"`
	Content     string   `json:"content"`
	EmbedText   string   `json:"embed_text"`
	ContentHash string   `json:"content_hash"`
	HasTypeInfo bool     `json:"has_type_info"`

	// key is the ID before an ordinal is appended; see AssignIDs.
	key string
}

// SetKey records the un-numbered identity of a chunk, for example
// "code:token/driver.go:(*Driver).New". AssignIDs turns it into a unique ID.
func (c *Chunk) SetKey(key string) { c.key = key }

// AssignIDs gives every chunk a unique ID of the form key#n, where n counts
// earlier chunks with the same key. This keeps repeated init functions,
// build-tag variants and sub-chunks of split functions distinct.
func AssignIDs(chunks []Chunk) {
	seen := make(map[string]int)
	for i := range chunks {
		key := chunks[i].key
		if key == "" {
			key = fmt.Sprintf("%s:%s:L%d", chunks[i].Kind, chunks[i].Path, chunks[i].StartLine)
		}
		chunks[i].ID = fmt.Sprintf("%s#%d", key, seen[key])
		seen[key]++
	}
}

// EstimateTokens uses the usual four characters per token approximation.
func EstimateTokens(s string) int { return len(s) / 4 }

// Cap splits a chunk whose content is larger than maxTokens into line
// windows, so no chunk is silently truncated by the embedding model. This is
// the last resort for units the structural chunkers cannot cut, such as a
// single huge Markdown table or string constant. Each part keeps the
// original context header and anything appended after the content.
func Cap(c Chunk, maxTokens int) []Chunk {
	if EstimateTokens(c.Content) <= maxTokens {
		return []Chunk{c}
	}
	header, trailer := "", ""
	if i := strings.Index(c.EmbedText, c.Content); i >= 0 {
		header, trailer = c.EmbedText[:i], c.EmbedText[i+len(c.Content):]
	}
	lines := strings.SplitAfter(c.Content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []Chunk
	start, size := 0, 0
	flush := func(end int) {
		p := c
		p.StartLine = c.StartLine + start
		p.EndLine = c.StartLine + end - 1
		p.Content = strings.Join(lines[start:end], "")
		p.EmbedText = header + p.Content + trailer
		out = append(out, p)
		start, size = end, 0
	}
	for i, l := range lines {
		t := EstimateTokens(l)
		if i > start && size+t > maxTokens {
			flush(i)
		}
		size += t
	}
	flush(len(lines))
	return out
}

// Hash returns the hex SHA-256 of s. The index compares hashes of EmbedText
// to decide which chunks need re-embedding.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// GitHubURL links to the chunk's lines at the indexed commit, so a citation
// never drifts when main moves on.
func (c *Chunk) GitHubURL() string {
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s#L%d-L%d", c.Repo, c.CommitSHA, c.Path, c.StartLine, c.EndLine)
}
