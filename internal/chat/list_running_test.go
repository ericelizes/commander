package chat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListRunningSkipsChatsWithNoLiveProcess(t *testing.T) {
	h := newHarness(t)
	h.role("commander")
	h.start("commander")
	if err := os.WriteFile(filepath.Join(h.e.logDir(), "manager-retired.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := h.e.ListRunning()
	if len(got) != 1 || got[0].Role != "commander" || !got[0].Running {
		t.Fatalf("running chats: %+v", got)
	}
}
