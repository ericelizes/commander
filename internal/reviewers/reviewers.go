// Package reviewers says who a review wait (waiting:<since>:review:<ref>) waits
// on: one GitHub login, any member of a GitHub team (<org>/<team>, or a bare team
// slug in the PR's org), or anyone (review:any). It only reads GitHub (gh api GET).
package reviewers

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Any is the review wait ref that any reviewer's verdict clears.
const Any = "any"

// Runner runs `gh <args>` and returns stdout. gh prints an API error's JSON body
// (with its "status") on stdout and exits non-zero; a Runner passes that body on.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// RunGH is the production Runner.
func RunGH(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil && len(out) > 0 {
		return out, nil
	}
	return out, err
}

// Team reads a ref written as <org>/<team>.
func Team(ref string) (org, slug string, ok bool) {
	org, slug, ok = strings.Cut(ref, "/")
	return org, slug, ok && org != "" && slug != ""
}

// Owner is the owner part of an owner/name repo.
func Owner(repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	return owner
}

// apiStatus reads gh's error body: "404" etc., "" when out is not one.
func apiStatus(out []byte) string {
	var e struct{ Status string }
	if json.Unmarshal(out, &e) != nil {
		return ""
	}
	return e.Status
}

// teamTTL is how long a team's member list is kept: membership changes rarely,
// and the wait poll runs every minute.
const teamTTL = 10 * time.Minute

type teamEntry struct {
	members map[string]bool // lowercased logins
	found   bool
	at      time.Time
}

// Teams reads and caches GitHub team membership.
type Teams struct {
	GH  Runner
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]teamEntry
}

// Members is org/slug's member logins (lowercased). found is false when GitHub
// says there is no such team; err means it could not tell (not cached).
func (t *Teams) Members(ctx context.Context, org, slug string) (members map[string]bool, found bool, err error) {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	key := strings.ToLower(org + "/" + slug)
	t.mu.Lock()
	e, ok := t.cache[key]
	t.mu.Unlock()
	if ok && now().Sub(e.at) < teamTTL {
		return e.members, e.found, nil
	}
	out, err := t.GH(ctx, "api", "orgs/"+org+"/teams/"+slug+"/members", "--paginate", "--method", "GET", "--jq", ".[].login")
	if err != nil {
		return nil, false, err
	}
	e = teamEntry{at: now()}
	switch apiStatus(out) {
	case "404":
	case "":
		e.found, e.members = true, map[string]bool{}
		for _, l := range strings.Fields(string(out)) {
			e.members[strings.ToLower(l)] = true
		}
	default:
		return nil, false, fmt.Errorf("gh api orgs/%s/teams/%s/members: HTTP %s", org, slug, apiStatus(out))
	}
	t.mu.Lock()
	if t.cache == nil {
		t.cache = map[string]teamEntry{}
	}
	t.cache[key] = e
	t.mu.Unlock()
	return e.members, e.found, nil
}

// lookup asks gh for path's .type; found is false on a 404, err when it could not tell.
func lookup(ctx context.Context, gh Runner, path string) (typ string, found bool, err error) {
	out, err := gh(ctx, "api", path, "--method", "GET", "--jq", ".type // \"team\"")
	if err != nil {
		return "", false, err
	}
	switch s := apiStatus(out); s {
	case "":
		return strings.TrimSpace(string(out)), true, nil
	case "404":
		return "", false, nil
	default:
		return "", false, fmt.Errorf("gh api %s: HTTP %s", path, s)
	}
}

// Check reports whether a review wait's ref can ever match a reviewer of a PR
// in repo (owner/name, "" when not known): any; a team (<org>/<team>, or a bare
// slug of a team in repo's org); a collaborator on repo; a GitHub App (<name>[bot]).
// Without a repo, any GitHub user. refusal says why it can never match and names
// the right forms. When gh cannot answer, the ref is allowed and unchecked says
// why (fail open). A collaborator or an <org>/<team> costs one gh call; only a
// name that is neither costs more.
func Check(ctx context.Context, gh Runner, ref, repo string) (refusal, unchecked error) {
	if ref == Any {
		return nil, nil
	}
	forms := "review:<GitHub login>, review:<org>/<team> (any member of that GitHub team), or review:any (any reviewer's approval or change request)"
	if org, slug, ok := Team(ref); ok {
		_, found, err := lookup(ctx, gh, "orgs/"+org+"/teams/"+slug)
		if err != nil {
			return nil, err
		}
		if !found {
			return fmt.Errorf("--waiting review:%s: GitHub has no team %s in %s (or gh cannot see it), so no review can ever clear the wait. Use the team's slug from github.com/orgs/%s/teams, or %s", ref, slug, org, org, forms), nil
		}
		return nil, nil
	}
	what := "is not a GitHub user, a team or a GitHub App"
	if repo != "" {
		// Any GitHub account may exist by that name (infra-team, infrastructure do): only one
		// who can review this repo counts.
		if _, found, err := lookup(ctx, gh, "repos/"+repo+"/collaborators/"+ref); err != nil || found {
			return nil, err
		}
		what = "is not a collaborator on " + repo + ", a team in " + Owner(repo) + " or a GitHub App"
		if _, team, err := lookup(ctx, gh, "orgs/"+Owner(repo)+"/teams/"+ref); err != nil || team {
			return nil, err
		}
	} else {
		typ, found, err := lookup(ctx, gh, "users/"+ref)
		if err != nil {
			return nil, err
		}
		if found && typ != "Organization" {
			return nil, nil
		}
		if found {
			what = "is an organization, which cannot review"
		}
	}
	// A GitHub App reviews as <name>[bot], and gh pr view names it <name>.
	if typ, bot, err := lookup(ctx, gh, "users/"+ref+"[bot]"); err != nil || (bot && typ == "Bot") {
		return nil, err
	}
	return fmt.Errorf("--waiting review:%s: %q %s, so no review can ever clear the wait. Use %s", ref, ref, what, forms), nil
}
