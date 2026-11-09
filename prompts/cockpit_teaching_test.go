package promptassets

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/managerguard"
)

var (
	cmdrCommand = regexp.MustCompile("\\b(?:cmdr|commander)\\s+[^`|,;:.]+")
	// A bare name is mostly the program in prose, so only an instruction to run it counts.
	bareCmdrTaught = regexp.MustCompile("(?i)\\b(?:run|route\\s+\\w+\\s+to|use|start|launch|call|execute)\\s+`(?:cmdr|commander)`|^\\s*(?:cmdr|commander)\\s*$")
)

// refusedCockpitTeaching returns what line tells an agent to run that cmdr would
// refuse it. It asks the guard's own classifier, so a form added there is covered
// here. A line that names the user's own terminal is telling the user, not the agent.
func refusedCockpitTeaching(line string) []string {
	if strings.Contains(line, "own terminal") {
		return nil
	}
	var found []string
	for _, m := range cmdrCommand.FindAllString(line, -1) {
		if managerguard.OpensCockpit(m) != "" {
			found = append(found, strings.TrimSpace(m))
		}
	}
	if m := bareCmdrTaught.FindString(line); m != "" {
		found = append(found, strings.TrimSpace(m))
	}
	return found
}

func TestPromptsTeachNoRefusedCockpitCommand(t *testing.T) {
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no prompts read")
	}
	for _, e := range entries {
		body, err := templateFS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, form := range refusedCockpitTeaching(line) {
				t.Errorf("%s:%d teaches %q, which cmdr refuses for an agent:\n%s", e.Name(), i+1, form, line)
			}
		}
	}
}

// The lines that stayed behind while a needle test held green, and their neighbours.
func TestRefusedCockpitTeachingFindsEachForm(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"- If the user says `show me <project>`, route directly to `cmdr --project <project>`.", true},
		{"- If the user says `show me task <id>`, route directly to `cmdr --task <id>`.", true},
		{"| \"show me commander\" | `cmdr --project X` |", true},
		{"| \"work on 3\" | `cmdr internal-workstreams` → find it, then `cmdr --project <project>` |", true},
		{"Run `cmdr restart` to pick it up.", true},
		{"then cmdr reload, and", true},
		{"Start with `commander --task demo-1`.", true},
		{"run `cmdr` first", true},
		{"cmdr", true},

		{"to watch it live, tell the user to run `cmdr --task cmdr-001` in their own terminal", false},
		{"Bare `cmdr`, `cmdr --project` and `cmdr --task` are for the user's own terminal:", false},
		{"`cmdr internal-open-workstream --project X --bead <bead-id> --focus`", false},
		{"`cmdr --help` prints the usage", false},
		{"a Manager's `cmdr` refuses anything else", false},
		{"the running `commander` session", false},
		{"Address peers by ROLE (`commander`, `manager-<workstream-bead-id>`)", false},
	} {
		got := fmt.Sprint(refusedCockpitTeaching(tc.line))
		if (got != "[]") != tc.want {
			t.Errorf("refusedCockpitTeaching(%q) = %s, want found=%v", tc.line, got, tc.want)
		}
	}
}
