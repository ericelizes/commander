//go:build linux

package testguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMainBoundsThisBinary(t *testing.T) {
	if !boundedCgroup("/proc/self/cgroup", "/sys/fs/cgroup", testTasksMax) {
		t.Fatalf("tests run outside a pids.max <= %d cgroup: a guard call in a test would re-exec and os.Exit", testTasksMax)
	}
}

func TestBoundedCgroup(t *testing.T) {
	for _, tc := range []struct {
		name, child, ancestor string
		want                  bool
	}{
		{"dedicated", "256", "max", true},
		{"ancestor", "max", "128", true},
		{"unbounded", "max", "max", false},
		{"too large", "3000", "max", false},
		{"invalid", "broken", "max", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
				t.Fatal(err)
			}
			for p, value := range map[string]string{"pids.max": tc.ancestor, "tests/pids.max": tc.child, "self": "0::/tests\n"} {
				if err := os.WriteFile(filepath.Join(root, p), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := boundedCgroup(filepath.Join(root, "self"), root, 256); got != tc.want {
				t.Fatalf("bounded=%v want %v", got, tc.want)
			}
		})
	}
}

// This is a finite saturation probe, not an uncontrolled fork bomb. Every
// process is owned by this helper and waited; the kernel cap must refuse a
// child before 256 children exist, while an ordinary child first succeeds.
func TestLinuxProcessContainment(t *testing.T) {
	if os.Getenv("COMMANDER_TESTGUARD_PROBE") == "1" {
		if err := boundProcesses(); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("/bin/true").Run(); err != nil {
			t.Fatalf("ordinary child refused: %v", err)
		}
		fmt.Println("ordinary child succeeded")
		before := cgroupRefusals(t)
		var children []*exec.Cmd
		defer func() {
			for _, cmd := range children {
				_ = cmd.Process.Kill()
			}
			for _, cmd := range children {
				_ = cmd.Wait()
			}
		}()
		for i := 0; i < testTasksMax; i++ {
			cmd := exec.Command("/bin/sleep", "30")
			if err := cmd.Start(); err != nil {
				if len(children) == 0 {
					t.Fatalf("no workload headroom: %v", err)
				}
				if after := cgroupRefusals(t); after <= before {
					t.Fatalf("fork failed without a cgroup pids refusal: %v (events %d -> %d)", err, before, after)
				}
				fmt.Printf("kernel refused child %d: %v\n", i+1, err)
				return
			}
			children = append(children, cmd)
		}
		t.Fatal("kernel allowed 256 children in addition to helper threads")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxProcessContainment$", "-test.v")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, envTestBinary+"=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "COMMANDER_TESTGUARD_PROBE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bounded Linux helper: %v\n%s", err, out)
	}
	for _, want := range []string{"ordinary child succeeded", "kernel refused child"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	t.Log(string(out))
}

func cgroupRefusals(t *testing.T) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		path, ok := strings.CutPrefix(line, "0::/")
		if !ok {
			continue
		}
		dir := filepath.Join("/sys/fs/cgroup", path)
		for {
			if data, err := os.ReadFile(filepath.Join(dir, "pids.events")); err == nil {
				fields := strings.Fields(string(data))
				for i := 0; i+1 < len(fields); i += 2 {
					if fields[i] == "max" {
						n, err := strconv.ParseUint(fields[i+1], 10, 64)
						if err != nil {
							t.Fatal(err)
						}
						total += n
						found = true
					}
				}
			}
			if dir == "/sys/fs/cgroup" {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if !found {
		t.Fatal("no cgroup pids.events to verify kernel containment")
	}
	return total
}

func TestLinuxRefusesMissingIsolation(t *testing.T) {
	for _, scope := range []string{"", "unverified-marker"} {
		t.Run(scope, func(t *testing.T) {
			t.Setenv("COMMANDER_TESTGUARD_SCOPE", scope)
			t.Setenv("PATH", t.TempDir())
			root := t.TempDir()
			err := boundProcessesAt(filepath.Join(root, "missing-proc"), root)
			if err == nil || !strings.Contains(err.Error(), "pids") {
				t.Fatalf("missing isolation must fail with a cgroup requirement: %v", err)
			}
		})
	}
}
