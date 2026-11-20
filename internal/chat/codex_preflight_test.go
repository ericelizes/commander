package chat

import (
	"errors"
	"testing"
	"time"
)

func TestCodexToolsReadyIsCachedAndFailsClosed(t *testing.T) {
	old := codexPreflightRun
	t.Cleanup(func() { codexPreflightRun = old; codexPreflightAt = time.Time{} })
	calls := 0
	codexPreflightRun = func() error { calls++; return errors.New("failed to decode code-mode IPC frame") }
	codexPreflightAt = time.Time{}
	if CodexToolsReady() || CodexToolsReady() || calls != 1 {
		t.Fatal("a broken tool runner must not be ready, and the answer is cached", calls)
	}
}
