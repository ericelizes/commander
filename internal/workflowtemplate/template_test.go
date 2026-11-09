package workflowtemplate

import (
	"strings"
	"testing"
)

func TestLoadPRReview(t *testing.T) {
	tpl, err := Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	if tpl.ID != "pr-review" || tpl.Version != 1 || len(tpl.Steps) != 6 {
		t.Fatalf("unexpected template %+v", tpl)
	}
	if tpl.Steps[0].ID != "devin-comments" || tpl.Steps[5].ID != "final-check" {
		t.Fatalf("chain order wrong: %+v", tpl.Steps)
	}
}

func TestLoadUnknown(t *testing.T) {
	if _, err := Load("nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Load("../x"); err == nil {
		t.Fatal("path escape must fail")
	}
}

const good = `{"schema":"workflow-template@v1","id":"t","version":1,"title":"T","steps":[
 {"id":"a","title":"A","kind":"task","verifier":"review-clean","actor":"operator"},
 {"id":"b","title":"B","kind":"task","verifier":"review-clean","actor":"operator"}]}`

func TestParseValidation(t *testing.T) {
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ from, to, want string }{
		"unknown verifier": {`"verifier":"review-clean","actor":"operator"},
 {"id":"b"`, `"verifier":"shell","actor":"operator"},
 {"id":"b"`, "unknown verifier"},
		"missing title": {`"title":"T"`, `"title":""`, "title is missing"},
		"bad schema":    {"workflow-template@v1", "workflow-template@v2", "schema must be"},
		"bad kind": {`"kind":"task","verifier":"review-clean","actor":"operator"},
 {"id":"b"`, `"kind":"x","verifier":"review-clean","actor":"operator"},
 {"id":"b"`, "kind"},
		"duplicate id":    {`"id":"b"`, `"id":"a"`, "duplicate"},
		"unknown field":   {`"version":1`, `"version":1,"extra":1`, "unknown field"},
		"unknown wait":    {`"id":"b","title":"B"`, `"id":"b","after":["zz"],"title":"B"`, "unknown step"},
		"cycle":           {`{"id":"a","title":"A"`, `{"id":"a","after":["b"],"title":"A"`, "cycle"},
		"missing version": {`"version":1,`, ``, "version"},
		"no steps":        {`"steps":[`, `"steps":[],"x":[`, "unknown field"},
	}
	for name, c := range cases {
		src := strings.Replace(good, c.from, c.to, 1)
		if src == good {
			t.Fatalf("%s: case did not change the template", name)
		}
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want %q", name, err, c.want)
		}
	}
}

func TestInstantiate(t *testing.T) {
	tpl, _ := Load("pr-review")
	p := Params{Project: "p", Parent: "b-1", Before: "m-1", PR: "https://github.com/o/r/pull/3"}
	in, err := Instantiate(tpl, p)
	if err != nil {
		t.Fatal(err)
	}
	if in.Key != Key(p) || len(in.Key) != 12 {
		t.Fatalf("key %q", in.Key)
	}
	if in.Version != 1 || in.Hash != tpl.Hash() || !strings.Contains(in.Snapshot, `"pr-review"`) {
		t.Fatalf("snapshot missing: %+v", in)
	}
	want := map[string]bool{"workflow-template:pr-review": true, "workflow-version:1": true, "workflow-instance:" + in.Key: true}
	for _, l := range in.Steps[2].Labels {
		delete(want, l)
	}
	if len(want) != 0 || !contains(in.Steps[2].Labels, "workflow-step:fix-review") {
		t.Fatalf("step labels %v", in.Steps[2].Labels)
	}
	if got := in.Steps[2].After; len(got) != 1 || got[0] != "review" {
		t.Fatalf("after %v", got)
	}
	if len(in.Steps[0].After) != 0 {
		t.Fatalf("first step waits on %v", in.Steps[0].After)
	}
	if k2 := Key(Params{Project: "p", Parent: "b-1", Before: "m-1", PR: "https://github.com/o/r/pull/4"}); k2 == in.Key {
		t.Fatal("different PR must give a different key")
	}
	if _, err := Instantiate(tpl, Params{Project: "p"}); err == nil {
		t.Fatal("missing params must fail")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
