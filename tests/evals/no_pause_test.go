package evals

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Nov 20 (4.1, RCM SharePoint move): the Commander put a ready step on hold
// and no agent ran, while the board read a quiet "Paused". A step is never
// paused: the real cmdr over the 4.1 shape shows the held step and the parked
// step as invalid states (red, in the sanity audit), while the legal shape
// beside them (an agent step blocked by the user's laptop step with its card)
// and a park with a time cmdrd ends are not flagged.
func TestNoPauseScenario(t *testing.T) {
	file, err := filepath.Abs("scenarios/no-pause.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	run := func(argv ...string) string {
		cmd := exec.Command(binary, argv...)
		cmd.Env = append(os.Environ(), "COMMANDER_HOME="+dir)
		out, _ := cmd.CombinedOutput() // graph check exits 1 on suspects: the text is what is judged
		return string(out)
	}
	workflow := run("board", "--snapshot", file, "--workflow", "w")
	for _, want := range []string{
		"Revise the test write to answer the review: Invalid state: on hold, so nothing will start it (w.6.18)",
		"Move the old folders: Invalid state: parked, so nothing will start it (w.6.21)",
		"Re-check after the weekend: Parked until",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("board --workflow missing %q:\n%s", want, workflow)
		}
	}
	for _, banned := range []string{"Check the test write landed: Invalid state", "Run the Modal test write from your laptop: Invalid state"} {
		if strings.Contains(workflow, banned) {
			t.Errorf("board --workflow flags the legal shape %q:\n%s", banned, workflow)
		}
	}
	audit := run("graph", "check", "--snapshot", file, "--audit")
	for _, want := range []string{"w.6.18", "w.6.21"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit missing %s:\n%s", want, audit)
		}
	}
	for _, banned := range []string{"w.6.22", "w.6.23"} {
		if strings.Contains(audit, banned) {
			t.Errorf("audit flags %s:\n%s", banned, audit)
		}
	}
}
