package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// inheritingExec simulates upstream bd: a create under --parent leaves the
// child with the parent's labels plus the asked ones; update --remove-label
// drops them.
func inheritingExec(parent []string, calls *[][]string, childLabels *[]string) execFunc {
	return func(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		switch args[0] {
		case "create":
			*childLabels = append([]string{}, parent...)
			for i, a := range args {
				if a == "--labels" {
					*childLabels = append(*childLabels, strings.Split(args[i+1], ",")...)
				}
			}
			return []byte("p-new\n"), nil, nil
		case "show":
			return []byte(`[{"id":"p-new","labels":["` + strings.Join(*childLabels, `","`) + `"]}]`), nil, nil
		case "update":
			for i, a := range args {
				if a == "--remove-label" {
					var kept []string
					for _, l := range *childLabels {
						if l != args[i+1] {
							kept = append(kept, l)
						}
					}
					*childLabels = kept
				}
			}
		}
		return nil, nil, nil
	}
}

func TestCreate_NoInheritStripsParentState(t *testing.T) {
	var calls [][]string
	var labels []string
	parent := []string{"blocked", "owner:manager-x", "waiting:1:review", "wait-checked:2", "kind:objective", "kind:track", "hold", "parked", "gate", "auto-respawn:1", "decision:answered", "needs-user", "area:infra"}
	c := &cmdClient{run: inheritingExec(parent, &calls, &labels)}

	_, err := c.Create(context.Background(), "/p", "t", CreateOptions{
		Parent: "p-step", Labels: []string{"kind:task", "owner:operator-a"}, Silent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(labels, ",")
	if want := "area:infra,kind:task,owner:operator-a"; got != want {
		t.Errorf("labels = %s, want %s", got, want)
	}
	updates := 0
	for _, call := range calls {
		if call[1] == "update" {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("update calls = %d, want 1", updates)
	}
}

func TestCreate_NoInheritKeepsAskedTrack(t *testing.T) {
	var calls [][]string
	var labels []string
	c := &cmdClient{run: inheritingExec([]string{"kind:objective", "blocked"}, &calls, &labels)}

	_, err := c.Create(context.Background(), "/p", "t", CreateOptions{
		Parent: "p-ws", Labels: []string{"kind:objective", "kind:track"}, Silent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(labels, ","), "kind:objective,kind:objective,kind:track"; got != want {
		t.Errorf("labels = %s, want %s", got, want)
	}
}

func TestCreate_SilentReturnsIDDirectly(t *testing.T) {
	var got []string
	c := &cmdClient{run: func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if args[0] == "create" && args[1] == "--help" {
			return nil, nil, nil
		}
		if args[0] == "show" {
			return []byte(`[{"id":"bd-XYZ","labels":["kind:operational"]}]`), nil, nil
		}
		return captureExec(&got, []byte("bd-XYZ\n"), nil, nil)(ctx, dir, name, args...)
	}}

	id, err := c.Create(context.Background(), "/code/p", "analyze requirements", CreateOptions{
		Parent: "bd-task",
		Labels: []string{"kind:operational"},
		Silent: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "bd-XYZ" {
		t.Errorf("id = %q, want bd-XYZ", id)
	}

	want := []string{
		"br", "create", "analyze requirements",
		"--parent", "bd-task",
		"--labels", "kind:operational",
		"--silent",
		"cwd=/code/p",
	}
	assertArgv(t, got, want)
}

func TestCreate_NonSilentParsesCreatedLine(t *testing.T) {
	out := "Created issue: bd-7Q2 — analyze requirements\n"
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte(out), nil, nil)}

	id, err := c.Create(context.Background(), "/code/p", "analyze requirements", CreateOptions{
		Labels:   []string{"kind:task", "priority:high"},
		Priority: 1,
		Type:     "feature",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "bd-7Q2" {
		t.Errorf("id = %q, want bd-7Q2", id)
	}

	want := []string{
		"br", "create", "analyze requirements",
		"--labels", "kind:task,priority:high",
		"--priority", "1",
		"--type", "feature",
		"cwd=/code/p",
	}
	assertArgv(t, got, want)
}

func TestCreate_NonSilentToleratesAsciiHyphen(t *testing.T) {
	// Some terminals/encodings may render the em-dash as ASCII " - ".
	out := "Created issue: bd-XYZ - analyze requirements\n"
	c := &cmdClient{run: captureExec(new([]string), []byte(out), nil, nil)}

	id, err := c.Create(context.Background(), "/code/p", "analyze requirements", CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "bd-XYZ" {
		t.Errorf("id = %q, want bd-XYZ", id)
	}
}

func TestCreate_RejectsEmptyTitle(t *testing.T) {
	c := &cmdClient{run: func(_ context.Context, _, _ string, _ ...string) ([]byte, []byte, error) {
		t.Fatal("exec should not run with empty title")
		return nil, nil, nil
	}}
	if _, err := c.Create(context.Background(), "/code/p", "", CreateOptions{}); err == nil {
		t.Error("expected error for empty title")
	}
}

func TestCreate_UnparseableOutputErrors(t *testing.T) {
	c := &cmdClient{run: captureExec(new([]string), []byte("garbage\n"), nil, nil)}
	if _, err := c.Create(context.Background(), "/code/p", "x", CreateOptions{}); err == nil {
		t.Error("expected parse error when 'Created issue:' marker absent")
	}
}

func TestParseCreatedID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Created issue: bd-1 — title", "bd-1"},
		{"Created issue: bd-2 – title", "bd-2"},
		{"Created issue: bd-3 - title", "bd-3"},
		{"Created issue: bd-4", "bd-4"},
		{"  Created issue: bd-5  ", "bd-5"},
		{"prefix\nCreated issue: bd-6 — t", "bd-6"},
		{"no marker here", ""},
	}
	for _, tc := range cases {
		got := parseCreatedID(tc.in)
		if got != tc.want {
			t.Errorf("parseCreatedID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCreate_DescriptionPassesBody(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte("bd-1\n"), nil, nil)}
	if _, err := c.Create(context.Background(), "/code/p", "t", CreateOptions{Description: "do the thing", Silent: true}); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, []string{"br", "create", "t", "--description", "do the thing", "--silent", "cwd=/code/p"})
}

// helpExec answers `create --help` with the given text and records the rest.
func helpExec(help string, calls *[][]string, createdLabels []string, updateErr error) execFunc {
	return func(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case args[0] == "create" && args[1] == "--help":
			return []byte(help), nil, nil
		case args[0] == "create":
			return []byte("p-new\n"), nil, nil
		case args[0] == "show":
			return []byte(`[{"id":"p-new","labels":["` + strings.Join(createdLabels, `","`) + `"]}]`), nil, nil
		case args[0] == "update":
			return nil, []byte("boom"), updateErr
		}
		return nil, nil, nil
	}
}

func hasCall(calls [][]string, verb string) int {
	n := 0
	for _, c := range calls {
		if c[1] == verb && !(len(c) > 2 && c[2] == "--help") {
			n++
		}
	}
	return n
}

func TestCreate_NoInheritFlagUsedWhenHelpHasIt(t *testing.T) {
	t.Setenv("COMMANDER_BEADS_BIN", "bd-flag-test")
	var calls [][]string
	c := &cmdClient{run: helpExec("Flags:\n  --no-inherit-labels   Don't inherit\n", &calls, []string{"blocked"}, nil)}
	for i := 0; i < 2; i++ {
		if _, err := c.Create(context.Background(), "/p", "t", CreateOptions{Parent: "p-ws", Silent: true}); err != nil {
			t.Fatal(err)
		}
	}
	flagged := 0
	for _, call := range calls {
		if call[1] == "create" && call[2] != "--help" {
			for _, a := range call {
				if a == "--no-inherit-labels" {
					flagged++
				}
			}
		}
	}
	if flagged != 2 {
		t.Errorf("creates with the flag = %d, want 2", flagged)
	}
	if n := hasCall(calls, "show") + hasCall(calls, "update"); n != 0 {
		t.Errorf("strip ran %d calls despite the flag", n)
	}
	help := 0
	for _, call := range calls {
		if call[1] == "create" && call[2] == "--help" {
			help++
		}
	}
	if help != 1 {
		t.Errorf("help probes = %d, want 1 (cached)", help)
	}
}

func TestCreate_NoInheritFlagOmittedWhenHelpLacksIt(t *testing.T) {
	t.Setenv("COMMANDER_BEADS_BIN", "br-flag-test")
	var calls [][]string
	c := &cmdClient{run: helpExec("Flags:\n  --silent\n", &calls, []string{"blocked"}, nil)}
	if _, err := c.Create(context.Background(), "/p", "t", CreateOptions{Parent: "p-ws", Silent: true}); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		for _, a := range call {
			if a == "--no-inherit-labels" {
				t.Errorf("flag passed to a binary without it: %v", call)
			}
		}
	}
	if hasCall(calls, "update") != 1 {
		t.Errorf("strip fallback did not run")
	}
}

func TestCreate_NoInheritFailedStripReturnsIDAndRetriesOnce(t *testing.T) {
	t.Setenv("COMMANDER_BEADS_BIN", "br-strip-fail")
	var calls [][]string
	c := &cmdClient{run: helpExec("", &calls, []string{"blocked"}, errors.New("exit 1"))}
	id, err := c.Create(context.Background(), "/p", "t", CreateOptions{Parent: "p-ws", Silent: true})
	if err == nil {
		t.Fatal("want the strip error")
	}
	if id != "p-new" {
		t.Errorf("id = %q, want p-new returned with the error", id)
	}
	if n := hasCall(calls, "update"); n != 2 {
		t.Errorf("update attempts = %d, want 2 (one retry)", n)
	}
}

func TestCreate_NoInheritStripsMoreState(t *testing.T) {
	t.Setenv("COMMANDER_BEADS_BIN", "br-more-state")
	var calls [][]string
	var labels []string
	parent := []string{"gate-status:failed", "superseded", "retired", "max-operators:3", "viz:1", "prod-gate:x", "review", "parked-until:5", "area:x"}
	c := &cmdClient{run: inheritingExec(parent, &calls, &labels)}
	if _, err := c.Create(context.Background(), "/p", "t", CreateOptions{Parent: "p-ws", Silent: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(labels, ","); got != "area:x" {
		t.Errorf("labels = %s, want area:x", got)
	}
}
