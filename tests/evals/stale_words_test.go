package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real board command against the same snapshot the app renders.
func TestStaleWordsScenario(t *testing.T) { runStaleWordsScenario(t, "scenarios/stale-words.json") }
func TestStaleDependenciesScenario(t *testing.T) {
	runStaleWordsScenario(t, "scenarios/stale-dependencies.json")
}
func runStaleWordsScenario(t *testing.T, scenario string) {
	raw, err := os.ReadFile(scenario)
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Fixture  string
		Contains []string
		Absent   []string
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(sc.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Snapshot json.RawMessage }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(file, fx.Snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, extra := range [][]string{nil, {"--diagram", "1"}} {
		argv := append([]string{"board", "--snapshot", file}, extra...)
		cmd := exec.Command(binary, argv...)
		cmd.Env = append(os.Environ(), "COMMANDER_HOME="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
		for _, want := range sc.Contains {
			if !strings.Contains(string(out), want) {
				t.Errorf("%v missing %q:\n%s", argv, want, out)
			}
		}
		for _, banned := range sc.Absent {
			if strings.Contains(string(out), banned) {
				t.Errorf("%v shows %q:\n%s", argv, banned, out)
			}
		}
	}
}
