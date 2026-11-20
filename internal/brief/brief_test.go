package brief

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
	"github.com/example-user/commander/internal/store"
)

func et(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, Zone) }

func TestNextSlotIsSevenAMThenEverySixHours(t *testing.T) {
	for _, c := range []struct{ now, want time.Time }{
		{et(2026, 11, 20, 0, 30), et(2026, 11, 20, 1, 0)},
		{et(2026, 11, 20, 6, 59), et(2026, 11, 20, 7, 0)},
		{et(2026, 11, 20, 7, 0), et(2026, 11, 20, 13, 0)}, // strictly after: a slot that just ran is not next
		{et(2026, 11, 20, 8, 23), et(2026, 11, 20, 13, 0)},
		{et(2026, 11, 20, 13, 1), et(2026, 11, 20, 19, 0)},
		{et(2026, 11, 20, 19, 30), et(2026, 11, 21, 1, 0)},
		{et(2026, 12, 31, 23, 0), et(2027, 1, 1, 1, 0)},
		// Daylight saving: the slots stay on Eastern wall-clock hours.
		{et(2026, 3, 8, 1, 30), et(2026, 3, 8, 7, 0)},
		{et(2026, 11, 1, 6, 0), et(2026, 11, 1, 7, 0)},
	} {
		if got := NextSlot(c.now, Zone); !got.Equal(c.want) {
			t.Errorf("NextSlot(%s) = %s, want %s", c.now, got, c.want)
		}
	}
	// Any zone in, Eastern out.
	if got := NextSlot(time.Date(2026, 11, 20, 11, 30, 0, 0, time.UTC), Zone); !got.Equal(et(2026, 11, 20, 7, 0)) {
		t.Fatal(got)
	}
	if Zone.String() != "America/New_York" {
		t.Fatal(Zone)
	}
}

func TestYesterdayIsThePreviousWorkdayAndTheWeekStartsMonday(t *testing.T) {
	for _, c := range []struct {
		name            string
		now             time.Time
		yesterday, week time.Time
		label           string
	}{
		{"Monday reads Friday", et(2026, 11, 23, 9, 0), et(2026, 11, 20, 0, 0), et(2026, 11, 23, 0, 0), "Friday, Nov 20"},
		{"Tuesday reads Monday", et(2026, 11, 24, 9, 0), et(2026, 11, 23, 0, 0), et(2026, 11, 23, 0, 0), "Monday, Nov 23"},
		{"Friday reads Thursday", et(2026, 11, 20, 8, 0), et(2026, 11, 19, 0, 0), et(2026, 11, 16, 0, 0), "Thursday, Nov 19"},
		{"Saturday reads Friday", et(2026, 11, 21, 9, 0), et(2026, 11, 20, 0, 0), et(2026, 11, 16, 0, 0), "Friday, Nov 20"},
		{"Sunday reads Friday", et(2026, 11, 22, 9, 0), et(2026, 11, 20, 0, 0), et(2026, 11, 16, 0, 0), "Friday, Nov 20"},
	} {
		w := WindowsAt(c.now, Zone)
		if !w.Yesterday.Start.Equal(c.yesterday) || !w.Yesterday.End.Equal(c.yesterday.AddDate(0, 0, 1)) || w.Yesterday.Label != c.label {
			t.Errorf("%s: yesterday %s-%s %q", c.name, w.Yesterday.Start, w.Yesterday.End, w.Yesterday.Label)
		}
		if !w.Week.Start.Equal(c.week) || !w.Week.End.Equal(c.now) {
			t.Errorf("%s: week %s-%s", c.name, w.Week.Start, w.Week.End)
		}
	}
	mon := WindowsAt(et(2026, 11, 23, 9, 0), Zone)
	if !mon.Since().Equal(et(2026, 11, 20, 0, 0)) {
		t.Fatal("Monday reaches back to Friday", mon.Since())
	}
	for at, want := range map[time.Time]string{
		et(2026, 11, 23, 8, 0):  "today",
		et(2026, 11, 20, 15, 0): "yesterday",
		et(2026, 11, 21, 10, 0): "", // the weekend is neither yesterday nor this week
		et(2026, 11, 19, 10, 0): "",
		et(2026, 11, 23, 10, 0): "", // after now
	} {
		if got := mon.When(at); got != want {
			t.Errorf("When(%s) = %q, want %q", at, got, want)
		}
	}
	fri := WindowsAt(et(2026, 11, 20, 8, 0), Zone)
	if fri.When(et(2026, 11, 17, 12, 0)) != "week" || fri.When(et(2026, 11, 19, 23, 59)) != "yesterday" {
		t.Fatal("Friday's week and yesterday")
	}
}

func TestShortName(t *testing.T) {
	for in, want := range map[string]string{
		"Forge Notify: every notification goes through Notify":                 "Forge Notify",
		"F2F diagnoses stop polling hard: backoff (ENGCHA-5337)":               "F2F diagnoses stop polling hard",
		"Claims stuck after payer answered (Hunter: ENGINT-3263, ENGINT-3288)": "Claims stuck after payer answered",
		"Caesar & RCM Hub automation failures (daily health report, Nov 9)":    "Caesar & RCM Hub automation failures (daily health report, Nov 9)",
		"QA Hub": "QA Hub",
	} {
		if got := ShortName(in); got != want {
			t.Errorf("ShortName(%q) = %q, want %q", in, got, want)
		}
	}
}

// sources is a Friday morning: a board with one workstream, what finished
// yesterday and today, and a Linear ticket.
func sources(now time.Time) Sources {
	yesterday, today := now.Add(-20*time.Hour), now.Add(-time.Hour)
	tree := livedata.Tree{
		Rows: []livedata.TreeRow{
			{ID: "p-ws", Key: "p:p-ws", Project: "p", Depth: 0, Title: "Billing: retries that never double charge (ABC-1)", Criteria: []string{"A retry charges once."}, Progress: 60, Of: "3 of 5 steps", State: "In progress", Tickets: []string{"ABC-1"}},
			{ID: "p-ws.2", Key: "p:p-ws.2", Project: "p", Parent: "p-ws", Depth: 1, Title: "Rollout", Progress: 0},
		},
		Steps: []livedata.TreeStep{
			{ID: "p-ws.2.1", Row: "p-ws.2", Title: "Deploy window", Type: "automatic", State: "active", Col: 0, Own: "Automatic", Wait: "Waiting for the deploy window"},
			{ID: "p-ws.2.2", Row: "p-ws.2", Title: "Soak until Monday", Type: "automatic", State: "notstarted", Col: 1, Own: "Automatic"},
			{ID: "p-ws.3", Row: "p-ws", Title: "Approve the rollout", Type: "you", State: "active", Col: 0, Own: "You", Ask: "Roll out?"},
			{ID: "p-ws.4", Row: "p-ws", Title: "Done long ago", State: "done", Col: -1},
			{ID: "p-ws.5", Row: "p-ws", Title: "Far out", State: "notstarted", Col: 3},
		},
	}
	return Sources{
		Tree:      tree,
		WaitUntil: map[string]int64{"p-ws.2.1": now.Add(3 * time.Hour).Unix(), "p-ws.2.2": now.Add(72 * time.Hour).Unix()},
		Beads: []store.BriefBead{
			{Project: "p", ID: "p-ws.1", Title: "Build the retry key", Status: "closed", CloseReason: "Merged PR 12", UpdatedAt: yesterday, Parent: "Billing", Workstream: "p-ws", WorkstreamTitle: "Billing", Labels: []string{"kind:objective"}},
			{Project: "p", ID: "p-ws.1.9", Title: "Review the retry key", Status: "closed", UpdatedAt: today, Workstream: "p-ws", WorkstreamTitle: "Billing", Labels: []string{"review"}},
			{Project: "p", ID: "p-ws.1.8", Title: "Old plan", Status: "closed", UpdatedAt: yesterday, Workstream: "p-ws", Labels: []string{"superseded"}},
			{Project: "p", ID: "p-ws.1.7", Title: "Automatic check", Status: "closed", UpdatedAt: yesterday, Workstream: "p-ws", Labels: []string{"gate"}},
			{Project: "p", ID: "p-ws.1.6", Title: "Confirm the key is done", Status: "closed", UpdatedAt: yesterday, Workstream: "p-ws", Labels: []string{"gate", "approver:user"}},
			{Project: "p", ID: "p-ws.1.5", Title: "Still open", Status: "open", UpdatedAt: yesterday, Workstream: "p-ws"},
			{Project: "q", ID: "q-done", Title: "Docs site: new search (XY-9)", Status: "closed", UpdatedAt: now.Add(-50 * time.Hour), Workstream: "q-done", WorkstreamTitle: "Docs site: new search (XY-9)", Labels: []string{"kind:workstream", "ticket:XY-9"}},
			{Project: "q", ID: "q-loose", Title: "A loose fix", Status: "closed", UpdatedAt: yesterday},
		},
		Reports: []store.BriefReport{
			{Project: "p", BeadID: "p-ws.1", Text: "Final report: older", CreatedAt: yesterday.Add(-time.Minute)},
			{Project: "p", BeadID: "p-ws.1", Text: "Final report: PASS, merged.", CreatedAt: yesterday},
		},
		Linear: []Ticket{
			{ID: "ABC-1", Title: "Retries charge once", State: "In Progress", StateType: "started", UpdatedAt: yesterday},
			{ID: "XY-9", Title: "New search", State: "Done", StateType: "completed", UpdatedAt: yesterday, CompletedAt: yesterday},
			{ID: "ZZ-3", Title: "Edited only", State: "Triage", StateType: "triage", UpdatedAt: today},
		},
	}
}

func byID(in Input) map[string]Evidence {
	m := map[string]Evidence{}
	for _, e := range in.Evidence {
		m[e.ID] = e
	}
	return m
}

func TestAssembleTagsEachRecordWithItsWorkstreamAndWindow(t *testing.T) {
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	ev := byID(in)
	for id, want := range map[string][2]string{
		"goal:p:p-ws":     {"p:p-ws", "board"},
		"step:p-ws.2.1":   {"p:p-ws", "board"}, // its wait comes due today
		"step:p-ws.2.2":   {"p:p-ws", "later"}, // Monday's soak is not today
		"step:p-ws.3":     {"p:p-ws", "board"},
		"bead:p:p-ws.1":   {"p:p-ws", "yesterday"},
		"bead:p:p-ws.1.9": {"p:p-ws", "today"},
		"bead:p:p-ws.1.6": {"p:p-ws", "yesterday"}, // the user's own approval
		"bead:q:q-loose":  {Other, "yesterday"},
		"report:p:p-ws.1": {"p:p-ws", "yesterday"},
		"linear:ABC-1":    {"p:p-ws", "board"}, // joined through the board's ticket
		"linear:XY-9":     {"q:q-done", "yesterday"},
		"week:p:p-ws":     {"p:p-ws", "week"},
		"week:q:q-done":   {"q:q-done", "week"},
	} {
		e, ok := ev[id]
		if !ok || e.Workstream != want[0] || e.When != want[1] {
			t.Errorf("%s: %+v, want %v", id, e, want)
		}
	}
	for _, id := range []string{"step:p-ws.4", "step:p-ws.5", "bead:p:p-ws.1.8", "bead:p:p-ws.1.7", "bead:p:p-ws.1.5", "linear:ZZ-3", "bead:q:q-done"} {
		if _, ok := ev[id]; ok {
			t.Errorf("%s should not be evidence", id)
		}
	}
	if !strings.Contains(ev["report:p:p-ws.1"].Text, "PASS, merged.") || strings.Contains(ev["report:p:p-ws.1"].Text, "older") {
		t.Fatal("the latest report per bead", ev["report:p:p-ws.1"])
	}
	if !strings.Contains(ev["step:p-ws.2.1"].Text, `"happens_today":true`) || !strings.Contains(ev["step:p-ws.2.1"].Text, `"part_of":"Rollout"`) {
		t.Fatal(ev["step:p-ws.2.1"].Text)
	}
	if !strings.Contains(ev["goal:p:p-ws"].Text, `"percent_done":60`) || !strings.Contains(ev["goal:p:p-ws"].Text, "A retry charges once.") {
		t.Fatal(ev["goal:p:p-ws"].Text)
	}
	var week map[string]any
	json.Unmarshal([]byte(ev["week:q:q-done"].Text), &week)
	if week["finished_this_week"] != 1.0 || len(week["workstreams_finished"].([]any)) != 1 {
		t.Fatal(week)
	}
	if len(in.Workstreams) != 3 || in.Workstreams[0].Key != "p:p-ws" || in.Workstreams[0].Name != "Billing" || in.Workstreams[1].Name != "Docs site" || in.Workstreams[2].Key != Other {
		t.Fatalf("%+v", in.Workstreams)
	}
	if in.Workstreams[0].Progress != 60 || !in.Workstreams[1].Done {
		t.Fatalf("%+v", in.Workstreams)
	}
	if in.Today != "Friday, Nov 20" || in.Yesterday != "Thursday, Nov 19" || in.Week != "Mon Nov 16 to now" {
		t.Fatal(in.Today, in.Yesterday, in.Week)
	}
}

func s(text string, sources ...string) overview.Sentence {
	return overview.Sentence{Text: text, Sources: sources}
}

func TestCheckKeepsOnlySentencesTheEvidenceBacks(t *testing.T) {
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	out := Output{
		Today: []Group{
			{Workstream: "p:p-ws", Sentences: []overview.Sentence{
				s("I'm rolling out the retry fix in today's deploy window.", "step:p-ws.2.1"),
				s("I'm soaking it today.", "step:p-ws.2.2"), // only a later wait
			}},
		},
		Yesterday: []Group{
			{Workstream: "p:p-ws", Sentences: []overview.Sentence{
				s("I built the retry key.", "bead:p:p-ws.1", "report:p:p-ws.1"),
				s("I finished the rollout.", "goal:p:p-ws"),    // the board is not yesterday
				s("I built p-ws.1.", "bead:p:p-ws.1"),          // a bead id in the prose
				s("I shipped the docs search.", "linear:XY-9"), // another workstream's record
				s("I invented this.", "bead:p:nope"),           // unknown evidence
				s("", "bead:p:p-ws.1"),                         // empty
			}},
			{Workstream: "q:q-done", Sentences: []overview.Sentence{s("I shipped the new docs search.", "linear:XY-9")}},
			{Workstream: "p:p-ws", Sentences: []overview.Sentence{s("I approved the retry key.", "bead:p:p-ws.1.6")}},
		},
		Week: []Group{
			{Workstream: "q:q-done", Sentences: []overview.Sentence{s("The docs search is done.", "week:q:q-done")}},
			{Workstream: "p:p-ws", Sentences: []overview.Sentence{s("Retries that charge once are about 60% there.", "goal:p:p-ws")}},
		},
	}
	got, dropped := Check(out, in)
	if dropped != 6 {
		t.Fatalf("dropped %d", dropped)
	}
	if len(got.Today) != 1 || len(got.Today[0].Sentences) != 1 || got.Today[0].Name != "Billing" || got.Today[0].Progress != nil {
		t.Fatalf("%+v", got.Today)
	}
	if len(got.Yesterday) != 2 || got.Yesterday[0].Workstream != "p:p-ws" || len(got.Yesterday[0].Sentences) != 2 || got.Yesterday[1].Name != "Docs site" {
		t.Fatalf("groups merge per workstream, in board order: %+v", got.Yesterday)
	}
	if len(got.Week) != 2 || got.Week[0].Workstream != "p:p-ws" || *got.Week[0].Progress != 60 || *got.Week[1].Progress != 100 {
		t.Fatalf("%+v", got.Week)
	}
}

func TestClaudeReaderIsReadOnlyAndUsesTheBriefSchema(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv")
	envelope := `{"type":"result","is_error":false,"structured_output":{"today":[],"yesterday":[{"workstream":"p:p-ws","sentences":[{"text":"I built the retry key.","sources":["bead:p:p-ws.1"]}]}],"week":[]}}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BRIEF_CAPTURE\"\ncat > \"$BRIEF_CAPTURE.in\"\nprintf '%s\\n' '" + envelope + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	out, err := Claude("sonnet", Prompt, []string{"BRIEF_CAPTURE=" + capture})(context.Background(), in)
	if err != nil || len(out.Yesterday) != 1 {
		t.Fatal(out, err)
	}
	raw, _ := os.ReadFile(capture)
	args := string(raw)
	for _, flag := range []string{"--safe-mode\n", "--tools\n\n", "--strict-mcp-config\n", "--no-session-persistence\n", "--model\nsonnet\n", "--json-schema\n"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %q: %s", flag, args)
		}
	}
	if !strings.Contains(args, `"enum":["p:p-ws","q:q-done","other"]`) || !strings.Contains(args, `"bead:p:p-ws.1"`) {
		t.Fatal("the schema names only known workstreams and evidence:", args)
	}
	stdin, _ := os.ReadFile(capture + ".in")
	if !strings.Contains(string(stdin), `"evidence"`) || strings.Contains(string(stdin), `"ids"`) {
		t.Fatal(string(stdin))
	}
}

func TestPromptAsksForFirstPersonPhaseVerbsAndNoIDs(t *testing.T) {
	for _, want := range []string{`written as "I"`, "I'm shipping", "rolled out", "No bead ids", "only when it comes due today", "Never write \"you\""} {
		if !strings.Contains(Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestReadLinearReadsAssignedTicketsWithTheSavedKey(t *testing.T) {
	home := t.TempDir()
	if _, note := ReadLinear(context.Background(), home, time.Now(), nil, ""); note != "Linear is not connected" {
		t.Fatal(note)
	}
	if err := linearsync.WriteKey(home, "lin_api_testkey"); err != nil {
		t.Fatal(err)
	}
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_testkey" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		query = string(b)
		io.WriteString(w, `{"data":{"viewer":{"assignedIssues":{"nodes":[{"identifier":"ABC-1","title":"Retries","updatedAt":"2026-11-19T15:00:00Z","startedAt":"2026-11-18T10:00:00Z","completedAt":"2026-11-19T15:00:00Z","state":{"name":"Done","type":"completed"}}]}}}}`)
	}))
	defer srv.Close()
	got, note := ReadLinear(context.Background(), home, time.Date(2026, 11, 16, 5, 0, 0, 0, time.UTC), srv.Client().Do, srv.URL)
	if note != "" || len(got) != 1 || got[0].ID != "ABC-1" || got[0].StateType != "completed" || !got[0].CompletedAt.Equal(time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)) {
		t.Fatal(got, note)
	}
	if !strings.Contains(query, `updatedAt: {gte: \"2026-11-16T05:00:00Z\"}`) || !strings.Contains(query, "assignedIssues") {
		t.Fatal(query)
	}
}

// fakeService is a Service over a temp home with a fixed clock and counted reader runs.
func fakeService(t *testing.T, home string, now *time.Time, fail bool) (*Service, *atomic.Int32) {
	var runs atomic.Int32
	gather := func(context.Context, Windows) (Sources, error) { return sources(*now), nil }
	generate := func(_ context.Context, in Input) (Output, error) {
		runs.Add(1)
		if fail {
			return Output{}, errors.New("reader down")
		}
		return Output{Yesterday: []Group{{Workstream: "p:p-ws", Sentences: []overview.Sentence{s("I built the retry key.", "bead:p:p-ws.1")}}}}, nil
	}
	svc := NewService(home, gather, generate)
	svc.now = func() time.Time { return *now }
	return svc, &runs
}

func TestServiceRewritesAtTheSlotNotAtStartAndKeepsTheLastBrief(t *testing.T) {
	home := t.TempDir()
	now := et(2026, 11, 20, 6, 50)
	svc, runs := fakeService(t, home, &now, false)
	if st := svc.Status(); st.Brief != nil || st.Generating || !st.NextAt.Equal(et(2026, 11, 20, 7, 0)) {
		t.Fatalf("%+v", st)
	}
	next := NextSlot(now, Zone) // what Run starts with
	now = et(2026, 11, 20, 6, 59)
	if next = svc.tick(next); runs.Load() != 0 || !next.Equal(et(2026, 11, 20, 7, 0)) {
		t.Fatal("before the slot: nothing", runs.Load(), next)
	}
	now = et(2026, 11, 20, 7, 0)
	next = svc.tick(next)
	svc.Wait()
	if runs.Load() != 1 || !next.Equal(et(2026, 11, 20, 13, 0)) {
		t.Fatal("at 7 AM: one rewrite, then 1 PM", runs.Load(), next)
	}
	// A machine that slept through 1 PM catches it once when it wakes.
	now = et(2026, 11, 20, 15, 10)
	next = svc.tick(next)
	svc.Wait()
	if runs.Load() != 2 || !next.Equal(et(2026, 11, 20, 19, 0)) {
		t.Fatal(runs.Load(), next)
	}
	st := svc.Status()
	if st.Brief == nil || len(st.Brief.Yesterday) != 1 || st.Brief.Yesterday[0].Name != "Billing" || len(st.Evidence) == 0 {
		t.Fatalf("%+v", st)
	}
	info, err := os.Stat(filepath.Join(home, FileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}

	// A restart shows the saved Brief and writes nothing until the next slot.
	again, runs2 := fakeService(t, home, &now, false)
	st2 := again.Status()
	if st2.Brief == nil || !st2.Brief.GeneratedAt.Equal(st.Brief.GeneratedAt) || runs2.Load() != 0 {
		t.Fatalf("%+v", st2)
	}
	if again.tick(NextSlot(now, Zone)); runs2.Load() != 0 {
		t.Fatal("no rewrite at start")
	}
}

func TestStartRunsOneRewriteAtATimeAndAFailureKeepsTheLastBrief(t *testing.T) {
	home := t.TempDir()
	now := et(2026, 11, 20, 9, 0)
	ok, _ := fakeService(t, home, &now, false)
	if !ok.Start() {
		t.Fatal("first Start runs")
	}
	ok.Wait()
	good := ok.Status().Brief

	failing, runs := fakeService(t, home, &now, true)
	block := make(chan struct{})
	gate := failing.generate
	failing.generate = func(ctx context.Context, in Input) (Output, error) { <-block; return gate(ctx, in) }
	if !failing.Start() || failing.Start() {
		t.Fatal("a second Start while one runs starts nothing")
	}
	if st := failing.Status(); !st.Generating || st.Brief == nil {
		t.Fatalf("%+v", st)
	}
	close(block)
	failing.Wait()
	st := failing.Status()
	if runs.Load() != 1 || st.Generating || st.Error == "" || st.Brief == nil || !st.Brief.GeneratedAt.Equal(good.GeneratedAt) {
		t.Fatalf("%+v", st)
	}
}
