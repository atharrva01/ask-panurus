package fixed

import (
	"strings"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

func TestChunk(t *testing.T) {
	src := strings.Repeat(strings.Repeat("x", 99)+"\n", 25) // 2,500 chars, 25 lines
	cs := Chunk("a.go", []byte(src), chunk.KindCode)
	if len(cs) != 3 {
		t.Fatalf("got %d windows, want 3", len(cs))
	}
	var joined strings.Builder
	for i, c := range cs {
		if len(c.Content) > Size {
			t.Errorf("window %d is %d chars", i, len(c.Content))
		}
		if i > 0 && c.StartLine != cs[i-1].EndLine+1 {
			t.Errorf("gap before window %d", i)
		}
		joined.WriteString(c.Content)
	}
	if joined.String() != src || cs[2].EndLine != 25 {
		t.Error("windows do not cover the file exactly")
	}
}
