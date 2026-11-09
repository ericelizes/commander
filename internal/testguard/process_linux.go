//go:build linux

package testguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const testTasksMax = 256

// capProcesses never changes Linux's per-UID thread limit. A dedicated cgroup
// provides the workload bound, without subtracting unrelated userspace threads.
func capProcesses() {}

func boundProcesses() error { return boundProcessesAt("/proc/self/cgroup", "/sys/fs/cgroup") }

func boundProcessesAt(procFile, mount string) error {
	if boundedCgroup(procFile, mount, testTasksMax) {
		return nil
	}
	if os.Getenv("COMMANDER_TESTGUARD_SCOPE") != "" {
		return fmt.Errorf("transient test scope did not provide a pids.max <= %d", testTasksMax)
	}
	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return fmt.Errorf("Linux tests require a cgroup v2 pids cap <= %d or systemd-run --user: %w", testTasksMax, err)
	}
	unit := fmt.Sprintf("commander-test-%d-%d.scope", os.Getpid(), time.Now().UnixNano())
	args := []string{"--user", "--scope", "--quiet", "--unit=" + unit, "--property=TasksMax=" + strconv.Itoa(testTasksMax), "--property=RuntimeMaxSec=1800", "--", os.Args[0]}
	args = append(args, os.Args[1:]...)
	cmd := exec.Command(runner, args...)
	cmd.Env = append(os.Environ(), "COMMANDER_TESTGUARD_SCOPE="+unit)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	// A detached grandchild can outlive the test binary. Stop only this newly
	// minted scope, never a shared user service or another workload's cgroup.
	stop := exec.Command("systemctl", "--user", "stop", unit)
	_ = stop.Run()
	if err == nil {
		os.Exit(0)
	}
	if exit, ok := err.(*exec.ExitError); ok {
		fmt.Fprintln(os.Stderr, "testguard: bounded workload failed; if the scope could not start, provide a user systemd session or dedicated cgroup v2 with pids.max <= 256; no unbounded fallback was run")
		os.Exit(exit.ExitCode())
	}
	return fmt.Errorf("start bounded test scope: %w (ensure a user systemd session or run in a dedicated cgroup v2 with pids.max <= %d)", err, testTasksMax)
}

func boundedCgroup(procFile, mount string, max uint64) bool {
	raw, err := os.ReadFile(procFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		path, ok := strings.CutPrefix(line, "0::/")
		if !ok {
			continue
		}
		// A container may expose an unmappable host path. Do not guess a bound.
		for _, part := range strings.Split(path, "/") {
			if part == ".." {
				return false
			}
		}
		dir := filepath.Join(mount, path)
		for {
			data, err := os.ReadFile(filepath.Join(dir, "pids.max"))
			if err == nil {
				n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
				if err == nil && n > 0 && n <= max {
					return true
				}
			}
			if dir == mount {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return false
}
