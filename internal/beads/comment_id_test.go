package beads

import (
	"encoding/json"
	"testing"
)

func TestCommentIDFormats(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{`{"id":"synthetic-comment","text":"exact\nbytes"}`, "synthetic-comment"}, {`{"id":17}`, "17"}, {`{"text":"legacy"}`, ""}} {
		var c Comment
		if e := json.Unmarshal([]byte(tc.raw), &c); e != nil || c.ID != tc.want {
			t.Fatalf("%s: %+v %v", tc.raw, c, e)
		}
	}
	for _, raw := range []string{`{"id":1.5}`, `{"id":{}}`} {
		var c Comment
		if json.Unmarshal([]byte(raw), &c) == nil {
			t.Fatal("accepted invalid id")
		}
	}
}
