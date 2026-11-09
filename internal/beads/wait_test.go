package beads

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseWait(t *testing.T) {
	for arg, want := range map[string]Wait{
		"ci:#4101":                 {Kind: "ci", PR: 4101},
		"ci:acme/web#12":           {Kind: "ci", Repo: "acme/web", PR: 12},
		"main-ci:349a7f4":          {Kind: "main-ci", Sha: "349a7f4"},
		"main-ci:acme/web@349a7f4": {Kind: "main-ci", Repo: "acme/web", Sha: "349a7f4"},
		"prod-tag:349a7f4abc":      {Kind: "prod-tag", Sha: "349a7f4abc"},
		"review:reviewer-a":        {Kind: "review", Ref: "reviewer-a"},
		"user":                     {Kind: "user"},
		"operator:op-x-1":          {Kind: "operator", Ref: "op-x-1"},
	} {
		got, err := ParseWait(arg)
		if err != nil || got != want {
			t.Errorf("ParseWait(%q) = %+v, %v; want %+v", arg, got, err, want)
		}
	}
	for _, bad := range []string{"", "ci:4101", "ci:#x", "ci:a.b/c#1", "review:", "review:a b", "user:x", "slack:x", "main-ci:", "main-ci:xyz1234", "main-ci:abc", "prod-tag:a.b/c@349a7f4"} {
		if _, err := ParseWait(bad); err == nil {
			t.Errorf("ParseWait(%q) succeeded", bad)
		}
	}
}

func TestWaitLabelRoundTrip(t *testing.T) {
	since := time.Unix(1700000000, 0)
	for _, arg := range []string{"ci:#4101", "ci:acme/web#12", "main-ci:349a7f4", "main-ci:acme/web@349a7f4", "prod-tag:349a7f4", "review:reviewer-a", "user", "operator:op-x"} {
		w, _ := ParseWait(arg)
		w.Since = since
		got, ok := ParseWaitLabel(w.Label())
		if !ok || got != w {
			t.Errorf("%q: label %q -> %+v, %v", arg, w.Label(), got, ok)
		}
	}
	if _, ok := ParseWaitLabel("waiting-on-me"); ok {
		t.Error("parsed a foreign label")
	}
}

func TestMainCIAndProdTagText(t *testing.T) {
	ws := WaitsOf([]string{"waiting:1:main-ci:349a7f4e1f", "waiting:2:prod-tag:acme:web:349a7f4"})
	if got := WaitsText(ws); got != "main CI 349a7f4 · prod tag 349a7f4" {
		t.Errorf("WaitsText = %q", got)
	}
}

func TestWaitsText(t *testing.T) {
	ws := WaitsOf([]string{"owner:x", "waiting:20:review:reviewer-a", "waiting:10:ci:4101", "waiting:30:review:reviewer-b"})
	if got := WaitsText(ws); got != "CI #4101 · review (reviewer-a, reviewer-b)" {
		t.Errorf("WaitsText = %q", got)
	}
	if got := WaitsText(WaitsOf([]string{"waiting:1:user"})); got != "you" {
		t.Errorf("WaitsText = %q", got)
	}
}

func TestUntilWaitParsesAndRoundTrips(t *testing.T) {
	w, err := ParseWait("until:2030-01-01T12:00:00Z:Nightly reconcile: 12:00")
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != WaitUntil || w.Until.Unix() != 1893499200 || w.Ref != "Nightly reconcile: 12:00" {
		t.Fatalf("parsed: %+v", w)
	}
	w.Since = time.Unix(1000, 0)
	label := w.Label()
	if strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_:") != "" {
		t.Fatalf("label has chars br rejects: %q", label)
	}
	back, ok := ParseWaitLabel(label)
	if !ok || back.Ref != w.Ref || !back.Until.Equal(w.Until) || back.Text() != w.Ref {
		t.Fatalf("round trip: %+v ok=%v", back, ok)
	}
	// no label, and an offset time
	n, err := ParseWait("until:2030-01-01T14:00:00+02:00")
	if err != nil || n.Ref != "" || n.Until.Unix() != 1893499200 || n.Text() != "time" {
		t.Fatalf("bare: %+v %v", n, err)
	}
	if _, err := ParseWait("until:soon"); err == nil {
		t.Fatal("bad time accepted")
	}
}

// br refuses labels over 50 characters: long waits are written in the compact
// form and read back unchanged. An invented catalog task with a long Operator
// name and a scheduled wait exercises both cases without truncating their refs.
func TestLongWaitsFitAndRoundTrip(t *testing.T) {
	since := time.Unix(1893491158, 0)
	for _, w := range []Wait{
		{Kind: WaitOperator, Since: since, Ref: "operator-op-long-name-x-example-app-abc.7.8.1"},
		{Kind: WaitUntil, Since: since, Until: time.Unix(1893499200, 0), Ref: "scheduled reconcile of the test servers"},
		{Kind: WaitCI, Since: since, Repo: "acme/api", PR: 25},
		{Kind: WaitReview, Since: since, Ref: "reviewer-a"},
	} {
		l := w.Label()
		if len(l) > MaxLabelLen {
			t.Errorf("%s wait label %q is %d chars", w.Kind, l, len(l))
		}
		got, ok := ParseWaitLabel(l)
		if !ok {
			t.Fatalf("%q does not parse", l)
		}
		want := w
		if w.Kind == WaitUntil {
			if !strings.HasPrefix(w.Ref, got.Ref) || got.Ref == "" {
				t.Errorf("until label %q: want a prefix of %q", got.Ref, w.Ref)
			}
			want.Ref = got.Ref
		}
		if got.Kind != want.Kind || !got.Since.Equal(want.Since) || got.Ref != want.Ref || got.PR != want.PR || got.Repo != want.Repo || !got.Until.Equal(want.Until) {
			t.Errorf("round trip of %q: got %+v want %+v", l, got, want)
		}
	}
	short := Wait{Kind: WaitUser, Since: since}
	if l := short.Label(); !strings.HasPrefix(l, WaitLabelPrefix) {
		t.Errorf("a short wait keeps the long form, got %q", l)
	}
}

func TestEscapeLabelRoundTrip(t *testing.T) {
	for _, id := range []string{"demo-ws.1.22", "sample-app-t06.1.11.42", "a_b.c", "plain-1"} {
		esc := EscapeLabel(id)
		if strings.ContainsAny(esc, ".") {
			t.Errorf("EscapeLabel(%q) = %q still has a dot", id, esc)
		}
		if got := UnescapeLabel(esc); got != id {
			t.Errorf("round trip %q -> %q -> %q", id, esc, got)
		}
	}
}

// The wait cmdrd puts on a finished step fits a label and is recognised again.
func TestManagerCloseWaitIsASettleMarker(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	w := Wait{Kind: WaitUntil, Since: now, Until: now.Add(time.Hour), Ref: ManagerCloseWait}
	l := w.Label()
	if len(l) > MaxLabelLen {
		t.Fatalf("label %q too long", l)
	}
	back, ok := ParseWaitLabel(l)
	if !ok || back.Ref != ManagerCloseWait || !back.Until.Equal(w.Until) || back.Text() != ManagerCloseWait {
		t.Fatalf("round trip %q: %+v %v", l, back, ok)
	}
	for label, want := range map[string]bool{l: true, OperatorDiedLabel: true, "owner:operator-x": false, Wait{Kind: WaitUntil, Since: now, Until: now, Ref: "deploy"}.Label(): false} {
		if IsSettleMarker(label) != want {
			t.Errorf("IsSettleMarker(%q) = %v", label, !want)
		}
	}
}

// A time wait's label cut to fit ends at a word and reads as cut; one cut
// mid-word before that drops the stub. Either way it is cleared by its label.
func TestCutTimeWaitReadsAtAWord(t *testing.T) {
	since, until := time.Unix(1893491158, 0), time.Unix(1893499200, 0)
	w := Wait{Kind: WaitUntil, Since: since, Until: until, Ref: "the slow pages safety review finishes its second pass"}
	got, ok := ParseWaitLabel(w.Label())
	if !ok || !got.Cut || got.Text() != "the slow pages safety…" {
		t.Fatalf("cut wait: %+v reads %q", got, got.Text())
	}
	if got.Label() != w.Label() {
		t.Fatalf("label %q does not round trip: %q", w.Label(), got.Label())
	}
	legacy := "w:" + strconv.FormatInt(since.Unix(), 36) + ":un:" + strconv.FormatInt(until.Unix(), 36) + ":" + EscapeLabel("the slow pages safety r")
	old, ok := ParseWaitLabel(legacy)
	if !ok || old.Text() != "the slow pages safety…" || old.Label() != legacy {
		t.Fatalf("legacy cut %q: reads %q, label %q", legacy, old.Text(), old.Label())
	}
	short, _ := ParseWaitLabel(Wait{Kind: WaitUntil, Since: since, Until: until, Ref: "reconcile of the VMs"}.Label())
	if short.Cut || short.Text() != "reconcile of the VMs" {
		t.Fatalf("uncut wait reads %q", short.Text())
	}
}
