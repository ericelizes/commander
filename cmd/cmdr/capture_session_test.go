package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMarshalCommanderBlankSession_NoBootstrapMessages(t *testing.T) {
	projectPath := t.TempDir()
	now := time.UnixMilli(1778448655181)
	raw, err := marshalCommanderBlankSession("ses_cmdr_test", projectPath, now)
	if err != nil {
		t.Fatalf("marshalCommanderBlankSession: %v", err)
	}

	var got struct {
		Info struct {
			ID        string `json:"id"`
			Slug      string `json:"slug"`
			ProjectID string `json:"projectID"`
			Directory string `json:"directory"`
			Path      string `json:"path"`
			Title     string `json:"title"`
			Version   string `json:"version"`
			Time      struct {
				Created int64 `json:"created"`
				Updated int64 `json:"updated"`
			} `json:"time"`
		} `json:"info"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Info.ID != "ses_cmdr_test" {
		t.Fatalf("id = %q, want %q", got.Info.ID, "ses_cmdr_test")
	}
	if got.Info.Slug != "commander" {
		t.Fatalf("slug = %q, want %q", got.Info.Slug, "commander")
	}
	if got.Info.ProjectID != "global" {
		t.Fatalf("projectID = %q, want %q", got.Info.ProjectID, "global")
	}
	if got.Info.Directory != projectPath {
		t.Fatalf("directory = %q", got.Info.Directory)
	}
	if got.Info.Path != strings.TrimPrefix(projectPath, "/") {
		t.Fatalf("path = %q", got.Info.Path)
	}
	if got.Info.Title != "Commander" {
		t.Fatalf("title = %q, want %q", got.Info.Title, "Commander")
	}
	if got.Info.Version != "0" {
		t.Fatalf("version = %q, want %q", got.Info.Version, "0")
	}
	if got.Info.Time.Created != now.UnixMilli() || got.Info.Time.Updated != now.UnixMilli() {
		t.Fatalf("timestamps = (%d,%d), want %d", got.Info.Time.Created, got.Info.Time.Updated, now.UnixMilli())
	}
	if len(got.Messages) != 0 {
		t.Fatalf("messages len = %d, want 0", len(got.Messages))
	}
	if strings.Contains(string(raw), "Ready.") {
		t.Fatalf("blank session payload should not embed a bootstrap chat: %s", raw)
	}
}

func TestParseImportedSessionID(t *testing.T) {
	got, err := parseImportedSessionID("Imported session: ses_cmdr_blank_probe\n")
	if err != nil {
		t.Fatalf("parseImportedSessionID: %v", err)
	}
	if got != "ses_cmdr_blank_probe" {
		t.Fatalf("got = %q, want %q", got, "ses_cmdr_blank_probe")
	}
}

func TestParseImportedSessionID_Missing(t *testing.T) {
	if _, err := parseImportedSessionID("nothing useful here"); err == nil {
		t.Fatal("expected error for missing imported session id")
	}
}
