package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestParseOutputFile_ValidV1(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"schema": "task-output@v1",
		"entries": [
			{"title": "Evaluate library alpha", "description": "HTTP client, check retries"},
			{"title": "Evaluate library beta", "context": {"library": "beta"}}
		]
	}`
	p := writeFile(t, dir, "output.json", body)

	o, err := ParseOutputFile(p)
	if err != nil {
		t.Fatalf("ParseOutputFile: %v", err)
	}
	if o.Schema != SchemaV1 {
		t.Errorf("schema = %q; want %q", o.Schema, SchemaV1)
	}
	if len(o.Entries) != 2 {
		t.Fatalf("entries = %d; want 2", len(o.Entries))
	}
	if o.Entries[0].Title != "Evaluate library alpha" {
		t.Errorf("entries[0].Title = %q", o.Entries[0].Title)
	}
	if o.Entries[1].Context["library"] != "beta" {
		t.Errorf("entries[1].Context[library] = %v", o.Entries[1].Context["library"])
	}
}

func TestParseOutputFile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseOutputFile(filepath.Join(dir, "nonexistent.json"))
	if !errors.Is(err, ErrNoArtifact) {
		t.Fatalf("err = %v; want ErrNoArtifact", err)
	}
}

func TestParseOutputFile_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "output.json", "{not json")
	_, err := ParseOutputFile(p)
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
	if errors.Is(err, ErrNoArtifact) {
		t.Errorf("malformed JSON should not be ErrNoArtifact: %v", err)
	}
}

func TestValidateOutput_MissingSchema(t *testing.T) {
	o := &Output{Entries: []OutputEntry{{Title: "x"}}}
	err := ValidateOutput(o)
	if err == nil {
		t.Fatal("expected error on missing schema")
	}
	if !strings.Contains(err.Error(), "missing schema") {
		t.Errorf("error %v should mention missing schema", err)
	}
	if !strings.Contains(err.Error(), SchemaV1) {
		t.Errorf("error %v should name supported version", err)
	}
}

func TestValidateOutput_WrongSchemaVersion(t *testing.T) {
	o := &Output{Schema: "task-output@v2", Entries: []OutputEntry{{Title: "x"}}}
	err := ValidateOutput(o)
	if err == nil {
		t.Fatal("expected error on wrong schema version")
	}
	if !strings.Contains(err.Error(), "task-output@v2") {
		t.Errorf("error %v should name observed version", err)
	}
	if !strings.Contains(err.Error(), SchemaV1) {
		t.Errorf("error %v should name supported version", err)
	}
}

func TestValidateOutput_EmptyEntries(t *testing.T) {
	o := &Output{Schema: SchemaV1, Entries: nil}
	err := ValidateOutput(o)
	if err == nil {
		t.Fatal("expected error on empty entries")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("error %v should mention entries", err)
	}
}

func TestValidateOutput_MissingTitle(t *testing.T) {
	o := &Output{Schema: SchemaV1, Entries: []OutputEntry{
		{Title: "ok"},
		{Title: "", Description: "no title"},
	}}
	err := ValidateOutput(o)
	if err == nil {
		t.Fatal("expected error on missing title")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("error %v should mention title", err)
	}
	if !strings.Contains(err.Error(), "[1]") {
		t.Errorf("error %v should name failing index", err)
	}
}

func TestValidateOutput_NilOutput(t *testing.T) {
	if err := ValidateOutput(nil); err == nil {
		t.Fatal("expected error on nil output")
	}
}
