package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// fakeApprovalGH answers as real gh does: pr view prints the PR's JSON, gh api prints a team's
// member logins (--jq .[].login) or, for an API error, its JSON body on stdout and exits 1.
const fakeApprovalGH = `#!/bin/sh
case "$1 $2" in
"pr view")
  n=$3; repo=""; shift 3
  while [ $# -gt 0 ]; do [ "$1" = "--repo" ] && repo=$2; shift; done
  f="$FAKE_GH/pr_$(echo "$repo" | tr / _)_$n.json"
  if [ -f "$f" ]; then cat "$f"; exit 0; fi
  echo '{"message":"Not Found","status":"404"}'; echo 'gh: Not Found (HTTP 404)' >&2; exit 1;;
"api orgs/"*)
  f="$FAKE_GH/$(echo "$2" | tr / _)"
  if [ -f "$f" ]; then cat "$f"; exit 0; fi
  case "$2" in *secret-team*) echo '{"message":"Forbidden","status":"403"}'; echo 'gh: Forbidden (HTTP 403)' >&2; exit 1;; esac
  echo '{"message":"Not Found","status":"404"}'; echo 'gh: Not Found (HTTP 404)' >&2; exit 1;;
esac
echo "fake gh: unexpected $*" >&2; exit 2
`

// TestApprovalWaitScenario runs tests/evals/scenarios/approval-waits.json: the two waits stuck on
// Nov 20 (pr:approved on a repo with no review rule) clear on the next poll with no bead edit, and
// team, any and named review waits clear only on a qualified reviewer.
func TestApprovalWaitScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/approval-waits.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		PRs   map[string]json.RawMessage
		Teams map[string][]string
		Cases []struct {
			Name, Subject, Closed string
			Labels                []string
			Cleared               bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("scenario has no cases")
	}
	bin, world := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeApprovalGH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH", world)
	for ref, view := range scenario.PRs {
		repo, n, _ := strings.Cut(ref, "#")
		if err := os.WriteFile(filepath.Join(world, "pr_"+strings.ReplaceAll(repo, "/", "_")+"_"+n+".json"), view, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for team, members := range scenario.Teams {
		org, slug, _ := strings.Cut(team, "/")
		if err := os.WriteFile(filepath.Join(world, "orgs_"+org+"_teams_"+slug+"_members"), []byte(strings.Join(members, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checked := time.Date(2026, 11, 20, 22, 39, 22, 0, time.UTC) // the poll that left them waiting
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			w, m, sent := memWatcher(c.Labels, checked)
			w.gh = runGH
			var closed []string
			w.closeStep = func(_ context.Context, _, bead, reason string) error {
				closed = append(closed, bead+": "+reason)
				return nil
			}
			w.poll(context.Background())
			var wantClosed []string
			if c.Closed != "" {
				wantClosed = []string{"p-ws.1: " + c.Closed}
			}
			if !slices.Equal(closed, wantClosed) {
				t.Fatalf("closed %q, want %q", closed, wantClosed)
			}
			var wantSent []sentMail
			if c.Subject != "" {
				wantSent = []sentMail{{"manager-p-ws", c.Subject}}
			}
			if !slices.Equal(*sent, wantSent) {
				t.Fatalf("mail %+v, want %+v", *sent, wantSent)
			}
			var waits []string
			for _, l := range labelsOf(t, m) {
				if wt, ok := beads.ParseWaitLabel(l); ok && (wt.Kind == beads.WaitPR || wt.Kind == beads.WaitReview) {
					waits = append(waits, l)
				}
			}
			if c.Cleared != (len(waits) == 0) {
				t.Fatalf("cleared=%v, want %v (waits left: %v)", len(waits) == 0, c.Cleared, waits)
			}
		})
	}
}
