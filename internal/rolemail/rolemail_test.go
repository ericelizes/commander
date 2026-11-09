package rolemail

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/store"
)

type fakeStore struct{ m map[string]string }

func (f *fakeStore) GetAmAgent(_ context.Context, k, r string) (string, error) { return f.m[r], nil }
func (f *fakeStore) UpsertAmAgent(_ context.Context, k, r, n string) error     { f.m[r] = n; return nil }
func (f *fakeStore) ListAmAgents(_ context.Context, k string) ([]store.AmAgent, error) {
	var out []store.AmAgent
	for r, n := range f.m {
		out = append(out, store.AmAgent{Role: r, AmName: n})
	}
	return out, nil
}

func TestRegisteredMatchesManagerSpellings(t *testing.T) {
	st := &fakeStore{m: map[string]string{"manager-app-596t.6": "RedFox"}}
	for _, in := range []string{"manager-app-596t.6", "manager-app-596t-6"} {
		reg, name, err := Registered(context.Background(), st, "k", in)
		if err != nil || reg != "manager-app-596t.6" || name != "RedFox" {
			t.Fatalf("%s: %q %q %v", in, reg, name, err)
		}
	}
	if reg, _, _ := Registered(context.Background(), st, "k", "operator-app-596t-6"); reg != "" {
		t.Fatalf("operator matched a manager: %q", reg)
	}
	if n, err := Resolve(context.Background(), st, nil, "k", "manager-app-596t-6", false); err != nil || n != "RedFox" {
		t.Fatalf("resolve: %q %v", n, err)
	}
	if len(st.m) != 1 {
		t.Fatalf("second agent registered: %v", st.m)
	}
}
