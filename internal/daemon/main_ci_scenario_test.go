package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// fakeGHScript behaves as real gh does on these reads: run list matches --commit
// against whole shas only, commits/<sha> resolves a prefix, and an API error
// prints its body on stdout and exits 1.
const fakeGHScript = `#!/bin/sh
[ -e "$FAKE_GH/down" ] && { echo '{"message":"Server Error","status":"502"}'; exit 1; }
case "$1 $2" in
"run list")
	shift 2
	while [ $# -gt 0 ]; do
		[ "$1" = --commit ] && sha=$2
		shift
	done
	if [ -f "$FAKE_GH/$sha.json" ]; then cat "$FAKE_GH/$sha.json"; else echo '[]'; fi
	;;
api*)
	repo=${2#repos/}; repo=${repo%%/commits/*}; sha=${2##*/}
	if [ "$repo" != "$(cat "$FAKE_GH/repo")" ]; then
		echo '{"message":"Not Found","status":"404"}'; exit 1
	fi
	for f in "$FAKE_GH"/"$sha"*.json; do
		[ -e "$f" ] && { basename "$f" .json; exit 0; }
	done
	echo '{"message":"No commit found for SHA: '"$sha"'","status":"422"}'; exit 1
	;;
*) echo "unexpected gh $*" >&2; exit 2 ;;
esac
`

// The real poll, runGH and a gh process, so a sha form only real gh refuses cannot pass.
func TestMainCIWaitScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/main-ci-wait.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Repo    string
		Commits map[string]json.RawMessage
		Cases   []struct {
			Name, Wait, Subject string
			AgeMinutes          int `json:"age_minutes"`
			Cleared             bool
			GHDown              bool `json:"gh_down"`
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("scenario has no cases")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			world := t.TempDir()
			t.Setenv("FAKE_GH", world)
			if err := os.WriteFile(filepath.Join(world, "repo"), []byte(scenario.Repo), 0o644); err != nil {
				t.Fatal(err)
			}
			for sha, runs := range scenario.Commits {
				if err := os.WriteFile(filepath.Join(world, sha+".json"), runs, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.GHDown {
				if err := os.WriteFile(filepath.Join(world, "down"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wt, err := beads.ParseWait(c.Wait)
			if err != nil {
				t.Fatal(err)
			}
			wt.Since = epoch
			label := wt.Label()
			if len(label) > beads.MaxLabelLen {
				t.Fatalf("%s cannot be recorded: %s", c.Wait, label)
			}
			b := waitBead(label)
			var sent []sentMail
			w := watcher(b, nil, &sent, epoch.Add(time.Duration(c.AgeMinutes)*time.Minute))
			w.gh = runGH
			w.poll(context.Background())
			var want []sentMail
			if c.Subject != "" {
				want = []sentMail{{"manager-p-ws", c.Subject}}
			}
			if fmt.Sprint(sent) != fmt.Sprint(want) {
				t.Errorf("mail %+v, want %+v", sent, want)
			}
			if cleared := strings.Contains(strings.Join(b.Calls, "\n"), "label="+label+"|on=false"); cleared != c.Cleared {
				t.Errorf("cleared = %v, want %v: %v", cleared, c.Cleared, b.Calls)
			}
		})
	}
}
