package chunk

import (
	"strings"
	"testing"
)

func TestCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString(strings.Repeat("x", 39) + "\n") // 10 tokens per line
	}
	c := Chunk{StartLine: 11, EndLine: 110, Content: b.String()}
	c.EmbedText = "// header\n" + c.Content + "// methods: A\n"

	parts := Cap(c, 250)
	if len(parts) != 4 {
		t.Fatalf("got %d parts, want 4", len(parts))
	}
	if parts[0].StartLine != 11 || parts[len(parts)-1].EndLine != 110 {
		t.Errorf("span %d-%d, want 11-110", parts[0].StartLine, parts[len(parts)-1].EndLine)
	}
	var joined strings.Builder
	for i, p := range parts {
		if i > 0 && p.StartLine != parts[i-1].EndLine+1 {
			t.Errorf("gap before part %d", i)
		}
		if strings.Count(p.Content, "\n") != p.EndLine-p.StartLine+1 {
			t.Errorf("part %d: content lines do not match range", i)
		}
		if !strings.HasPrefix(p.EmbedText, "// header\n") || !strings.HasSuffix(p.EmbedText, "// methods: A\n") {
			t.Errorf("part %d lost header or trailer", i)
		}
		joined.WriteString(p.Content)
	}
	if joined.String() != c.Content {
		t.Error("parts do not reassemble to the original content")
	}
	if got := Cap(Chunk{Content: "small\n"}, 250); len(got) != 1 {
		t.Errorf("small chunk split into %d", len(got))
	}
}
