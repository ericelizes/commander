package ship

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mixedOut = `ok  	x/a	0.1s
runtime/cgo: pthread_create failed: Resource temporarily unavailable
FAIL	x/b	0.3s
--- FAIL: TestC (0.00s)
    c_test.go:1: nope
FAIL	x/c	0.2s
FAIL`

func TestClassifier(t *testing.T) {
	if !IsExhaustion("fork/exec /usr/bin/tmux: resource temporarily unavailable") || !IsExhaustion("runtime/cgo: pthread_create failed") {
		t.Fatal("exhaustion not recognised")
	}
	if IsExhaustion("--- FAIL: TestX: want 1 got 2") {
		t.Fatal("plain failure classed as exhaustion")
	}
	pkg, text, ok := firstFailure(mixedOut, 40)
	if !ok || pkg != "x/b" || !strings.Contains(text, "pthread_create") || strings.Contains(text, "TestC") {
		t.Fatalf("firstFailure = %q %q %v", pkg, text, ok)
	}
	pkgs, other := exhaustedPackages(mixedOut)
	if len(pkgs) != 1 || pkgs[0] != "x/b" || !other {
		t.Fatalf("exhaustedPackages = %v %v", pkgs, other)
	}
	msg := describeRed("abcdef0123456", []string{"go", "test"}, os.ErrInvalid, mixedOut)
	if !strings.Contains(msg, "x/b") || !strings.Contains(msg, "RESOURCE EXHAUSTION") || !strings.Contains(msg, "retry") {
		t.Fatalf("describeRed: %s", msg)
	}
}

func TestRetryExhaustedPackageAlone(t *testing.T) {
	out := "runtime/cgo: pthread_create failed\nFAIL\tx/b\t0.3s\nFAIL"
	var calls []call
	var buf bytes.Buffer
	o, err := retryExhausted(context.Background(), recorder("", &calls), "d", []string{"go", "test", "./..."}, out, os.ErrInvalid, &buf)
	if err != nil || o != "" || len(calls) != 1 || calls[0].cmd != "go test -p 1 x/b" {
		t.Fatalf("retry: %q %v %v", o, err, calls)
	}
	// a still-red retry keeps the error; a mixed failure is not retried at all
	calls = nil
	if _, err := retryExhausted(context.Background(), recorder("go test -p 1 x/b", &calls), "d", []string{"go", "test"}, out, os.ErrInvalid, &buf); err == nil {
		t.Fatal("red retry must stay red")
	}
	calls = nil
	if _, err := retryExhausted(context.Background(), recorder("", &calls), "d", []string{"go", "test"}, mixedOut, os.ErrInvalid, &buf); err == nil || len(calls) != 0 {
		t.Fatalf("mixed failure retried: %v", calls)
	}
}

func TestWaitForThreads(t *testing.T) {
	counts := []int{3000, 2800, 2000}
	i, slept := 0, 0
	count := func() int { n := counts[i]; i++; return n }
	var buf bytes.Buffer
	if err := waitForThreads(context.Background(), &buf, count, 2400, time.Hour, time.Second, func(context.Context, time.Duration) { slept++ }); err != nil {
		t.Fatal(err)
	}
	if slept != 2 || strings.Count(buf.String(), "waiting") != 2 {
		t.Fatalf("slept %d: %s", slept, buf.String())
	}
	// bounded: gives up waiting after maxWait
	buf.Reset()
	if err := waitForThreads(context.Background(), &buf, func() int { return 9999 }, 2400, 0, time.Second, func(context.Context, time.Duration) {}); err != nil || !strings.Contains(buf.String(), "starting anyway") {
		t.Fatalf("bounded wait: %v %s", err, buf.String())
	}
	// threshold 0 never waits
	if err := waitForThreads(context.Background(), &buf, func() int { t.Fatal("counted"); return 0 }, 0, time.Hour, time.Second, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCountThreads(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"1/task/1", "1/task/2", "2/task/2", "self"} {
		os.MkdirAll(filepath.Join(root, p), 0o755)
	}
	if n := CountThreads(root, os.Getuid()); n != 3 {
		t.Fatalf("CountThreads = %d", n)
	}
	if n := CountThreads(root, os.Getuid()+12345); n != 0 {
		t.Fatalf("other uid = %d", n)
	}
}

func TestShipWaitsForThreadsAndTakesExclusiveLock(t *testing.T) {
	dir := newRepo(t)
	var order []string
	n := 0
	opts := Options{Root: dir, Branch: "feat", Run: func(context.Context, string, string, ...string) (string, error) {
		order = append(order, "run")
		return "", nil
	},
		Exclusive: func(context.Context, io.Writer) (func(), error) {
			order = append(order, "lock")
			return func() { order = append(order, "unlock") }, nil
		},
		Threads: func() int {
			n++
			if n == 1 {
				return 9000
			}
			return 10
		},
		Sleep: func(context.Context, time.Duration) { order = append(order, "sleep") },
	}
	if err := Run(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if order[0] != "lock" || order[1] != "sleep" || order[len(order)-1] != "unlock" {
		t.Fatalf("order %v", order)
	}
}

const pthread = "runtime/cgo: pthread_create failed: Resource temporarily unavailable\n"

func retryOut(t *testing.T, out string, runnerFail string) (calls []call, o string, err error, log string) {
	t.Helper()
	var buf bytes.Buffer
	o, err = retryExhausted(context.Background(), recorder(runnerFail, &calls), "d", []string{"go", "test", "./..."}, out, os.ErrInvalid, &buf)
	return calls, o, err, buf.String()
}

func TestRetryClassification(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		retries int
		red     bool
	}{
		{"real FAIL plus pthread line in same package", "--- FAIL: TestB (0.00s)\n" + pthread + "FAIL\tx/b\t0.3s\nFAIL", 0, true},
		{"bare phrase in real test output", "    b_test.go:9: got resource temporarily unavailable\nFAIL\tx/b\t0.3s\nFAIL", 0, true},
		{"fake exhaustion only", pthread + "FAIL\tx/b\t0.3s\nFAIL", 1, false},
		{"unattributed tail", pthread + "FAIL\tx/b\t0.3s\npanic: boom\nexit status 2", 0, true},
		{"two packages, one exhausted one real", pthread + "FAIL\tx/b\t0.3s\n--- FAIL: TestC\nFAIL\tx/c\t0.1s\nFAIL", 0, true},
		{"top-level panic", "panic: oops\n\ngoroutine 1 [running]:\nexit status 2", 0, true},
	}
	for _, c := range cases {
		calls, _, err, _ := retryOut(t, c.out, "")
		if len(calls) != c.retries || (err != nil) != c.red {
			t.Errorf("%s: retries=%d err=%v", c.name, len(calls), err)
		}
	}
}

func TestRetrySuccessIsReported(t *testing.T) {
	_, _, err, log := retryOut(t, pthread+"FAIL\tx/b\t0.3s\nFAIL", "")
	if err != nil || !strings.Contains(log, "pthread_create") || !strings.Contains(log, "retried package x/b: passed alone") {
		t.Fatalf("%v\n%s", err, log)
	}
}

func TestWaitForThreadsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForThreads(ctx, io.Discard, func() int { return 9999 }, 2400, time.Hour, time.Second, func(c context.Context, _ time.Duration) { cancel() })
	if err == nil {
		t.Fatal("cancel not honoured")
	}
}

func TestMaxThreadsConfig(t *testing.T) {
	t.Setenv(ThreadsEnv, "")
	v := 100
	if maxThreads(&v) != 100 || maxThreads(nil) != defaultMaxThreads {
		t.Fatal("config/default")
	}
	t.Setenv(ThreadsEnv, "5")
	if maxThreads(&v) != 5 {
		t.Fatal("env must win")
	}
}
