package beads

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestExactDescription(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `[]`, `{"id":"other","description":""}`, `[{"id":"demo","description":""},{"id":"demo","description":""}]`, `{"id":"demo","description":null}`, `{"id":"demo","description":7}`, `banner {"id":"demo","description":""}`, `{"id":"demo","description":""} trailing`} {
		c := &cmdClient{run: captureExec(new([]string), []byte(raw), nil, nil)}
		if _, err := c.ExactDescription(context.Background(), "/fixture", "demo"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"id":"demo","description":" \t\n"}`, `[{"id":"demo","description":" \t\n"}]`} {
		c := &cmdClient{run: captureExec(new([]string), []byte(raw), nil, nil)}
		got, err := c.ExactDescription(context.Background(), "/fixture", "demo")
		if err != nil || got != " \t\n" {
			t.Fatalf("%q %v", got, err)
		}
	}
}

// Synthetic values preserve the native bd show shape, including omitted
// description and revision/count fields. No production record is copied.
const nativeEmptyShow = `[{"id":"demo","title":"Example gate","status":"closed","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","labels":["gate","approver:user"],"dependent_count":0,"dependency_count":0,"comment_count":0,"revision":"123"}]`

func TestExactDescriptionNativeOmission(t *testing.T) {
	for _, tc := range []struct {
		name, show, version, query, want string
		unavailable, deny                bool
	}{
		{name: "native empty", query: `[{"id":"demo","description":""}]`},
		{name: "whitespace preserved", query: `[{"id":"demo","description":" \t\n"}]`, want: " \t\n"},
		{name: "padded preserved", query: `[{"id":"demo","description":" scope "}]`, want: " scope "},
		{name: "unknown version", version: "bd version 1.3.2", deny: true},
		{name: "partial", show: `[{"id":"demo"}]`, deny: true},
		{name: "object omission", show: `{"id":"demo"}`, deny: true},
		{name: "explicit null", show: `[{"id":"demo","description":null}]`, deny: true},
		{name: "query unavailable", unavailable: true, deny: true},
		{name: "query missing", query: `[{"id":"demo"}]`, deny: true},
		{name: "query null", query: `[{"id":"demo","description":null}]`, deny: true},
		{name: "query wrong id", query: `[{"id":"other","description":""}]`, deny: true},
		{name: "query empty", query: `[]`, deny: true},
		{name: "query ambiguous", query: `[{"id":"demo","description":""},{"id":"demo","description":""}]`, deny: true},
		{name: "query malformed", query: `[{`, deny: true},
		{name: "query wrong type", query: `[{"id":"demo","description":7}]`, deny: true},
		{name: "query banner", query: `banner []`, deny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &cmdClient{run: func(_ context.Context, dir, bin string, args ...string) ([]byte, []byte, error) {
				if dir != "/fixture" {
					t.Fatal(dir)
				}
				switch args[0] {
				case "show":
					raw := tc.show
					if raw == "" {
						raw = nativeEmptyShow
					}
					return []byte(raw), nil, nil
				case "--version":
					v := tc.version
					if v == "" {
						v = "bd version 1.3.1 (c1c4b642a)\n"
					}
					return []byte(v), nil, nil
				case "--readonly":
					if !reflect.DeepEqual(args, []string{"--readonly", "sql", "SELECT id, description FROM issues WHERE id = 'demo'", "--json"}) {
						t.Fatal(args)
					}
					if tc.unavailable {
						return nil, nil, fmt.Errorf("unavailable")
					}
					return []byte(tc.query), nil, nil
				default:
					t.Fatalf("unexpected command: %v", args)
					return nil, nil, nil
				}
			}}
			got, err := c.ExactDescription(context.Background(), "/fixture", "demo")
			if (err != nil) != tc.deny || (!tc.deny && got != tc.want) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
