package reports

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportNamesAReportByItsContent(t *testing.T) {
	home, src := t.TempDir(), filepath.Join(t.TempDir(), "a.HTML")
	os.WriteFile(src, []byte("<p>x</p>"), 0o644)
	a, err := Import(home, src)
	if err != nil || !ValidName(a) || filepath.Ext(a) != ".html" {
		t.Fatalf("%q %v", a, err)
	}
	b, _ := Import(home, src)
	if a != b {
		t.Fatal("the same content keeps its name")
	}
	if _, err := os.Stat(filepath.Join(Dir(home), a)); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(home, filepath.Join(t.TempDir(), "x.sh")); err == nil {
		t.Fatal("a script is not a report")
	}
	if ValidName("../config.toml") || ValidName("config.toml") {
		t.Fatal("only hash names are valid")
	}
}
