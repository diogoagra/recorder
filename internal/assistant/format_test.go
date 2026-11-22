package assistant

import (
	"fmt"
	"recorder/internal/storage"
	"strings"
	"testing"
)

func TestOffsetsRoundTrip(t *testing.T) {
	for _, c := range []struct {
		seconds float64
		text    string
	}{{0, "00:00"}, {754.9, "12:34"}, {3723, "1:02:03"}} {
		if got := FormatOffset(c.seconds); got != c.text {
			t.Errorf("FormatOffset(%v) = %q", c.seconds, got)
		}
		if got := parseOffset("[" + c.text + "]"); got != float64(int(c.seconds)) {
			t.Errorf("parseOffset(%q) = %v", c.text, got)
		}
	}
	for _, bad := range []string{"", "amanhã", "12", "12:75", "1:2:3:4"} {
		if got := parseOffset(bad); got != -1 {
			t.Errorf("parseOffset(%q) = %v, esperava -1", bad, got)
		}
	}
}

func TestChunkLinesKeepsEverythingWithinBudget(t *testing.T) {
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("[Pessoa %d]: %s\n", i%3+1, strings.Repeat("palavra ", 20)))
	}
	// Um monólogo que sozinho passa do limite
	lines = append(lines, "[Pessoa 1]: "+strings.Repeat("Uma frase longa. ", 100)+"\n")

	parts := chunkLines(lines, 1000)
	if len(parts) < 3 {
		t.Fatalf("esperava várias partes, veio %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 1000+1 {
			t.Fatalf("parte %d com %d caracteres, acima do limite", i, len(p))
		}
	}
	joined := strings.ReplaceAll(strings.Join(parts, ""), "\n", "")
	if joined != strings.ReplaceAll(strings.Join(lines, ""), "\n", "") {
		t.Fatal("as partes juntas deveriam ter todo o texto, na ordem")
	}
}

func TestRecentWithin(t *testing.T) {
	ts := []storage.Transcript{{Text: strings.Repeat("a", 100)}, {Text: strings.Repeat("b", 100)}, {Text: strings.Repeat("c", 100)}}
	got := recentWithin(ts, 250)
	if len(got) != 2 || got[0].Text[0] != 'b' {
		t.Fatalf("esperava as duas falas mais recentes, veio %d", len(got))
	}
}
