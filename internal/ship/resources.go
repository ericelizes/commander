package ship

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ThreadsEnv overrides the user thread count above which a verify waits
// instead of starting (0 turns the wait off).
const ThreadsEnv = "COMMANDER_SHIP_MAX_THREADS"

// ThreadsWaitEnv overrides, as a Go duration, how long a verify waits for the
// thread count to fall under the threshold before it starts anyway.
const ThreadsWaitEnv = "COMMANDER_SHIP_THREADS_WAIT"

const (
	defaultMaxThreads  = 2400
	defaultThreadsWait = 20 * time.Minute
	threadsPollEvery   = 30 * time.Second
)

// maxProcs caps the Go scheduler of each test binary a verify starts.
const maxProcs = "4"

// maxThreads is the env override, else the config value ([ship] max_threads),
// else the default. 0 turns the wait off.
func maxThreads(cfg *int) int {
	if n, err := strconv.Atoi(os.Getenv(ThreadsEnv)); err == nil && n >= 0 {
		return n
	}
	if cfg != nil && *cfg >= 0 {
		return *cfg
	}
	return defaultMaxThreads
}

func threadsWait() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(ThreadsWaitEnv)); err == nil && d > 0 {
		return d
	}
	return defaultThreadsWait
}

// CountThreads counts the threads (entries of /proc/*/task) of processes owned
// by uid under procRoot. Where there is no procfs it returns 0.
func CountThreads(procRoot string, uid int) int {
	pids, err := filepath.Glob(filepath.Join(procRoot, "[0-9]*"))
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range pids {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		ents, err := os.ReadDir(filepath.Join(p, "task"))
		if err != nil {
			continue
		}
		n += len(ents)
	}
	return n
}

// waitForThreads blocks while count() is above max, printing a line every
// poll, for at most maxWait; then it returns and the verify starts anyway
// (a wait must never become a hang). max <= 0 waits for nothing.
func waitForThreads(ctx context.Context, out io.Writer, count func() int, max int, maxWait, poll time.Duration, sleep func(context.Context, time.Duration)) error {
	if max <= 0 {
		return nil
	}
	deadline := time.Now().Add(maxWait)
	for {
		n := count()
		if n <= max {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(out, "ship check: %d threads in use (limit %d) after waiting %s; starting anyway\n", n, max, maxWait)
			return nil
		}
		fmt.Fprintf(out, "ship check: %d threads in use (limit %d); waiting for the machine to settle\n", n, max)
		sleep(ctx, poll)
	}
}

// exhaustionLine matches only runtime-level resource lines, anchored at the
// start of a line; text a test prints inside its own output never counts.
var exhaustionLine = regexp.MustCompile(`(?im)^(runtime/cgo: pthread_create failed|runtime: failed to create new OS thread|fatal error: newosproc|runtime: cannot allocate memory|fork/exec [^\n]*: (resource temporarily unavailable|cannot allocate memory)|[^\n]*: cannot fork)`)

// realFailure marks output that holds a real test failure or a panic that is
// not the runtime's own thread crash.
var realFailure = regexp.MustCompile(`(?m)^\s*--- FAIL:|^panic:|^\s+\S+\.go:\d+:`)

// IsExhaustion reports whether output shows the process ran out of threads or
// process slots (the machine's fault, not the code's): runtime resource lines
// and no mark of a real failure.
func IsExhaustion(output string) bool {
	return exhaustionLine.MatchString(output) && !realFailure.MatchString(output)
}

// pkgResult is one package's slice of `go test` output.
type pkgResult struct {
	Pkg    string
	Failed bool
	Output string
}

// splitPackages cuts plain `go test` output into per-package results: a
// package's text ends at its "ok" / "FAIL <pkg>" / "? <pkg>" line. Text after
// the last such line is returned as a result with no package and Failed set:
// output nothing accounts for is never green.
func splitPackages(output string) []pkgResult {
	var res []pkgResult
	var cur []string
	for _, line := range strings.Split(output, "\n") {
		cur = append(cur, line)
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "ok", "?":
		case "FAIL":
			if strings.HasPrefix(f[1], "-") {
				continue
			}
		default:
			continue
		}
		res = append(res, pkgResult{Pkg: f[1], Failed: f[0] == "FAIL", Output: strings.Join(cur, "\n")})
		cur = nil
	}
	if rest := strings.TrimSpace(strings.Join(cur, "\n")); rest != "" && !onlyBareFail(rest) {
		res = append(res, pkgResult{Failed: true, Output: rest})
	}
	return res
}

// onlyBareFail is true for go test's closing summary line ("FAIL") alone.
func onlyBareFail(s string) bool { return strings.TrimSpace(s) == "FAIL" }

// firstFailure is the first failing package of go test output and the first
// n lines of its failure. ok is false when the output names none.
func firstFailure(output string, n int) (pkg, text string, ok bool) {
	for _, r := range splitPackages(output) {
		if r.Failed && r.Pkg != "" {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			if len(lines) > n {
				lines = lines[:n]
			}
			return r.Pkg, strings.Join(lines, "\n"), true
		}
	}
	return "", "", false
}

// exhaustedPackages lists the failing packages whose own output shows only
// resource exhaustion, and whether anything else failed: a package with a real
// failure, or output no package line accounts for (a top-level panic, trailing
// text), counts as other.
func exhaustedPackages(output string) (pkgs []string, other bool) {
	for _, r := range splitPackages(output) {
		if !r.Failed {
			continue
		}
		if r.Pkg != "" && IsExhaustion(r.Output) {
			pkgs = append(pkgs, r.Pkg)
		} else {
			other = true
		}
	}
	return pkgs, other
}

const failureLines = 40

// describeRed is the error text of a red step.
func describeRed(sha string, argv []string, err error, output string) string {
	cmd := strings.Join(argv, " ")
	head := fmt.Sprintf("%s is red: %s: %v", short(sha), cmd, err)
	exhausted := false
	if pkg, text, ok := firstFailure(output, failureLines); ok {
		exhausted = IsExhaustion(text)
		kind := "first failing package"
		if exhausted {
			kind = "first failing package (RESOURCE EXHAUSTION)"
		}
		head += fmt.Sprintf("\n%s: %s\n%s", kind, pkg, text)
	} else {
		head += "\n" + tail(output)
	}
	if exhausted {
		head += "\nresource exhaustion (threads or processes ran out), not a test failure: check `ps -eLf | wc -l` and `ulimit -u`, wait for the machine to calm down and retry cmdr internal-ship"
	}
	return head
}
