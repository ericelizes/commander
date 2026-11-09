package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real cmdr board over a snapshot of work a close released, a create made
// and workstreams with no plan yet: none reads Nothing to do or State missing.
// cmdr board reads the clock, so the fixture's times move to now first.
func TestNeverUnheldScenario(t *testing.T) {
	var sc struct {
		Fixture  string
		Contains []string
		Diagram  []string
		Absent   []string
	}
	readJSON(t, "scenarios/never-unheld.json", &sc)
	var fx struct {
		Now      int64          `json:"now"`
		Snapshot map[string]any `json:"snapshot"`
	}
	readJSON(t, sc.Fixture, &fx)
	shiftTimes(fx.Snapshot, time.Now().Unix()-fx.Now)
	dir := t.TempDir()
	file := filepath.Join(dir, "snapshot.json")
	raw, _ := json.Marshal(fx.Snapshot)
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, run := range []struct {
		argv []string
		want []string
	}{
		{[]string{"board", "--snapshot", file}, sc.Contains},
		{[]string{"board", "--snapshot", file, "--diagram", "1"}, sc.Diagram},
	} {
		cmd := exec.Command(binary, run.argv...)
		cmd.Env = append(os.Environ(), "COMMANDER_HOME="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", run.argv, err, out)
		}
		for _, want := range run.want {
			if !strings.Contains(string(out), want) {
				t.Errorf("%v missing %q:\n%s", run.argv, want, out)
			}
		}
		for _, banned := range sc.Absent {
			if strings.Contains(string(out), banned) {
				t.Errorf("%v shows %q:\n%s", run.argv, banned, out)
			}
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// shiftTimes moves every unix time in a snapshot by d seconds.
func shiftTimes(v any, d int64) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			switch {
			case k == "waiting_untils":
				for kk, u := range e.(map[string]any) {
					e.(map[string]any)[kk] = u.(float64) + float64(d)
				}
			case k == "since" || strings.HasPrefix(k, "waiting_since") || k == "waiting_until":
				if f, ok := e.(float64); ok {
					x[k] = f + float64(d)
				}
			default:
				shiftTimes(e, d)
			}
		}
	case []any:
		for _, e := range x {
			shiftTimes(e, d)
		}
	}
}
