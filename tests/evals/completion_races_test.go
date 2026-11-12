package evals

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

// Reproduce concurrent_verify.py's gates-table barrier with real CLI handlers.
// Either mutation may win. No order may leave a closed parent with unfinished
// children or a review wait, and every child must retain its actual parent.
func TestCompletionConcurrentCLIScenario(t *testing.T) {
	r := newBareCmdrRig(t)
	mustBuild(t, append(os.Environ(), "GOFLAGS=-p=2"), filepath.Join(r.bin, "cmdrd"), "./cmd/cmdrd")
	for _, review := range []bool{false, true} {
		name := "child"
		if review {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			st, dsn := pgtest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 45*time.Second))
			defer cancel()
			home := t.TempDir()
			project := t.TempDir()
			if e := os.Mkdir(filepath.Join(project, ".beads"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
				t.Fatal(e)
			}
			pid, e := st.InsertProject(ctx, store.Project{Name: "race", Path: project, Type: "knowledge", Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(?,'parent','Concurrent parent','open','["kind:objective","kind:workstream","until:ready"]','now','now')`, pid); e != nil {
				t.Fatal(e)
			}

			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			if e = os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf("[api]\nport = %d\n", port)), 0600); e != nil {
				t.Fatal(e)
			}
			daemon := exec.CommandContext(ctx, filepath.Join(r.bin, "cmdrd"), "--notifications=false")
			daemon.Env = append(append([]string{}, r.env...), "COMMANDER_HOME="+home)
			log, e := os.Create(filepath.Join(home, "daemon.log"))
			if e != nil {
				t.Fatal(e)
			}
			daemon.Stdout = log
			daemon.Stderr = log
			if e = daemon.Start(); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { daemon.Process.Kill(); daemon.Wait(); log.Close() })
			for end := time.Now().Add(10 * time.Second); ; {
				if _, e = os.Stat(filepath.Join(home, "api-url")); e == nil {
					break
				}
				if time.Now().After(end) {
					t.Fatal("private daemon did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			locked, release := make(chan struct{}), make(chan struct{})
			locker := make(chan error, 1)
			go func() {
				locker <- st.WithTx(ctx, func(q store.Querier) error {
					if _, e := q.ExecContext(ctx, `LOCK TABLE gates IN ACCESS EXCLUSIVE MODE`); e != nil {
						return e
					}
					close(locked)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			select {
			case <-locked:
			case e := <-locker:
				t.Fatal(e)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			run := func(args ...string) (*exec.Cmd, *bytes.Buffer) {
				c := exec.CommandContext(ctx, filepath.Join(r.bin, "cmdr"), args...)
				c.Env = append(append([]string{}, r.env...), "COMMANDER_HOME="+home)
				b := &bytes.Buffer{}
				c.Stdout = b
				c.Stderr = b
				if e := c.Start(); e != nil {
					t.Fatal(e)
				}
				return c, b
			}
			closeCmd, closeOut := run("internal-close-objective", "--project", "race", "--bead", "parent", "--reason", "Ready")
			blocked := false
			for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
				e = st.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='gates' AND NOT l.granted)`).Scan(&blocked)
				if e != nil {
					t.Fatal(e)
				}
				if blocked {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("close did not reach gates-table barrier")
			}
			args := []string{"internal-create-objective", "--project", "race", "--parent", "parent", "--title", "Concurrent child", "--waiting", "review:reviewer-a"}
			if review {
				args = []string{"internal-set-objective", "--project", "race", "--bead", "parent", "--review", "123", "--waiting", "review:reviewer-a"}
			}
			mutation, mutationOut := run(args...)
			// Give the mutation the same opportunity as concurrent_verify.py while the
			// close is suspended. A graph lock may correctly serialize it after close.
			time.Sleep(200 * time.Millisecond)
			close(release)
			released = true
			if e = <-locker; e != nil {
				t.Fatal(e)
			}
			closeErr, mutationErr := closeCmd.Wait(), mutation.Wait()
			var invalid bool
			e = st.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks p WHERE p.project_id=? AND p.id='parent' AND p.status='closed' AND (EXISTS(SELECT 1 FROM bead_tasks c WHERE c.project_id=p.project_id AND c.parent_id=p.id AND c.status NOT IN ('closed','tombstone')) OR p.labels::text LIKE '%reviewer-a%'))`, pid).Scan(&invalid)
			if e != nil {
				t.Fatal(e)
			}
			if invalid {
				t.Fatalf("concurrent close left wrong state: close=%v %s mutation=%v %s", closeErr, closeOut, mutationErr, mutationOut)
			}
			if closeErr == nil && mutationErr == nil {
				t.Fatalf("both conflicting operations succeeded: %s %s", closeOut, mutationOut)
			}
			if closeErr != nil && !strings.Contains(closeOut.String(), "unfinished children") && !strings.Contains(closeOut.String(), "review") {
				t.Fatalf("unexpected close failure: %s", closeOut)
			}

			t.Logf("gate barrier reached; close=%v %s mutation=%v %s", closeErr, closeOut, mutationErr, mutationOut)
		})
	}
}
