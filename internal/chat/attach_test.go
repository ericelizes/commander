package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserContentBlocks(t *testing.T) {
	if got := userContent("hi", nil); got != "hi" {
		t.Fatalf("no files: %v", got)
	}
	files := []Attachment{
		{Name: "shot.png", Mime: "image/png", Data: []byte("png")},
		{Name: "notes.txt", Mime: "text/plain", Path: "/u/notes.txt"},
	}
	raw, _ := json.Marshal(userContent("look", files))
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) != 2 {
		t.Fatalf("blocks = %s (%v)", raw, err)
	}
	if blocks[0]["type"] != "text" || !strings.Contains(blocks[0]["text"].(string), "/u/notes.txt") {
		t.Fatalf("text block = %v", blocks[0])
	}
	src := blocks[1]["source"].(map[string]any)
	if blocks[1]["type"] != "image" || src["media_type"] != "image/png" || src["data"] != "cG5n" {
		t.Fatalf("image block = %v", blocks[1])
	}
}

func TestSendWithAttachmentsSavesAndPublishes(t *testing.T) {
	h := newHarness(t)
	h.role("operator-a")
	h.start("operator-a")
	sub := h.subscribe("operator-a")
	files := []Attachment{
		{Name: "../../evil.txt", Mime: "text/plain", Data: []byte("data")},
		{Name: "shot.png", Mime: "image/png", Data: []byte("png")},
	}
	if err := h.e.SendWithAttachments(context.Background(), "operator-a", "see these", files); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindResult))
	ev := seen[0]
	if ev.Kind != KindUser || len(ev.Attachments) != 2 {
		t.Fatalf("event = %+v", ev)
	}
	dir := filepath.Join(h.e.opts.Home, "uploads", "operator-a")
	for _, a := range ev.Attachments {
		if filepath.Dir(a.Path) != dir {
			t.Fatalf("%q saved outside %s", a.Path, dir)
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasSuffix(ev.Attachments[0].Path, "-evil.txt") {
		t.Fatalf("name not sanitized: %s", ev.Attachments[0].Path)
	}
}

func TestSaveUploadsLimits(t *testing.T) {
	e := &Engine{opts: Options{Home: t.TempDir()}}
	if _, err := e.SaveUploads("r", []Attachment{{Name: "big.png", Mime: "image/png", Data: make([]byte, maxImageBytes+1)}}); err == nil {
		t.Fatal("oversized image accepted")
	}
	if _, err := e.SaveUploads("r", []Attachment{{Name: "empty.txt"}}); err == nil {
		t.Fatal("empty file accepted")
	}
}

func TestSweepCapturesRemovesOnlyOldCaptures(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "uploads", "commander")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldPNG := write("20261028-100000-0-capture-20261028100000.png", 25*time.Hour)
	oldTxt := write("20261028-100000-1-capture-20261028100000.txt", 30*time.Hour)
	fresh := write("20261029-100000-0-capture-20261029100000.png", time.Hour)
	lookalike := write("20261020-100000-0-my-capture-notes.pdf", 100*time.Hour)
	lookalike2 := write("20261020-100000-1-capture-notes.txt", 100*time.Hour)
	userFile := write("20261020-100000-0-notes.txt", 100*time.Hour) // a user's own upload stays
	if n := SweepCaptures(home, 24*time.Hour, now); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for _, gone := range []string{oldPNG, oldTxt} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still there", gone)
		}
	}
	for _, kept := range []string{fresh, userFile, lookalike, lookalike2} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}

func TestSaveUploadsDropsCapturesOverTheCap(t *testing.T) {
	e := &Engine{opts: Options{Home: t.TempDir()}}
	var files []Attachment
	for i := 0; i < maxAttachments; i++ {
		files = append(files, Attachment{Name: fmt.Sprintf("f%d.txt", i), Data: []byte("x")})
	}
	files = append(files, Attachment{Name: "capture-20261029100000.png", Mime: "image/png", Data: []byte("x")},
		Attachment{Name: "capture-20261029100000.txt", Data: []byte("x")})
	out, err := e.SaveUploads("r", files)
	if err != nil || len(out) != maxAttachments {
		t.Fatalf("got %d, %v; want the user's %d files", len(out), err, maxAttachments)
	}
	if _, err := e.SaveUploads("r", append(files, Attachment{Name: "extra.txt", Data: []byte("x")})); err == nil {
		t.Fatal("user files over the cap accepted")
	}
}

func TestRunCaptureSweepSweepsAtStart(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "uploads", "commander")
	_ = os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "20261020-100000-0-capture-20261020100000.png")
	_ = os.WriteFile(p, []byte("x"), 0o600)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(p, old, old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunCaptureSweep(ctx, home, time.Hour)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("old capture survived the start sweep")
	}
}
