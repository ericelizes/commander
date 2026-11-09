package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

// The real cmdr's internal-gate pass|rm|apply --no-pr against a real bd: a merge
// gate of a ship with no PR passes or goes only for the user, Commander or the
// owning Manager, with the approval recorded on the gate first; a pr: label or an
// Operator still refuses and changes nothing. No GitHub call is made (gh fails).
func TestGateNoPRScenario(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	raw, err := os.ReadFile("scenarios/gate-no-pr.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Cases []struct {
			Name, Verb, Role, NoPR, Evidence, StageLabel, GateLabel, Stderr, After string
			Exit, Notes                                                            int
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1")
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := beads.New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.InsertProject(ctx, store.Project{Name: "sample", Type: "code", Path: dir, Status: "active", ActivatedAt: 1})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	stub := t.TempDir()
	writeExecutable(t, filepath.Join(stub, "gh"), "#!/bin/sh\necho 'gh called in a no-PR scenario' >&2\nexit 1\n")
	bin := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	root, err := c.Create(ctx, dir, "Sample ship", beads.CreateOptions{Labels: []string{"kind:objective"}, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	run := func(role string, args ...string) (int, string) {
		t.Helper()
		cctx, cancel := context.WithTimeout(ctx, evalTimeout(t, 90*time.Second))
		defer cancel()
		cmd := exec.CommandContext(cctx, bin, args...)
		cmd.Dir = t.TempDir()
		cmd.Env = append(envWithout(os.Environ(), "COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE", "PATH"),
			"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "COMMANDER_ROLE="+role)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), strings.ReplaceAll(string(out), `\"`, `"`)
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			stage, err := c.Create(ctx, dir, "Deploy the sample service", beads.CreateOptions{Parent: root, Labels: []string{"kind:objective"}, Silent: true})
			if err != nil {
				t.Fatal(err)
			}
			if code, out := run("", "internal-gate", "add", "--project", "sample", "--bead", stage, "--name", "merged", "--check", "the deploy is live", "--verifier", "user"); code != 0 {
				t.Fatal(out)
			}
			ns, err := gates.ForStageFresh(ctx, c, dir, p, stage)
			if err != nil || len(ns) != 1 {
				t.Fatal(ns, err)
			}
			gate := ns[0].ID
			if tc.StageLabel != "" {
				must(t, c.SetLabel(ctx, dir, stage, tc.StageLabel, true))
			}
			if tc.GateLabel != "" {
				must(t, c.SetLabel(ctx, dir, gate, tc.GateLabel, true))
			}
			role := tc.Role
			if role == "owner" {
				role = "manager-" + root
			}
			args := []string{"internal-gate", tc.Verb, "--project", "sample", "--bead", stage}
			if tc.Verb == "apply" {
				args = append(args, "--template", "spec")
			} else {
				args = append(args, "--name", "merged")
			}
			if tc.NoPR != "" {
				args = append(args, "--no-pr", tc.NoPR)
			}
			if tc.Evidence != "" {
				args = append(args, "--evidence", tc.Evidence)
			}
			code, out := run(role, args...)
			t.Logf("exit %d: %s", code, strings.TrimSpace(out))
			if code != tc.Exit || (tc.Stderr != "" && !strings.Contains(out, tc.Stderr)) {
				t.Fatalf("exit %d, want %d with %q: %s", code, tc.Exit, tc.Stderr, out)
			}
			if strings.Contains(out, "gh called") {
				t.Fatal("a no-PR waiver read GitHub")
			}
			g, err := beads.FreshShow(ctx, c, dir, gate)
			if err != nil || g == nil {
				t.Fatal(g, err)
			}
			now, err := gates.ForStageFresh(ctx, c, dir, p, stage)
			if err != nil {
				t.Fatal(err)
			}
			merged := false
			for _, n := range now {
				merged = merged || n.Gate.Name == "merged"
			}
			switch tc.After {
			case "open":
				if g.Status == "closed" || !merged {
					t.Fatalf("the gate changed: %s %+v", g.Status, now)
				}
			case "closed":
				if g.Status != "closed" {
					t.Fatalf("gate %s", g.Status)
				}
			case "gone":
				if merged {
					t.Fatalf("the merged gate is still on the stage: %+v", now)
				}
			}
			cs, err := c.Comments(ctx, dir, gate)
			if err != nil {
				t.Fatal(err)
			}
			notes := 0
			for _, cm := range cs {
				if strings.HasPrefix(cm.Text, "merge rule waived for a ship with no PR by ") {
					notes++
					if !strings.Contains(cm.Text, tc.NoPR) || !strings.Contains(cm.Text, tc.Evidence) {
						t.Fatalf("the record misses the reason or approval: %s", cm.Text)
					}
				}
			}
			if notes != tc.Notes {
				t.Fatalf("%d waiver notes, want %d", notes, tc.Notes)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
