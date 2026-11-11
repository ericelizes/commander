package chat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type custodyResolver struct {
	Resolver
	keep bool
	err  error
}

func (r custodyResolver) PreserveRuntime(context.Context, string) (bool, error) { return r.keep, r.err }

func TestOrphanMetadataCannotStopCustodiedOrUncertainHost(t *testing.T) {
	for _, test := range []struct {
		name string
		keep bool
		err  error
	}{
		{"recorded-execution", true, nil},
		{"database-unavailable", false, errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.role("operator-custody")
			status := h.start("operator-custody")
			h.e.Close()
			m := readMeta(filepath.Join(h.home, "chat"), "operator-custody")
			m.Running = false
			if err := writeMeta(filepath.Join(h.home, "chat"), "operator-custody", m); err != nil {
				t.Fatal(err)
			}
			h.opts.Resolver = custodyResolver{Resolver: h.opts.Resolver, keep: test.keep, err: test.err}
			e := New(h.opts)
			defer e.Close()
			e.endOrphanHosts()
			if syscall.Kill(status.PID, 0) != nil {
				t.Fatal("cleanup killed a protected host")
			}
			hc, _, err := dialHost(hostSock(h.home, "operator-custody"))
			if err != nil {
				t.Fatal("protected host disappeared:", err)
			}
			defer hc.close()
			if err = hc.send(hostMsg{Op: "quit"}); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "private fake host exit", func() bool { return !alive(status.PID) })
		})
	}
}

func TestOrphanGraceCannotKillRecordedExecution(t *testing.T) {
	h := newHarness(t)
	sid := h.role("operator-preserved")
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	h.procs.set(sid, []Proc{{PID: child.Process.Pid, Command: "claude -p --input-format stream-json --resume " + sid}})
	h.opts.Resolver = custodyResolver{Resolver: h.opts.Resolver, keep: true}
	h.opts.OrphanGrace = time.Millisecond
	h.opts.ResumeRetry = time.Millisecond
	e := New(h.opts)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	e.resumeRole(ctx, "operator-preserved", func() {})
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("orphan grace killed recorded execution:", err)
	}
	if e.Running("operator-preserved") {
		t.Fatal("started duplicate execution")
	}
}

func TestStoreCustodyIncludesRawRunsAndDatabaseFailure(t *testing.T) {
	h := newHarness(t)
	st, resolver, _, rid, _ := nativeClaimFixture(t, h.home, "manager-preserve", "knowledge")
	for _, driver := range []string{"native", ""} {
		if _, err := st.DB().Exec("UPDATE agent_runs SET execution_driver=? WHERE id=?", driver, rid); err != nil {
			t.Fatal(err)
		}
		keep, err := resolver.PreserveRuntime(context.Background(), "manager.preserve")
		if err != nil || !keep {
			t.Fatalf("driver %q lost custody: %v %v", driver, keep, err)
		}
	}
	if _, err := st.DB().Exec("UPDATE agent_runs SET status='exited' WHERE id=?", rid); err != nil {
		t.Fatal(err)
	}
	keep, err := resolver.PreserveRuntime(context.Background(), "manager-preserve")
	if err != nil || keep {
		t.Fatal("terminal run retained custody", keep, err)
	}
	if _, err = st.DB().Exec("ALTER TABLE agent_runs RENAME TO unavailable_agent_runs"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{Home: os.TempDir(), Resolver: resolver})
	if !e.preserveRuntime(context.Background(), "manager-preserve") {
		t.Fatal("database failure allowed process cleanup")
	}
}
