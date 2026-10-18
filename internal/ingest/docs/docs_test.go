package docs

import (
	"strings"
	"testing"
)

const design = `Intro text before any heading.

# EVM Design

Overview.

## Spent Tracking

Tokens are marked spent.

` + "```bash" + `
# not a heading
make evm
` + "```" + `

### Details

More.

#### Too deep

Stays in Details.

## Finality

Done.
`

func TestHeadingPaths(t *testing.T) {
	cs := Chunk("docs/evm.md", []byte(design))
	var paths []string
	for _, c := range cs {
		paths = append(paths, c.HeadingPath)
	}
	want := []string{
		"",
		"EVM Design",
		"EVM Design > Spent Tracking",
		"EVM Design > Spent Tracking > Details",
		"EVM Design > Finality",
	}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %q\nwant    %q", paths, want)
	}
	spent := cs[2]
	if !strings.Contains(spent.Content, "# not a heading") {
		t.Error("code fence content lost or treated as heading")
	}
	if spent.StartLine != 7 || spent.EndLine != 15 {
		t.Errorf("spent lines = %d-%d, want 7-15", spent.StartLine, spent.EndLine)
	}
	if !strings.Contains(cs[3].Content, "Stays in Details.") {
		t.Error("level-4 heading should stay inside its level-3 section")
	}
}

func TestLongSectionSplitKeepsFencesIntact(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Big\n\n")
	para := strings.Repeat("word ", 120) // about 150 tokens
	for i := 0; i < 12; i++ {
		b.WriteString(para + "\n\n")
		if i == 5 {
			b.WriteString("```go\nfunc a() {}\n\nfunc b() {}\n```\n\n")
		}
	}
	cs := Chunk("big.md", []byte(b.String()))
	if len(cs) < 2 {
		t.Fatalf("got %d chunks, want a split", len(cs))
	}
	for i, c := range cs {
		if strings.Count(c.Content, "```")%2 != 0 {
			t.Errorf("chunk %d cuts a code fence", i)
		}
		if c.HeadingPath != "Big" {
			t.Errorf("chunk %d heading path = %q", i, c.HeadingPath)
		}
		if i > 0 && c.StartLine > cs[i-1].EndLine {
			t.Errorf("chunk %d does not overlap the previous one", i)
		}
	}
}
