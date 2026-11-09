package boardturn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func row(id, kind, text string) Entry {
	return Entry{ID: id, Label: "#" + id, Kind: kind, Text: text, Key: text, BeadID: "b" + id, Track: len(id) > 1}
}

func rows(n int) []Entry {
	var es []Entry
	for i := 1; i <= n; i++ {
		es = append(es, row(string(rune('0'+i)), "working", "Row: Working"))
	}
	return es
}

func TestWriter_VersionMovesOnlyWhenALineSaysSomethingNew(t *testing.T) {
	home := t.TempDir()
	w := &Writer{Home: home}
	now := time.Unix(1_800_000_000, 0)
	es := rows(3)
	if v := w.Write(map[string][]Entry{"commander": es}, now)["commander"]; v != 1 {
		t.Fatalf("first version %d", v)
	}
	aged := append([]Entry(nil), es...)
	aged[0].Text = "Row: Working (for 5m)" // same Key: only the age moved
	if v := w.Write(map[string][]Entry{"commander": aged}, now.Add(time.Minute))["commander"]; v != 1 {
		t.Fatalf("an age bumped the version to %d", v)
	}
	changed := append([]Entry(nil), es...)
	changed[0].Key, changed[0].Text = "Row: Stalled", "Row: Stalled"
	if v := w.Write(map[string][]Entry{"commander": changed}, now.Add(2*time.Minute))["commander"]; v != 2 {
		t.Fatalf("a changed line left version %d", v)
	}
	txt, _ := os.ReadFile(filepath.Join(Dir(home), "commander.txt"))
	if !strings.HasPrefix(string(txt), "board v2 @ ") || !strings.Contains(string(txt), "#1 Row: Stalled") {
		t.Fatalf("txt copy: %q", txt)
	}
	// A new cmdrd carries on from the files, never reusing a version.
	w2 := &Writer{Home: home}
	if v := w2.Write(map[string][]Entry{"commander": changed}, now.Add(3*time.Minute))["commander"]; v != 2 {
		t.Fatalf("restart moved the version to %d", v)
	}
	// A Manager whose workstream left the board loses its file.
	w2.Write(map[string][]Entry{"commander": changed, "manager-x": es[:1]}, now.Add(4*time.Minute))
	w2.Write(map[string][]Entry{"commander": changed}, now.Add(5*time.Minute))
	if _, err := os.Stat(filePath(home, "manager-x")); !os.IsNotExist(err) {
		t.Fatalf("stale manager board kept: %v", err)
	}
}

func TestDeliver_FullThenDiffThenUnchanged(t *testing.T) {
	home := t.TempDir()
	w := &Writer{Home: home}
	now := time.Unix(1_800_000_000, 0)
	es := rows(6)
	w.Write(map[string][]Entry{"commander": es}, now)

	text, commit := Deliver(home, "commander", now)
	if !strings.Contains(text, "The user's board v1 at") || !strings.Contains(text, "#6 Row: Working") {
		t.Fatalf("first delivery is not the full board: %q", text)
	}
	commit()
	if text, _ := Deliver(home, "commander", now); text != "The user's board v1: unchanged since you last saw it." {
		t.Fatalf("unchanged: %q", text)
	}

	next := append([]Entry(nil), es...)
	next[2] = row("3", "stalled", "Row: Stalled")
	next = append(next, row("3B", "working", "Track: Working"))
	w.Write(map[string][]Entry{"commander": next}, now.Add(time.Minute))
	text, commit = Deliver(home, "commander", now.Add(time.Minute))
	want := "The user's board v2 (since v1, which you saw; every other line still reads as you saw it):\n~ #3 Row: Stalled\n+ #3B Track: Working"
	if text != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", text, want)
	}
	// Not committed yet (as if the hook's print failed): the same diff again.
	if again, _ := Deliver(home, "commander", now.Add(time.Minute)); again != want {
		t.Fatalf("an unprinted delivery counted as seen: %q", again)
	}
	commit()

	// Compaction: the next one is full.
	MarkFull(home, "commander")
	if text, _ := Deliver(home, "commander", now.Add(time.Minute)); !strings.Contains(text, "exactly as the user's sidebar shows it") {
		t.Fatalf("after compaction: %q", text)
	}
}

func TestDeliver_FullWhenMuchChangedOrDue(t *testing.T) {
	home := t.TempDir()
	w := &Writer{Home: home}
	now := time.Unix(1_800_000_000, 0)
	es := rows(6)
	w.Write(map[string][]Entry{"commander": es}, now)
	_, commit := Deliver(home, "commander", now)
	commit()

	many := rows(6)
	for i := 0; i < 3; i++ {
		many[i].Key, many[i].Text = "Row: Stalled", "Row: Stalled"
	}
	w.Write(map[string][]Entry{"commander": many}, now)
	if text, _ := Deliver(home, "commander", now); !strings.Contains(text, "exactly as the user's sidebar") {
		t.Fatalf("half the rows changed, got a diff: %q", text)
	}

	// Every FullEvery deliveries the whole board comes again.
	w.Write(map[string][]Entry{"commander": es}, now)
	MarkFull(home, "commander")
	_, commit = Deliver(home, "commander", now)
	commit()
	var text string
	for i := 1; i <= FullEvery; i++ {
		text, commit = Deliver(home, "commander", now)
		commit()
		if full := strings.Contains(text, "exactly as"); full != (i == FullEvery) {
			t.Fatalf("delivery %d full=%v: %q", i, full, text)
		}
	}
	// And after FullAfter, whatever the count.
	w.Write(map[string][]Entry{"commander": es}, now.Add(FullAfter))
	if text, _ := Deliver(home, "commander", now.Add(FullAfter)); !strings.Contains(text, "exactly as") {
		t.Fatalf("30 min on: %q", text)
	}
}

func TestDeliver_StaleOrMissingBoardIsUnavailable(t *testing.T) {
	home := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	if text, _ := Deliver(home, "commander", now); !strings.Contains(text, "unavailable: cmdrd is not updating it (no board file)") {
		t.Fatalf("missing: %q", text)
	}
	(&Writer{Home: home}).Write(map[string][]Entry{"commander": rows(2)}, now)
	if text, _ := Deliver(home, "commander", now.Add(MaxAge+time.Second)); !strings.Contains(text, "unavailable") || strings.Contains(text, "#1") {
		t.Fatalf("stale board served: %q", text)
	}
	for _, role := range []string{"operator-x", "../commander", "manager-../x", ""} {
		if text, _ := Deliver(home, role, now); text != "" {
			t.Fatalf("%q got a board: %q", role, text)
		}
	}
}

func TestAfterWrite_ShowsTheLinesTheWriteTouched(t *testing.T) {
	home := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	es := []Entry{row("1", "working", "One: Working"), row("2", "stalled", "Two: Stalled"), row("2B", "waiting", "Track: Waiting for CI")}
	(&Writer{Home: home}).Write(map[string][]Entry{"commander": es}, now)
	got := AfterWrite(home, "commander", "cmdr internal-set-objective --project p --bead b2B.4 --state in-progress", now)
	if got != "After that write, the user's board v1 reads:\n#2B Track: Waiting for CI" {
		t.Fatalf("got %q", got)
	}
	if got := AfterWrite(home, "commander", "cmdr internal-set-objective --bead zz9", now); got != "" {
		t.Fatalf("unrelated bead: %q", got)
	}
}

func TestCheckClaims(t *testing.T) {
	f := File{Version: 124, Entries: []Entry{
		row("2", "working", "Two: Working"),
		row("3", "working", "Three: Working"),
		row("3B", "stalled", "Track: Agent stopped on review: the Manager's move"),
		row("4", "stuck", "Four: stuck"),
		row("5", "stalled", "Five: State missing"),
		{ID: "ask:k", Label: "ask #1", Kind: "needs", Text: "Approve it", Ask: true},
	}}
	for _, c := range []struct {
		reply string
		want  []string
	}{
		{"#3b is fixed now.", []string{`Board v124 #3B reads "Track: Agent stopped on review: the Manager's move". Your reply says it is fixed.`}},
		{"Done: #2 and #5 are resolved.", []string{"#5 reads", "says it is resolved"}},
		{"#4, #3B are green", []string{"#4 reads", "#3B reads"}},
		{"The board is accurate now: #5 reads State missing and is unblocked.", []string{"#5 reads"}},
		{"#2 is fixed.", nil},                                    // row is fine
		{"#3B is not fixed yet.", nil},                           // negated
		{"#3B will be fixed once CI passes.", nil},               // future
		{"I'll mark #5 resolved once the Manager answers.", nil}, // intent, not a report
		{"#2 is fixed, but #3B is still stalled.", nil},          // the claim is about #2
		{"#3B still reads `Stalled` so nothing is fixed.", nil},
		{"> #3B is fixed\nThat was the Manager's claim; it is wrong.", nil},
		{"The Manager said \"#3B is fixed\" but it reads stalled.", nil},
		{"PR #5 is green.", nil},
		{"ask #1 is resolved.", nil},
		{"Is #3B fixed? Checking.", nil},
	} {
		got := CheckClaims(f, c.reply)
		if c.want == nil && got != "" {
			t.Errorf("%q: false positive: %s", c.reply, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%q: missing %q in %q", c.reply, w, got)
			}
		}
	}
}
