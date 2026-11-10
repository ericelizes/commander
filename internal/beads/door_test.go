package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBr(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "br")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestRunWrite_FallsBackWhenDaemonDownAndLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	// api-url points nowhere: connection refused.
	os.WriteFile(filepath.Join(home, "api-url"), []byte("http://127.0.0.1:1"), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	out, _, err := runWrite(context.Background(), t.TempDir(), fakeBr(t, `echo ran "$@"`), "close", "x-1")
	if err != nil || !strings.Contains(string(out), "ran close x-1") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	log, _ := os.ReadFile(filepath.Join(home, "logs", "write-door-fallback.jsonl"))
	if !strings.Contains(string(log), `"verb":"close"`) {
		t.Fatalf("fallback not logged: %q", log)
	}
}

func TestRunWrite_OffSkipsTheDoor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(DoorEnv, "off")
	out, _, err := runWrite(context.Background(), t.TempDir(), fakeBr(t, "echo direct"), "close", "x-1")
	if err != nil || !strings.Contains(string(out), "direct") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "write-door-fallback.jsonl")); err == nil {
		t.Fatal("off must not even try (or log) the door")
	}
}

func TestDoorWritable(t *testing.T) {
	for args, want := range map[string]bool{
		"update x --title t": true,
		"comments add x hi":  true,
		"dep add a b":        true,
		"label add x y":      true,
		"list --json":        false,
		"show x":             false,
		"comments list x":    false,
		"init":               false,
		"--db /x update x":   false,
		"--x init":           false,
		"-v update x":        false,
	} {
		if got := DoorWritable(strings.Fields(args)); got != want {
			t.Errorf("%q = %v, want %v", args, got, want)
		}
	}
}

func TestDoorEnvKeep(t *testing.T) {
	for kv, want := range map[string]bool{
		"USER=alex":                   true,
		"COMMANDER_ROLE=op-x":         true,
		"BR_ACTOR=a":                  true,
		"BEADS_ACTOR=a":               true,
		"BEADS_DB=/x":                 false,
		"BEADS_DOLT_SERVER_HOST=evil": false,
		"BR_SOMETHING=1":              false,
		"BD_ACTOR=a":                  true,
		"COMMANDER_BEADS_BIN=/bin/sh": false,
		"PATH=/bin":                   false,
		"USER":                        false,
	} {
		if got := DoorEnvKeep(kv); got != want {
			t.Errorf("%q = %v, want %v", kv, got, want)
		}
	}
}

// The caller's attribution reaches br when the daemon runs the write.
func TestWithDoorEnv_ReachesBr(t *testing.T) {
	ctx := WithDoorEnv(context.Background(), []string{"BR_ACTOR=operator-7", "PATH=/nowhere"})
	out, _, err := runBr(ctx, t.TempDir(), fakeBr(t, `echo "$BR_ACTOR"`))
	if err != nil || strings.TrimSpace(string(out)) != "operator-7" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// A door that answers 401/503 sends nothing: the write runs directly, logged.
func TestRunWrite_FallsBackOn401And503(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		home := t.TempDir()
		t.Setenv("COMMANDER_HOME", home)
		os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
		os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
		out, _, err := runWrite(context.Background(), t.TempDir(), fakeBr(t, `echo direct`), "close", "x-1")
		srv.Close()
		if err != nil || !strings.Contains(string(out), "direct") {
			t.Fatalf("http %d: out=%q err=%v", code, out, err)
		}
		log, _ := os.ReadFile(filepath.Join(home, "logs", "write-door-fallback.jsonl"))
		if !strings.Contains(string(log), "http") {
			t.Fatalf("http %d: fallback not logged: %q", code, log)
		}
	}
}

// Sent but unanswered is never repeated, and the error says it may have applied.
func TestRunWrite_CutAnswerIsNotRepeatedAndSaysMayHaveApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	marker := filepath.Join(t.TempDir(), "ran")
	_, _, err := runWrite(context.Background(), t.TempDir(), fakeBr(t, "touch "+marker), "comments", "add", "x", "hi")
	if err == nil || !strings.Contains(err.Error(), "may still have been applied") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("a cut-off write was repeated directly")
	}
}

// A busy answer from cmdrd keeps ErrBusy through the door, so a repeat is safe;
// the retry creates exactly once, and a lost answer is never busy.
func TestDoorBusyAnswerSurvivesAndRetryCreatesOnce(t *testing.T) {
	var calls, made int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(DoorResponse{Exit: 1, Busy: true, Error: "br is busy (2 waiting, write not started); retry in a minute"})
			return
		}
		made++
		_ = json.NewEncoder(w).Encode(DoorResponse{Stdout: "x-1\n", Settled: true, Applied: 1})
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	dir := t.TempDir()
	var out []byte
	var err error
	for try := 0; try < 3; try++ {
		out, _, err = runWrite(context.Background(), dir, "br", "create", "t")
		if err == nil || !IsBusy(err) {
			break
		}
	}
	if err != nil || !strings.Contains(string(out), "x-1") || made != 1 || calls != 2 {
		t.Fatalf("out=%q err=%v made=%d calls=%d", out, err, made, calls)
	}
}

func TestDoorLostAnswerIsNotBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	_, _, err := runWrite(context.Background(), t.TempDir(), "br", "create", "t")
	if err == nil || IsBusy(err) {
		t.Fatalf("a lost answer must not read as busy: %v", err)
	}
}

func TestApplyOpsBusyFlag(t *testing.T) {
	busy := func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("br is busy (1 waiting, write not started): %w", ErrBusy)
	}
	if r := ApplyOps(context.Background(), "/d", [][]string{{"create", "t"}}, false, false, busy, nil); !r.Busy || r.Exit != 1 {
		t.Fatalf("busy not flagged: %+v", r)
	}
}

// A write killed while it ran may have committed before it died; one that never
// started, or that bd refused, did not.
func TestApplyOpsMaybeAppliedFlag(t *testing.T) {
	failing := func(err error) func(context.Context, string, []string) ([]byte, []byte, error) {
		return func(context.Context, string, []string) ([]byte, []byte, error) { return nil, nil, err }
	}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"killed", fmt.Errorf("%w: signal: killed", context.DeadlineExceeded), true},
		{"killed, no deadline", errors.New("signal: killed"), true},
		{"busy", fmt.Errorf("br is busy (1 waiting, write not started): %w", errors.Join(ErrBusy, context.DeadlineExceeded)), false},
		{"refused", errors.New("exit status 1: no such parent"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := ApplyOps(context.Background(), "/d", [][]string{{"create", "t"}}, false, false, failing(c.err), nil)
			if r.Exit != 1 || r.MaybeApplied != c.want {
				t.Fatalf("resp = %+v, want MaybeApplied %v", r, c.want)
			}
		})
	}
}

func TestApplyOpsMaybeAppliedOnACutBatch(t *testing.T) {
	cut := func(context.Context, string, string) ([]byte, []byte, error) {
		return nil, nil, errors.New("signal: killed")
	}
	if r := ApplyOps(context.Background(), "/d", [][]string{{"close", "a"}, {"close", "b"}}, true, true, nil, cut); !r.MaybeApplied {
		t.Fatalf("resp = %+v", r)
	}
}

// The flag reaches the client as ErrMaybeApplied; a plain failure and an older
// door's bare "signal: killed" text keep their meaning.
func TestDoorMaybeAppliedAnswerIsErrMaybeApplied(t *testing.T) {
	answer := DoorResponse{Exit: 1, Error: "signal: killed", MaybeApplied: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(answer)
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	write := func() error {
		_, _, err := runWrite(context.Background(), t.TempDir(), "unused", "create", "x")
		return err
	}
	if err := write(); !errors.Is(err, ErrMaybeApplied) || IsBusy(err) {
		t.Fatalf("flagged answer: %v", err)
	}
	answer = DoorResponse{Exit: 1, Error: "signal: killed"}
	if err := write(); errors.Is(err, ErrMaybeApplied) || !MayHaveApplied(err) {
		t.Fatalf("older door's text: %v", err)
	}
	answer = DoorResponse{Exit: 1, Error: "no such parent"}
	if err := write(); MayHaveApplied(err) {
		t.Fatalf("refusal: %v", err)
	}
}

// With no door (down, or switched off) a killed write says the same.
func TestDirectWriteKilledIsErrMaybeApplied(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(DoorEnv, "off")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := runWrite(ctx, t.TempDir(), fakeBr(t, "exec sleep 30"), "create", "x")
	if !errors.Is(err, ErrMaybeApplied) {
		t.Fatalf("direct write cut short: %v", err)
	}
	if _, _, err := runWrite(context.Background(), t.TempDir(), fakeBr(t, "exit 1"), "create", "x"); err == nil || MayHaveApplied(err) {
		t.Fatalf("direct refusal: %v", err)
	}
}

func TestTxnDirectKeepsMaybeApplied(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(DoorEnv, "off")
	t.Setenv("COMMANDER_BEADS_BIN", fakeBr(t, "exec sleep 30"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	resp, err := Txn(ctx, t.TempDir(), [][]string{{"create", "x"}}, false)
	if !resp.MaybeApplied || !errors.Is(err, ErrMaybeApplied) {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

// M3: a write made under WithIntent carries the intent to the door (the CLI verbs' decisive
// writes: a close, a state flush); one without it carries none.
func TestRunWrite_CarriesTheIntentFromItsContext(t *testing.T) {
	var got []*DoorIntent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoorRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Intent)
		_ = json.NewEncoder(w).Encode(DoorResponse{Settled: true, Applied: 1})
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("t"), 0o600)
	dir := t.TempDir()
	ctx := WithIntent(context.Background(), DoorIntent{Bead: "x-1", Verb: "closing x-1"})
	if _, _, err := runWrite(ctx, dir, "br", "close", "x-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runWrite(context.Background(), dir, "br", "comments", "add", "x-1", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] == nil || got[0].Bead != "x-1" || got[0].Verb != "closing x-1" || got[1] != nil {
		t.Fatalf("intents sent: %+v", got)
	}
}
