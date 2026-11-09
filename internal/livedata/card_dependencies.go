package livedata

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// CardDependencies records only facts already on the board. Missing facts are
// unknown, never evidence of a change. PR keys include the repository.
func CardDependencies(s Snapshot, project, beadID string, d *store.Decision, text string) *store.Basis {
	b := &store.Basis{Named: map[string]string{}, PRs: map[string]string{}, Answers: map[string]int{}}
	if d != nil {
		text += "\n" + d.Question + "\n" + d.Context + "\n" + strings.Join(d.Steps, "\n")
		for _, o := range d.Options {
			text += "\n" + o.Label + "\n" + o.Implications
		}
	}

	repo := ""
	for _, p := range s.Projects {
		if p.Name == project {
			repo = p.Repo
		}
	}
	refs := dependencyPRRefs(text, repo)
	related := map[string]bool{beadID: true}
	for _, g := range UserGatesOver(s, beadID) {
		related[g.Gate] = true
	}
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, st := range steps {
			if st.BeadID != beadID && hasToken(text, st.BeadID) {
				b.Named[st.BeadID] = st.Status + "|" + st.State + "|" + st.SpecHash
			}
			// Only board-tracked PRs in this project may become dependencies.
			prRepo := repo
			if st.PRRepo != "" {
				prRepo = st.PRRepo
			}
			for _, n := range st.PR {
				if refs[strings.ToLower(prRepo)+"#"+n] && prRepo != "" {
					key := prRepo + "#" + n
					state := s.PRStates[key]
					if state == "" {
						state = "unknown"
					}
					b.PRs[key] = state
				}
			}
			walk(st.Children)
		}
	}
	for _, w := range s.Workstreams {
		if w.Project == project {
			walk(w.Steps)
		}
	}
	for id := range related {
		if version, ok := s.Answers[project+":"+id]; ok {
			b.Answers[id] = version
		}
	}
	return b
}

// Qualified references retain their repository; ordinary PR #N refers to the
// project repository. Matching still requires a board-tracked PR below.
var dependencyPRRE = regexp.MustCompile(`(?i)https?://github\.com/([a-z0-9_.-]+/[a-z0-9_.-]+)/pull/(\d+)\b|\b([a-z0-9_.-]+/[a-z0-9_.-]+)#(\d+)\b|\b(?:PR|pull request)\s*#?(\d+)\b`)

func dependencyPRRefs(text, repo string) map[string]bool {
	refs := map[string]bool{}
	for _, m := range dependencyPRRE.FindAllStringSubmatch(text, -1) {
		r, n := repo, m[5]
		if m[1] != "" {
			r, n = m[1], m[2]
		}
		if m[3] != "" {
			r, n = m[3], m[4]
		}
		if r != "" {
			refs[strings.ToLower(r)+"#"+n] = true
		}
	}
	return refs
}

func namedDependencyChanged(before, after string) bool {
	old, now := strings.SplitN(before, "|", 3), strings.SplitN(after, "|", 3)
	if len(old) < 2 || len(now) < 2 {
		return false
	}
	if old[0] != now[0] || old[1] != now[1] {
		return true
	}
	return len(old) == 3 && len(now) == 3 && old[2] != "" && now[2] != "" && old[2] != now[2]
}

func dependencyStale(a NeedsYou, s Snapshot) (string, string) {
	if a.Basis == nil {
		return "", ""
	}
	current := CardDependencies(s, a.Project, a.BeadID, a.Decision, a.Ask+"\n"+a.Note)
	ids := make([]string, 0, len(a.Basis.Named))
	for id := range a.Basis.Named {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if now, ok := current.Named[id]; ok && namedDependencyChanged(a.Basis.Named[id], now) {
			return "named", "A step named in this card changed state"
		}
	}
	ids = ids[:0]
	for id := range a.Basis.PRs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		now := s.PRStates[id]
		if a.Basis.PRs[id] != "unknown" && a.Basis.PRs[id] != "" && now != a.Basis.PRs[id] && (now == "MERGED" || now == "CLOSED") {
			_, n, _ := strings.Cut(id, "#")
			return "pr", fmt.Sprintf("PR #%s %s", n, strings.ToLower(now))
		}
	}
	for id, version := range a.Basis.Answers {
		if s.Answers[a.Project+":"+id] > version {
			return "decision", "A newer decision on this step or its gate was answered"
		}
	}
	return "", ""
}

// CardStaleIn includes dependencies while retaining the standalone age/spec API.
func CardStaleIn(a NeedsYou, s Snapshot, now int64) (string, string) {
	reason, why := CardStale(a, now)
	if reason != "" && reason != StaleAge {
		return reason, why
	}
	if r, w := dependencyStale(a, s); r != "" {
		return r, w
	}
	return reason, why
}
