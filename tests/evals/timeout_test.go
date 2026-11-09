package evals

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

const evalTimeoutScaleEnv = "COMMANDER_EVAL_TIMEOUT_SCALE"

// Scale budgets only, never polling intervals or normal execution time. Reject
// typos instead of silently running with an unexpected (or unbounded) deadline.
func scaledEvalTimeout(base time.Duration, raw string) (time.Duration, error) {
	if raw == "" {
		return base, nil
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(scale >= 1 && scale <= 10) {
		return 0, fmt.Errorf("%s must be a number from 1 to 10, got %q", evalTimeoutScaleEnv, raw)
	}
	return time.Duration(float64(base) * scale), nil
}

// Run a real scenario against a hung fake in a child test process: the child
// MUST fail, both when a tmux command hangs and when no scripted reply arrives.
func TestEvalHungScenario(t *testing.T) {
	const base = 150 * time.Millisecond
	if mode := os.Getenv("COMMANDER_EVAL_HANG_CHILD"); mode != "" {
		dir := t.TempDir()
		testguard.NoLeakedProcesses(t, dir)
		script := "#!/bin/sh\nexit 0\n"
		if mode == "command" {
			script = "#!/bin/sh\nexec sleep 3600\n"
		}
		writeExecutable(t, filepath.Join(dir, "tmux"), script)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, base))
		defer cancel()
		h := &harness{t: t, ctx: ctx, scriptedMode: true, baseEnv: os.Environ(), transcriptLog: filepath.Join(dir, "transcript")}
		h.runScenario(loadScenario(t, "scenarios/deadlines/hung.json"))
		return
	}
	for _, mode := range []string{"command", "reply"} {
		for _, scale := range []string{"1", "3"} {
			t.Run(mode+"/"+scale, func(t *testing.T) {
				result, ok := hungResults[mode+"/"+scale]
				if !ok {
					t.Fatal("deadline helper was not orchestrated before parent isolation")
				}
				out, err, elapsed := result.out, result.err, result.elapsed
				budget, e := scaledEvalTimeout(base, scale)
				if e != nil {
					t.Fatal(e)
				}
				overhead, _ := scaledEvalTimeout(3*time.Second, scale)
				var exit *exec.ExitError
				want := "tmux [send-keys"
				if mode == "reply" {
					want = "timed out waiting for scripted reply"
				}
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || result.timedOut || !strings.Contains(string(out), want) || !strings.Contains(string(out), "--- FAIL: TestEvalHungScenario") {
					t.Fatalf("hung scenario did not fail at its deadline: %v\n%s", err, out)
				}
				t.Logf("fresh deadline helper %s/%s: elapsed=%v budget=%v; diagnostic=%q; exit=1", mode, scale, elapsed, budget, want)
				// Allow process startup/scheduling overhead, but no extra reply
				// budget after the enclosing scenario's deadline has expired.
				if elapsed < budget || elapsed > budget+overhead {
					t.Fatalf("hung scenario took %v, want %v plus startup overhead", elapsed, budget)
				}
			})
		}
	}
}

// Run fresh negative-scenario processes before the parent's Isolate consumes
// its reserve. Every helper runs the normal TestMain guard/isolation, against
// the unchanged finite launcher ceiling, and is bounded by a wall deadline.
var hungResults = map[string]hungResult{}

type hungResult struct {
	out      []byte
	err      error
	elapsed  time.Duration
	timedOut bool
}

func prepareHungScenarios() {
	for _, mode := range []string{"command", "reply"} {
		for _, scale := range []string{"1", "3"} {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEvalHungScenario$", "-test.v")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, evalTimeoutScaleEnv+"=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "COMMANDER_EVAL_HANG_CHILD="+mode, evalTimeoutScaleEnv+"="+scale)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			hungResults[mode+"/"+scale] = hungResult{out, err, time.Since(start), ctx.Err() != nil}
			cancel()
		}
	}
}

func evalTimeout(t *testing.T, base time.Duration) time.Duration {
	t.Helper()
	d, err := scaledEvalTimeout(base, os.Getenv(evalTimeoutScaleEnv))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEvalTimeoutScale(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", time.Second}, {"1", time.Second}, {"3", 3 * time.Second},
		{"1.5", 1500 * time.Millisecond}, {"10", 10 * time.Second},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(evalTimeoutScaleEnv, tc.raw)
			if got := evalTimeout(t, time.Second); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	for _, raw := range []string{"bad", "0", "-1", "0.5", "11", "NaN", "+Inf", "1e300"} {
		if _, err := scaledEvalTimeout(time.Second, raw); err == nil {
			t.Errorf("accepted invalid scale %q", raw)
		}
	}
}
