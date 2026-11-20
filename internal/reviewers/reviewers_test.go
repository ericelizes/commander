package reviewers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// github answers like gh api GET did on Nov 20 2026: a body on stdout, an error body (with its
// status) on stdout for an API error, a Go error when gh cannot reach GitHub.
type github struct {
	answers map[string]string
	down    bool
	calls   []string
}

func (g *github) run(_ context.Context, args ...string) ([]byte, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	if g.down {
		return nil, errors.New("gh: could not resolve host api.github.com")
	}
	if out, ok := g.answers[args[1]]; ok {
		return []byte(out), nil
	}
	return []byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`), nil
}

func nov20() *github {
	const repo = "repos/august-innovations/infrastructure/collaborators/"
	return &github{answers: map[string]string{
		repo + "GoodmanBen":                                    "",
		repo + "ericelizes":                                    "",
		"users/GoodmanBen":                                     "User\n",
		"users/infra-team":                                     "User\n", // a real, unrelated account
		"users/devin-ai-integration[bot]":                      "Bot\n",
		"users/august-innovations":                             "Organization\n",
		"orgs/august-innovations/teams/infrastructure":         "",
		"orgs/august-innovations/teams/infrastructure/members": "chub\nrtolsma\nzukwung\nGoodmanBen\nmfournierfira\n",
	}}
}

func TestCheck(t *testing.T) {
	const infra = "august-innovations/infrastructure"
	for _, c := range []struct {
		ref, repo, refuse string
		calls             int
	}{
		{"GoodmanBen", infra, "", 1},
		{"august-innovations/infrastructure", infra, "", 1},
		{"any", infra, "", 0},
		{"infrastructure", infra, "", 2},
		{"devin-ai-integration", infra, "", 3},
		// GitHub has a user infra-team, who is nobody on this repo.
		{"infra-team", infra, "is not a collaborator on august-innovations/infrastructure, a team in august-innovations or a GitHub App", 3},
		{"august-innovations/infra", infra, "GitHub has no team infra in august-innovations", 1},
		// No repo known: any GitHub user.
		{"infra-team", "", "", 1},
		{"no-such-user", "", "is not a GitHub user, a team or a GitHub App", 2},
		{"august-innovations", "", "is an organization, which cannot review", 2},
	} {
		g := nov20()
		refusal, unchecked := Check(context.Background(), g.run, c.ref, c.repo)
		if unchecked != nil {
			t.Errorf("%s: unchecked %v", c.ref, unchecked)
		}
		if c.refuse == "" && refusal != nil || c.refuse != "" && (refusal == nil || !strings.Contains(refusal.Error(), c.refuse)) {
			t.Errorf("%s (%s): refusal %v, want %q", c.ref, c.repo, refusal, c.refuse)
		}
		if refusal != nil && !strings.Contains(refusal.Error(), "review:<org>/<team>") {
			t.Errorf("%s: the refusal must name the right forms: %v", c.ref, refusal)
		}
		if len(g.calls) != c.calls {
			t.Errorf("%s (%s): %d gh calls, want %d: %v", c.ref, c.repo, len(g.calls), c.calls, g.calls)
		}
	}
}

// gh unreachable: the write goes through (fail open) and says why.
func TestCheckFailsOpenWhenGHCannotAnswer(t *testing.T) {
	g := &github{down: true}
	refusal, unchecked := Check(context.Background(), g.run, "anyone", "acme/web")
	if refusal != nil || unchecked == nil {
		t.Fatalf("refusal %v, unchecked %v; want allowed with a reason", refusal, unchecked)
	}
	g = &github{answers: map[string]string{"repos/acme/web/collaborators/x": `{"message":"Must have push access to view repository collaborators.","status":"403"}`}}
	if refusal, unchecked := Check(context.Background(), g.run, "x", "acme/web"); refusal != nil || unchecked == nil {
		t.Fatalf("a 403 is not a missing user: refusal %v, unchecked %v", refusal, unchecked)
	}
}

func TestTeamMembersAreCached(t *testing.T) {
	g := nov20()
	now := time.Date(2026, 11, 20, 22, 39, 0, 0, time.UTC)
	teams := &Teams{GH: g.run, Now: func() time.Time { return now }}
	for range 3 {
		members, found, err := teams.Members(context.Background(), "august-innovations", "infrastructure")
		if err != nil || !found || !members["goodmanben"] || members["ericelizes"] {
			t.Fatalf("members %v found %v err %v", members, found, err)
		}
	}
	if _, found, err := teams.Members(context.Background(), "august-innovations", "nope"); found || err != nil {
		t.Fatalf("a missing team: found %v err %v", found, err)
	}
	teams.Members(context.Background(), "august-innovations", "nope")
	if len(g.calls) != 2 {
		t.Fatalf("want one call per team (a missing team cached too), got %v", g.calls)
	}
	now = now.Add(teamTTL + time.Second)
	teams.Members(context.Background(), "august-innovations", "infrastructure")
	if len(g.calls) != 3 {
		t.Fatalf("an old member list must be read again: %v", g.calls)
	}
	g.down = true
	now = now.Add(teamTTL + time.Second)
	if _, _, err := teams.Members(context.Background(), "august-innovations", "infrastructure"); err == nil {
		t.Fatal("gh down must be an error, not an empty team")
	}
}
