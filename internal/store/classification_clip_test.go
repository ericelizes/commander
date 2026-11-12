package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipEvidenceStaysValidUTF8(t *testing.T) {
	text := strings.Repeat("→", 2500) + "\x00end"
	got := clipEvidence(text)
	if !utf8.ValidString(got) || strings.Contains(got, "\x00") || len(got) > 6000 || !strings.HasSuffix(got, "end") {
		t.Fatalf("clipped evidence: valid=%v len=%d", utf8.ValidString(got), len(got))
	}
}
