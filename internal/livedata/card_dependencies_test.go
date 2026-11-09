package livedata

import (
	"github.com/example-user/commander/internal/store"
	"testing"
)

func dependencyFixture() (Snapshot, NeedsYou) {
	s := Snapshot{Projects: []SnapProject{{Name: "p", Repo: "owner/repo"}}, PRStates: map[string]string{"owner/repo#12": "OPEN"}, Answers: map[string]int{"p:p-w.1": 1}, Workstreams: []SnapWorkstream{{Project: "p", Steps: []SnapStep{{BeadID: "p-w.2", Status: "open", State: "in-progress", PR: []string{"12"}}}}}}
	a := NeedsYou{Project: "p", BeadID: "p-w.1", Decision: &store.Decision{Question: "Approve?", Context: "Depends on p-w.2 and PR #12"}}
	a.Basis = CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
	return s, a
}
func TestCardDependencyChanges(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Snapshot)
	}{
		{"unchanged", "", func(s *Snapshot) {}},
		{"closed", "named", func(s *Snapshot) { s.Workstreams[0].Steps[0].Status = "closed" }},
		{"state", "named", func(s *Snapshot) { s.Workstreams[0].Steps[0].State = "blocked" }},
		{"merged", "pr", func(s *Snapshot) { s.PRStates["owner/repo#12"] = "MERGED" }},
		{"closed PR", "pr", func(s *Snapshot) { s.PRStates["owner/repo#12"] = "CLOSED" }},
		{"answered", "decision", func(s *Snapshot) { s.Answers["p:p-w.1"] = 2 }},
		{"unrelated answer", "", func(s *Snapshot) { s.Answers["p:p-other"] = 2 }},
		{"missing bead", "", func(s *Snapshot) { s.Workstreams = nil }},
		{"missing PR", "", func(s *Snapshot) { s.PRStates = nil }},
		{"unrelated PR", "", func(s *Snapshot) { s.PRStates["other/repo#12"] = "MERGED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a := dependencyFixture()
			tc.change(&s)
			r, _ := CardStaleIn(a, s, 1)
			if r != tc.want {
				t.Fatalf("got %q want %q", r, tc.want)
			}
		})
	}
}
func TestDependencyCaptureBoundariesAndUnknowns(t *testing.T) {
	s, a := dependencyFixture()
	a.Decision.Context = "p-w.20, PR #99, #12"
	b := CardDependencies(s, "p", a.BeadID, a.Decision, "")
	if len(b.Named) != 0 || len(b.PRs) != 0 {
		t.Fatalf("captured unrelated refs: %+v", b)
	}
	a.Decision.Steps = []string{"check p-w.2 and other/repo#12"}
	s.Workstreams[0].Steps[0].PRRepo = "other/repo"
	b = CardDependencies(s, "p", a.BeadID, a.Decision, "")
	if b.Named["p-w.2"] == "" || b.PRs["other/repo#12"] != "unknown" {
		t.Fatalf("steps/repo lost: %+v", b)
	}
	a.Basis = b
	s.PRStates["other/repo#12"] = "MERGED"
	if r, _ := CardStaleIn(a, s, 1); r != "" {
		t.Fatal("unknown is not proof of a change", r)
	}
	a.Basis = nil
	if r, _ := CardStaleIn(a, s, 1); r != "" {
		t.Fatal("legacy card compared", r)
	}
}

func TestCardDependencyGoverningGateAnswer(t *testing.T) {
	s, a := dependencyFixture()
	s.Workstreams[0].Steps = []SnapStep{
		{BeadID: "p-gate", Title: "Approve plan", Gate: true, Approvers: []string{"user"}, Status: "open"},
		{BeadID: a.BeadID, Title: "Build", Status: "open", BlockedBy: []string{"p-gate"}},
	}
	s.Answers["p:p-gate"] = 1
	a.Basis = CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
	if a.Basis.Answers["p-gate"] != 1 {
		t.Fatalf("gate answer omitted: %+v", a.Basis)
	}
	s.Answers["p:p-gate"] = 2
	if r, _ := CardStaleIn(a, s, 1); r != "decision" {
		t.Fatalf("got %q", r)
	}
}

func TestCardDependencyNoticeKeyChangesOnSameSecondRebaseline(t *testing.T) {
	_, a := dependencyFixture()
	before := StaleNoticeKey(a)
	a.Basis.PRs["owner/repo#12"] = "MERGED"
	if before == StaleNoticeKey(a) {
		t.Fatal("dependency rebaseline did not renew notice")
	}
}

func TestReviewNamedSpecChange(t *testing.T) {
	s, a := dependencyFixture()
	s.Workstreams[0].Steps[0].SpecHash = "before"
	a.Basis = CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
	s.Workstreams[0].Steps[0].SpecHash = "after"
	if r, _ := CardStaleIn(a, s, 1); r != "named" {
		t.Fatalf("named spec edit missed: got %q", r)
	}
}
func TestReviewQualifiedPRDoesNotTrackOtherRepo(t *testing.T) {
	s, a := dependencyFixture()
	a.Decision.Context = "Depends on https://github.com/owner/repo/pull/12"
	s.Workstreams[0].Steps = append(s.Workstreams[0].Steps, SnapStep{BeadID: "p-w.3", PRRepo: "other/repo", PR: []string{"12"}})
	s.PRStates["other/repo#12"] = "OPEN"
	a.Basis = CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
	s.PRStates["other/repo#12"] = "MERGED"
	if r, _ := CardStaleIn(a, s, 1); r != "" {
		t.Fatalf("unrelated repo merge stales unchanged card: %q, basis=%+v", r, a.Basis)
	}
}
func TestReviewMissingAnswerFactRestored(t *testing.T) {
	s, a := dependencyFixture()
	s.Answers = map[string]int{} // Source.Snapshot continues after ListPitches error
	a.Basis = CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
	s.Answers["p:p-w.1"] = 1 // existing answer becomes readable, no new answer
	if r, _ := CardStaleIn(a, s, 1); r != "" {
		t.Fatalf("restored existing answer stales unchanged card: %q", r)
	}
}

func TestCardDependencyNamedLegacyAndUnknownSpec(t *testing.T) {
	for _, before := range []string{"open|in-progress", "open|in-progress|"} {
		s, a := dependencyFixture()
		a.Basis.Named["p-w.2"] = before
		s.Workstreams[0].Steps[0].SpecHash = "new"
		if r, _ := CardStaleIn(a, s, 1); r != "" {
			t.Fatalf("unknown spec flagged: %q", r)
		}
		s.Workstreams[0].Steps[0].Status = "closed"
		if r, _ := CardStaleIn(a, s, 1); r != "named" {
			t.Fatalf("state change missed: %q", r)
		}
	}
	s, a := dependencyFixture()
	a.Basis.Named["p-w.2"] = "open|in-progress|known"
	if r, _ := CardStaleIn(a, s, 1); r != "" {
		t.Fatalf("missing current spec flagged: %q", r)
	}
}

func TestCardDependencyPRRepositoryResolution(t *testing.T) {
	for _, ref := range []string{"PR #12", "owner/repo#12", "https://github.com/owner/repo/pull/12"} {
		s, a := dependencyFixture()
		a.Decision.Context = ref
		s.Workstreams[0].Steps = append(s.Workstreams[0].Steps, SnapStep{PRRepo: "other/repo", PR: []string{"12"}})
		b := CardDependencies(s, a.Project, a.BeadID, a.Decision, "")
		if len(b.PRs) != 1 || b.PRs["owner/repo#12"] != "OPEN" {
			t.Fatalf("%s: %+v", ref, b.PRs)
		}
		a.Decision.Context = "stranger/repo#12"
		if b := CardDependencies(s, a.Project, a.BeadID, a.Decision, ""); len(b.PRs) != 0 {
			t.Fatal("untracked PR captured", b.PRs)
		}
	}
}
