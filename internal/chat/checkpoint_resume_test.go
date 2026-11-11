package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCheckpointRefusesFreshClaudeContextIfConversationDisappears(t *testing.T) {
	h := newHarness(t)
	role := "operator-required-context"
	sid := h.role(role)
	base := h.opts.Resolver
	h.opts.Resolver = ResolverFunc(func(ctx context.Context, role string) (Spec, error) {
		s, e := base.Resolve(ctx, role)
		s.ExecutionRunID = 42
		s.ExecutionResume = true
		return s, e
	})
	h.e = New(h.opts)
	if _, e := h.e.Start(context.Background(), role, StartOptions{}); e == nil || !strings.Contains(e.Error(), "refusing a fresh context") {
		t.Fatal("missing checkpoint started a fresh conversation", e)
	}
	if _, e := os.Stat(filepath.Join(h.fakeDir, "argv-"+sid+".json")); !os.IsNotExist(e) {
		t.Fatal("provider started without its required conversation", e)
	}
}

func TestCodexRequiredCheckpointNeverFallsBackToThreadStart(t *testing.T) {
	for _, failure := range []string{"missing-file", "resume-error", "different-thread"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint-thread")
			if failure != "missing-file" {
				if e := os.WriteFile(path, []byte("original-thread"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			defer inR.Close()
			defer inW.Close()
			defer outR.Close()
			defer outW.Close()
			methods := make(chan string, 16)
			done := make(chan struct{})
			go func() {
				defer close(done)
				scan := bufio.NewScanner(inR)
				enc := json.NewEncoder(outW)
				for scan.Scan() {
					var req struct {
						ID     int
						Method string
					}
					if json.Unmarshal(scan.Bytes(), &req) != nil {
						continue
					}
					methods <- req.Method
					if req.Method == "initialized" {
						continue
					}
					if req.Method == "thread/resume" && failure == "resume-error" {
						_ = enc.Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": -1, "message": "checkpoint unavailable"}})
					} else {
						_ = enc.Encode(map[string]any{"id": req.ID, "result": map[string]any{"thread": map[string]any{"id": "different-thread"}}})
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cfg := CodexBridgeConfig{ThreadFile: path, RequireResume: true, SessionID: "saved-session"}
			if !strings.Contains(strings.Join(cfg.Args(), " "), "--require-resume") {
				t.Fatal("required resume was lost from child arguments")
			}
			e := RunCodexBridge(ctx, cfg, strings.NewReader(""), io.Discard, outR, inW)
			if e == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
			_ = inR.Close()
			_ = inW.Close()
			_ = outR.Close()
			_ = outW.Close()
			<-done
			close(methods)
			for method := range methods {
				if method == "thread/start" {
					t.Fatal("required resume silently created a new thread", failure)
				}
			}
			if failure != "missing-file" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "original-thread" {
					t.Fatal("failed checkpoint changed its original thread", string(raw), err)
				}
			}
		})
	}
}
