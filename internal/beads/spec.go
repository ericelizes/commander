package beads

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// SpecHash fingerprints what a bead asks for: its title, description and
// acceptance criteria and nothing else. A decision card records it when it is
// raised; comments, labels, waits, owner and edges never move it, so only an
// edit of the spec itself makes a card out of date.
func SpecHash(b Bead) string {
	h := sha1.New()
	for _, part := range []string{b.Title, b.Description, b.AcceptanceCriteria} {
		h.Write([]byte(strings.TrimSpace(part)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
