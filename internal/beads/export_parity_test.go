package beads

import (
	"context"
	"os"
	"reflect"
	"testing"
)

// TestExportParityWithBr compares ExportReads with br on a real project:
// PARITY_DIR=~/code/<project> go test ./internal/beads -run ExportParity -v
func TestExportParityWithBr(t *testing.T) {
	dir := os.Getenv("PARITY_DIR")
	if dir == "" {
		t.Skip()
	}
	ctx := context.Background()
	br := New()
	ex := ExportReads(br)
	opts := ListOptions{Labels: []string{"kind:objective"}, Status: "all"}
	a, err := br.List(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ex.List(ctx, dir, opts)
	diff := 0
	for i := range a {
		if i >= len(b) || !reflect.DeepEqual(a[i], b[i]) {
			diff++
			if diff < 4 {
				t.Logf("list[%d]\n br=%+v\n ex=%+v", i, a[i], b[min(i, len(b)-1)])
			}
		}
	}
	ids := make([]string, 0, len(a))
	for _, x := range a {
		ids = append(ids, x.ID)
	}
	da, err := br.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := ex.Details(ctx, dir, ids)
	ddiff := 0
	for id, x := range da {
		y := db[id]
		if x.Parent != y.Parent || len(x.Deps) != len(y.Deps) || len(x.Comments) != len(y.Comments) {
			ddiff++
			if ddiff < 4 {
				t.Logf("detail %s\n br=%+v\n ex=%+v", id, x, y)
			}
		}
	}
	t.Logf("list rows br=%d ex=%d differing=%d; details br=%d ex=%d differing=%d", len(a), len(b), diff, len(da), len(db), ddiff)
	if diff+ddiff > 0 || len(a) != len(b) || len(da) != len(db) {
		t.Fail()
	}
}
