package decisions

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func TestAddListRetire(t *testing.T) {
	home := t.TempDir()
	g, err := Add(home, Decision{Words: "catalog changes require CI", AddedBy: "commander"}, now)
	if err != nil || g.ID != "d1" || g.Scope != Global || g.Date != "2026-10-28" {
		t.Fatalf("add: %+v %v", g, err)
	}
	w, err := Add(home, Decision{Words: "no deploy windows unless asked", Rule: "no-deploy-window", Scope: "ws-1", Date: "2026-10-24"}, now)
	if err != nil || w.ID != "d2" {
		t.Fatalf("add scoped: %+v %v", w, err)
	}
	if again, _ := Add(home, Decision{Words: "catalog changes require CI"}, now); again.ID != "d1" {
		t.Fatalf("the same call twice is one decision, got %s", again.ID)
	}
	for _, bad := range []Decision{{Words: " "}, {Words: "x", Rule: "deploy window"}, {Words: "x", Date: "Oct 24"}, {Words: strings.Repeat("x", MaxWords+1)}} {
		if _, err := Add(home, bad, now); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	all, _ := Load(home)
	if got := ActiveIn(all, "ws-2"); len(got) != 1 || got[0].ID != "d1" {
		t.Fatalf("another workstream sees only the global one: %+v", got)
	}
	if got := ActiveIn(all, "ws-1.3", "ws-1"); len(got) != 2 {
		t.Fatalf("a bead under ws-1 sees both: %+v", got)
	}
	if _, err := Retire(home, "d1", "commander", "the user wants CI skipped again", now); err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(home, "d1", "commander", "", now); err == nil {
		t.Fatal("retired twice")
	}
	all, _ = Load(home)
	if got := ActiveIn(all); len(got) != 1 || got[0].ID != "d2" || len(all) != 2 {
		t.Fatalf("retired stays on record but out of force: %+v", all)
	}
	if d, _ := Add(home, Decision{Words: "only heavy local work waits"}, now); d.ID != "d3" {
		t.Fatalf("ids never reuse: %s", d.ID)
	}
}

func TestContradicts_RuleKeyOnWholeWords(t *testing.T) {
	ds := []Decision{{ID: "d2", Words: "no deploy windows unless asked", Rule: "no-deploy-window", Scope: Global}}
	if d := Contradicts(ds, "", "the 6 AM deploy window opens"); d == nil || d.ID != "d2" {
		t.Fatal("a wait on the deploy window contradicts no-deploy-window")
	}
	if d := Contradicts(ds, "Deploy-Window: prod"); d == nil {
		t.Fatal("case and punctuation do not matter")
	}
	for _, ok := range []string{"the redeploy windows close", "deploy to prod", "window cleaning"} {
		if Contradicts(ds, ok) != nil {
			t.Fatalf("%q does not name the rule", ok)
		}
	}
	if Contradicts([]Decision{{ID: "d1", Words: "no catalog windows"}}, "catalog window") != nil {
		t.Fatal("words are never matched, only rule keys")
	}
	ds[0].RetiredAt = 1
	if Contradicts(ds, "deploy window") != nil {
		t.Fatal("a retired decision refuses nothing")
	}
}

func TestDeliver_OncePerContextAndScopedToTheManager(t *testing.T) {
	home := t.TempDir()
	if text, _ := Deliver(home, "manager-ws-1", now); text != "" {
		t.Fatalf("nothing recorded, nothing said: %q", text)
	}
	Add(home, Decision{Words: "catalog changes require CI"}, now)
	Add(home, Decision{Words: "no deploy windows unless asked", Rule: "no-deploy-window", Scope: "ws-1"}, now)
	Add(home, Decision{Words: "only heavy local work waits", Scope: "ws-2"}, now)

	text, commit := Deliver(home, "manager-ws-1", now)
	for _, want := range []string{"standing decisions", `d1 (global, 2026-10-28): "catalog changes require CI"`, "rule no-deploy-window"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "heavy local work") {
		t.Fatalf("another workstream's decision reached this Manager:\n%s", text)
	}
	commit()
	if again, _ := Deliver(home, "manager-ws-1", now.Add(time.Minute)); again != "" {
		t.Fatalf("the context already holds it: %q", again)
	}
	if again, _ := Deliver(home, "manager-ws-1", now.Add(RepeatAfter)); again == "" {
		t.Fatal("repeated after RepeatAfter")
	}
	MarkFull(home, "manager-ws-1")
	if again, _ := Deliver(home, "manager-ws-1", now.Add(time.Minute)); again == "" {
		t.Fatal("a new context gets the list again")
	}
	_, commit = Deliver(home, "manager-ws-1", now)
	commit()
	Retire(home, "d1", "commander", "", now)
	if text, _ := Deliver(home, "manager-ws-1", now.Add(time.Minute)); strings.Contains(text, "catalog changes require CI") || !strings.Contains(text, "deploy") {
		t.Fatalf("a retired decision is delivered as gone at once:\n%s", text)
	}
	if text, _ := Deliver(home, "commander", now); !strings.Contains(text, "heavy local work") || !strings.Contains(text, "deploy") {
		t.Fatalf("Commander reads every decision:\n%s", text)
	}
	if text, _ := Deliver(home, "operator-x", now); text != "" {
		t.Fatalf("Operators get none: %q", text)
	}
}

func TestScopedCommanderRetainsStandingUserDecisions(t *testing.T){
 home:=t.TempDir()
 if _,e:=Add(home,Decision{Words:"Keep the release hold",Scope:Global},now);e!=nil{t.Fatal(e)}
 if _,e:=Add(home,Decision{Words:"Keep this branch paused",Scope:"urgent-work"},now);e!=nil{t.Fatal(e)}
 all,e:=Load(home);if e!=nil{t.Fatal(e)}
 if got:=ForRole(all,"commander-urgent");len(got)!=2{t.Fatalf("scoped Commander lost standing direction: %+v",got)}
 text,commit:=Deliver(home,"commander-urgent",now);if !strings.Contains(text,"Keep the release hold")||!strings.Contains(text,"Keep this branch paused"){t.Fatal("standing directions were absent from its turn",text)};commit()
}
