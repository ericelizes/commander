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

// byRef is the evidence by the record it names.
func byRef(in Input) map[string]Evidence {
	m := map[string]Evidence{}
	for _, e := range in.Evidence {
		m[e.Ref] = e
	}
	return m
}

func TestAssembleTagsEachRecordWithItsWorkstreamAndWindow(t *testing.T) {
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	ev := byRef(in)
	for ref, want := range map[string][2]string{
		"goal:p:p-ws":     {"Billing", "board"},
		"step:p-ws.2.1":   {"Billing", "board"}, // its wait comes due today
		"step:p-ws.2.2":   {"Billing", "later"}, // Monday's soak is not today
		"step:p-ws.3":     {"Billing", "board"},
		"bead:p:p-ws.1":   {"Billing", "yesterday"},
		"bead:p:p-ws.1.9": {"Billing", "today"},
		"bead:p:p-ws.1.6": {"Billing", "yesterday"}, // the user's own approval
		"bead:q:q-loose":  {"Other", "yesterday"},
		"report:p:p-ws.1": {"Billing", "yesterday"},
		"linear:ABC-1":    {"Billing", "board"}, // joined through the board's ticket
		"linear:XY-9":     {"Docs site", "yesterday"},
		"week:p:p-ws":     {"Billing", "week"},
		"week:q:q-done":   {"Docs site", "week"},
	} {
		e, ok := ev[ref]
		if !ok || e.Workstream != want[0] || e.When != want[1] {
			t.Errorf("%s: %+v, want %v", ref, e, want)
		}
	}
	for _, ref := range []string{"step:p-ws.4", "step:p-ws.5", "bead:p:p-ws.1.8", "bead:p:p-ws.1.7", "bead:p:p-ws.1.5", "linear:ZZ-3", "bead:q:q-done"} {
		if _, ok := ev[ref]; ok {
			t.Errorf("%s should not be evidence", ref)
		}
	}
	for ref, cite := range map[string]string{"step:p-ws.2.1": "bead:p-ws.2.1", "report:p:p-ws.1": "bead:p-ws.1", "goal:p:p-ws": "bead:p-ws", "linear:XY-9": "linear:XY-9", "week:q:q-done": "bead:q-done"} {
		if ev[ref].ID != cite {
			t.Errorf("%s cites as %q, want %q", ref, ev[ref].ID, cite)
		}
	}
	if !strings.Contains(ev["report:p:p-ws.1"].Text, "PASS, merged.") || strings.Contains(ev["report:p:p-ws.1"].Text, "older") {
		t.Fatal("the latest report per bead", ev["report:p:p-ws.1"])
	}
	step := ev["step:p-ws.2.1"].Text
	if !strings.Contains(step, `"happens_today":true`) || !strings.Contains(step, `"part_of":"Rollout"`) {
		t.Fatal(step)
	}
	if mine := ev["step:p-ws.3"].Text; !strings.Contains(mine, `"kind":"you"`) || !strings.Contains(mine, `"owner":"You"`) {
		t.Fatal("the board's actor reaches the reader:", mine)
	}
	if !strings.Contains(ev["goal:p:p-ws"].Text, `"percent_done":60`) || !strings.Contains(ev["goal:p:p-ws"].Text, "A retry charges once.") {
		t.Fatal(ev["goal:p:p-ws"].Text)
	}
	var week map[string]any
	json.Unmarshal([]byte(ev["week:q:q-done"].Text), &week)
	if week["finished_this_week"] != 1.0 || len(week["workstreams_finished"].([]any)) != 1 {
		t.Fatal(week)
	}
	if len(in.Workstreams) != 2 || in.Workstreams[0].Name != "Billing" || in.Workstreams[1].Name != "Docs site" {
		t.Fatalf("%+v", in.Workstreams)
	}
	if *in.Workstreams[0].Progress != 60 || in.Workstreams[1].Progress != nil || !in.Workstreams[1].Done {
		t.Fatalf("%+v", in.Workstreams)
	}
	if in.Today != "Friday, Nov 20" || in.Yesterday != "Thursday, Nov 19" || in.Week != "Mon Nov 16 to now" {
		t.Fatal(in.Today, in.Yesterday, in.Week)
	}
}

// doc writes a writer's document, citing records by their names ({ref}).
func doc(in Input, md string) string {
	for _, e := range in.Evidence {
		md = strings.ReplaceAll(md, "{"+e.Ref+"}", e.ID)
	}
	return md
}

func TestCheckKeepsEachSentenceAndBulletTheEvidenceBacks(t *testing.T) {
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	md := doc(in, `Here is my brief.

## Moving today

Today I'm rolling out the retry fix in the deploy window. [^{step:p-ws.2.1}] I'm soaking it until Monday. [^{step:p-ws.2.2}] And something uncited.

- **Billing:** I'm rolling out retries that charge once. [^{step:p-ws.2.1}][^{linear:ABC-1}]
- **Needs me:** approve the rollout. [^{step:p-ws.3}]
- **Waiting on:** the Monday soak. [^{step:p-ws.2.2}]

## Done yesterday

Yesterday I built the retry key [^{bead:p:p-ws.1}, ^{report:p:p-ws.1}]. I finished the rollout. [^{goal:p:p-ws}]

- **Billing:** I built the retry key and approved it. [^{bead:p:p-ws.1}][^{bead:p:p-ws.1.6}]
  It passed review. [^{report:p:p-ws.1}]
- **Billing:** I built p-ws.1. [^{bead:p:p-ws.1}]
- I invented this. [^e999]
- I cited nothing.
- **Docs site:** I shipped the new docs search. [^{linear:XY-9}]

## Something else

- ignored [^{bead:p:p-ws.1}]

## This week

The week was about retries. [^{goal:p:p-ws}][^{week:p:p-ws}]

- **Billing:** retries that charge once, about 60% there. [^{goal:p:p-ws}]
- **Docs site:** done. [^{week:q:q-done}]
`)
	got, dropped := Check(md, in)
	// Dropped: two today sentences (a later wait, uncited), the later-only bullet,
	// the board-only sentence yesterday, and the id, unknown and uncited bullets.
	if dropped != 7 {
		t.Fatalf("dropped %d: %+v", dropped, got)
	}
	if len(got) != 3 || got[0].Title != "Moving today" || got[1].Title != "Done yesterday" || got[2].Title != "This week" {
		t.Fatalf("%+v", got)
	}
	today := got[0]
	if len(today.Paragraph) != 1 || today.Paragraph[0].Text != "Today I'm rolling out the retry fix in the deploy window." || len(today.Bullets) != 2 || today.Bullets[1].Text != "**Needs me:** approve the rollout." {
		t.Fatalf("%+v", today)
	}
	y := got[1]
	if len(y.Paragraph) != 1 || y.Paragraph[0].Text != "Yesterday I built the retry key." || len(y.Paragraph[0].Sources) != 2 {
		t.Fatalf("markers mid-sentence, then the period: %+v", y.Paragraph)
	}
	if len(y.Bullets) != 2 || y.Bullets[0].Text != "**Billing:** I built the retry key and approved it. It passed review." || len(y.Bullets[0].Sources) != 3 {
		t.Fatalf("a bullet's continuation line is part of it: %+v", y.Bullets)
	}
	if len(got[2].Bullets) != 2 || len(got[2].Paragraph) != 1 {
		t.Fatalf("%+v", got[2])
	}
	want := "## Moving today\n\nToday I'm rolling out the retry fix in the deploy window.\n\n- **Billing:** I'm rolling out retries that charge once.\n- **Needs me:** approve the rollout.\n"
	if out := Markdown(got); !strings.HasPrefix(out, want) || strings.Contains(out, "[^") || !strings.Contains(out, "\n## Done yesterday\n") {
		t.Fatalf("%q", out)
	}
	if empty, n := Check("", in); n != 0 || len(empty) != 3 || len(empty[0].Bullets) != 0 {
		t.Fatal(empty, n)
	}
}

func TestAgentRunHasOnlyTheReadCommandsAndTheGuard(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv")
	envelope := `{"type":"result","is_error":false,"num_turns":3,"structured_output":{"markdown":"## Moving today\n\n- I am. [^bead:p-ws.3]"}}`
	// The fake runs one command through the guard's counter, as the hook would.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BRIEF_CAPTURE\"\nenv > \"$BRIEF_CAPTURE.env\"\ncat > \"$BRIEF_CAPTURE.in\"\nfor i in 1 2 3 4 5 6 7 8 9 10; do echo 'gh pr view 1' >> \"$COMMANDER_BRIEF_RUN/calls\"; done\nprintf '%s\\n' '" + envelope + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDECODE", "1")
	now := et(2026, 11, 20, 9, 0)
	in := Assemble(WindowsAt(now, Zone), sources(now))
	md, calls, err := RunAgent(context.Background(), "sonnet", AgentPrompt, in, []string{"BRIEF_CAPTURE=" + capture})
	if err != nil || md != "## Moving today\n\n- I am. [^bead:p-ws.3]" || calls != MinToolCalls {
		t.Fatalf("%q %d %v", md, calls, err)
	}
	raw, _ := os.ReadFile(capture)
	args := string(raw)
	for _, flag := range []string{"--tools\nBash\n", "--permission-mode\ndontAsk\n", "--setting-sources\n\n", "--strict-mcp-config\n", "--no-session-persistence\n", "--model\nsonnet\n", "--json-schema\n", "cmdr internal-brief-guard", `"matcher":"Bash|StructuredOutput"`, "StructuredOutput,Bash(cmdr workstream-statuses:*)", "Bash(cmdr internal-brief-read:*)", "Bash(gh api --method GET:*)"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %q: %s", flag, args)
		}
	}
	if strings.Contains(args, "--safe-mode") {
		t.Fatal("--safe-mode would turn the guard hook off")
	}
	if allowed := args[strings.Index(args, "--allowedTools\n"):]; strings.Contains(allowed, "Bash(gh pr merge") || strings.Contains(allowed, "Edit") {
		t.Fatal(allowed)
	}
	env, _ := os.ReadFile(capture + ".env")
	if !strings.Contains(string(env), RunDirEnv+"=") || strings.Contains(string(env), "CLAUDECODE=") {
		t.Fatal("the run gets its own counter and not the parent's session:", string(env))
	}
	stdin, _ := os.ReadFile(capture + ".in")
	if !strings.Contains(string(stdin), `"id":"bead:p-ws.2.1"`) {
		t.Fatal(string(stdin))
	}
}

func TestGuardLetsOnlyPlainReadCommandsRun(t *testing.T) {
	for _, c := range []string{
		"cmdr workstream-statuses",
		"cmdr workstream-statuses --step vibe-apps-zq9h.23",
		`cmdr internal-brief-read sql "SELECT id, title FROM bead_tasks WHERE updated_at >= '2026-11-19' AND status = 'closed'"`,
		"cmdr internal-brief-read linear ABC-12",
		"gh pr view 212 -R acme/app --json state,mergedAt",
		`gh pr view 212 -R acme/app --json state --jq '.state | ascii_downcase'`,
		"gh run list -R acme/app --workflow deploy.yml --limit 5",
		"gh api --method GET repos/acme/app/actions/runs/1",
	} {
		if ok, why := GuardCommand(c); !ok {
			t.Errorf("refused %q: %s", c, why)
		}
	}
	for _, c := range []string{
		"", "ls", "cat ~/.commander/api-token", "cmdr bead close x", "cmdr internal-brief-readx",
		"gh pr merge 212 -R acme/app", "gh pr comment 212 -b hi", "gh pr view 212; gh pr merge 212",
		"gh pr view 212 && rm -rf /", "gh pr view 212 | sh", "gh pr view 212 > /tmp/x",
		"gh pr view $(cat /etc/passwd)", "gh pr view `id`", "gh pr view 1\ngh pr merge 1",
		"gh api --method GET repos/a/b -f x=1", "gh api --method GET --method POST repos/a/b", "gh api repos/a/b",
		"gh api -X POST repos/a/b", "FOO=1 gh pr view 1", `cmdr internal-brief-read sql "unbalanced`,
	} {
		if ok, _ := GuardCommand(c); ok {
			t.Errorf("let %q run", c)
		}
	}
	dir := t.TempDir()
	for i := 0; i < MaxToolCalls; i++ {
		if why := Guard("gh pr view 1 -R a/b", dir); why != "" {
			t.Fatal(i, why)
		}
	}
	if why := Guard("gh pr view 1 -R a/b", dir); !strings.Contains(why, "budget") || ToolCalls(dir) != MaxToolCalls+1 {
		t.Fatal("past the budget every command is refused:", why)
	}
	if why := Guard("gh pr merge 1", t.TempDir()); !strings.Contains(why, "only reads") {
		t.Fatal(why)
	}
}

func TestReadOnlySQLAllowsOneQueryOnly(t *testing.T) {
	for _, q := range []string{
		"SELECT id FROM bead_tasks",
		"select id from bead_tasks where close_reason like '%release%' and text <> 'drop; it'",
		"WITH c AS (SELECT bead_id FROM bead_comments) SELECT count(*) FROM c;",
		"SELECT id FROM bead_tasks WHERE jsonb_exists(labels, 'cmdr:release-deploy')",
	} {
		if err := ReadOnlySQL(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	for _, q := range []string{
		"", "DELETE FROM bead_tasks", "UPDATE bead_tasks SET title='x'", "SELECT 1; DROP TABLE projects",
		"WITH d AS (DELETE FROM bead_tasks RETURNING id) SELECT * FROM d", "SELECT * INTO copy FROM bead_tasks",
		"SELECT pg_sleep(60)", "SELECT 1 -- comment", "SELECT $$x$$", "SELECT set_config('a','b',false)",
		"COPY bead_tasks TO '/tmp/x'", "SELECT lo_export(1, '/tmp/x')",
	} {
		if err := ReadOnlySQL(q); err == nil {
			t.Errorf("allowed %q", q)
		}
	}
}

func TestReadIssueReadsOneTicket(t *testing.T) {
	home := t.TempDir()
	if _, err := ReadIssue(context.Background(), home, "ABC-1", nil, ""); err == nil {
		t.Fatal("no key, no read")
	}
	if err := linearsync.WriteKey(home, "lin_api_testkey"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIssue(context.Background(), home, `ABC-1") { x }`, nil, ""); err == nil {
		t.Fatal("only ticket ids")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "lin_api_testkey" || !strings.Contains(string(b), `"id":"ABC-1"`) || strings.Contains(string(b), "mutation") {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"data":{"issue":{"identifier":"ABC-1","title":"Retries","description":"Charge once.","state":{"name":"Done","type":"completed"},"assignee":{"name":"Sam"},"completedAt":"2026-11-19T15:00:00Z","comments":{"nodes":[{"body":"Deployed.","createdAt":"2026-11-19T15:00:00Z","user":{"name":"Sam"}}]}}}}`)
	}))
	defer srv.Close()
	text, err := ReadIssue(context.Background(), home, "ABC-1", srv.Client().Do, srv.URL)
	if err != nil || !strings.Contains(text, "ABC-1 (Done): Retries") || !strings.Contains(text, "Sam, 2026-11-19T15:00:00Z: Deployed.") {
		t.Fatal(text, err)
	}
}

func TestPromptAsksForOneInvestigatedDocument(t *testing.T) {
	for _, want := range []string{"INVESTIGATE FIRST", "Do not write from the briefing alone", "If you cannot confirm a deploy", "cmdr internal-brief-read sql", "gh run view", "only these read-only commands run", `write it as "I"`, "## Moving today", "## Done yesterday", "## This week", "I'm shipping", "rolled out", "**Needs me:**", "**Waiting on:**", "the board's owner decides", "No bead ids", "[^bead:", "a deploy still waiting is not shipped", "Never write \"you\""} {
		if !strings.Contains(AgentPrompt, want) {
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
	generate := func(_ context.Context, in Input) (string, error) {
		runs.Add(1)
		if fail {
			return "", errors.New("reader down")
		}
		return doc(in, "## Done yesterday\n\n- **Billing:** I built the retry key. [^{bead:p:p-ws.1}]\n"), nil
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
	if st.Brief == nil || len(st.Brief.Sections[1].Bullets) != 1 || !strings.Contains(st.Brief.Markdown, "- **Billing:** I built the retry key.") || len(st.Evidence) == 0 {
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
	failing.generate = func(ctx context.Context, in Input) (string, error) { <-block; return gate(ctx, in) }
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
