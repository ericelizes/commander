//go:build linux

package reviewrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewScopeEndsSetsidDescendant(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			r := fixture(t)
			pidFile := filepath.Join(t.TempDir(), "pid")
			body := `setsid /bin/sh -c 'echo $$ > "$1"; exec sleep 30' detached "$1" & while [ ! -s "$1" ]; do sleep 0.01; done`
			if timeout {
				body += "; sleep 30"
			}
			r.Command = []string{"/bin/sh", "-c", body, "review", pidFile}
			budget := 5 * time.Second
			if timeout {
				budget = time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			result, err := Run(ctx, r)
			if !timeout && err != nil {
				t.Fatalf("%+v %v", result, err)
			}
			if timeout && (err == nil || result.Execution != "blocked") {
				t.Fatalf("%+v %v", result, err)
			}
			b, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 1 {
				t.Fatal("invalid owned child pid")
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if err := syscall.Kill(pid, 0); err != nil {
					return
				}
				stat, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if strings.Contains(string(stat), ") Z ") {
					return
				}
			}
			t.Fatal("detached reviewer survived owned scope cleanup")
		})
	}
}
