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
	if tpl.ID != "pr-review" || tpl.Version != 2 || len(tpl.Steps) != 2 {
		t.Fatalf("unexpected template %+v", tpl)
	}
	if s := tpl.Steps; s[0].ID != "devin" || s[0].Verifier != "devin-comments-resolved" || s[1].ID != "code-review" || s[1].Verifier != "review-report" {
		t.Fatalf("steps: %+v", tpl.Steps)
	}
	if tpl.AppliesTo == "" || tpl.Summary == "" || len(tpl.UseWhen) == 0 || len(tpl.SkipWhen) == 0 || tpl.CatalogOnly {
		t.Fatalf("catalog fields: %+v", tpl)
	}
}

func TestCatalog(t *testing.T) {
	all, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]*Template{}
	for _, tpl := range all {
		ids[tpl.ID] = tpl
	}
	release := ids["forge-app-release"]
	if ids["pr-review"] == nil || release == nil {
		t.Fatalf("catalog: %v", Names())
	}
	if !release.CatalogOnly || len(release.Steps) != 5 || !release.Steps[4].Optional || release.Steps[2].Actor != "owner" {
		t.Fatalf("forge-app-release: %+v", release)
	}
	if _, err := Instantiate(release, Params{Project: "p", Parent: "b", Before: "m"}); err == nil || !strings.Contains(err.Error(), "catalog-only") {
		t.Fatalf("a catalog-only template was plugged: %v", err)
	}
}

func TestCatalogOnlyFieldsStayInTheCatalog(t *testing.T) {
	for name, raw := range map[string]string{
		"optional step":     `{"schema":"workflow-template@v1","id":"t","version":1,"title":"T","steps":[{"id":"a","title":"A","kind":"task","verifier":"ci-green","actor":"operator","optional":true}]}`,
		"recorded verifier": `{"schema":"workflow-template@v1","id":"t","version":1,"title":"T","steps":[{"id":"a","title":"A","kind":"task","verifier":"recorded","actor":"operator"}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s accepted on a pluggable template", name)
		}
		catalog := strings.Replace(raw, `"title":"T",`, `"title":"T","catalog_only":true,`, 1)
		if _, err := Parse([]byte(catalog)); err != nil {
			t.Errorf("%s refused on a catalog-only template: %v", name, err)
		}
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
	tpl := legacyTemplate(t)
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
