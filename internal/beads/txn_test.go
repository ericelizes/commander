package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"testing"
	"time"
)

func TestTxn_BatchScript(t *testing.T) {
	script, ok := BatchScript([][]string{
		{"close", "x-1"},
		{"close", "x-4", "--force"},
		{"update", "x-2", "--status", "in_progress", "--assignee", "bob"},
		{"create", `Say "hi"`, "--type", "task", "--priority", "1"},
		{"dep", "add", "x-3", "x-2"},
		{"dep", "remove", "x-3", "x-2"},
	})
	want := "update x-1 status=closed\nupdate x-4 status=closed force=true\nupdate x-2 status=in_progress assignee=bob\ncreate task 1 \"Say \\\"hi\\\"\"\ndep add x-3 x-2\ndep remove x-3 x-2\n"
	if !ok || script != want {
		t.Fatalf("ok=%v script=%q", ok, script)
	}
	for _, op := range [][]string{
		{"create", "T", "--parent", "x-1"},
		{"create", "T", "--labels", "a"},
		{"update", "x-1", "--add-label", "a"},
		{"update", "x-1", "--title", "two\nlines"},
		{"label", "add", "x-1", "a"},
		{"comments", "add", "x-1", "hi"},
		{"close", "x-1", "--bogus"},
		{"close", "x-1", "--reason", "why"},
		{"update", "x-1", "--priority", "P1"},
		{"update", "x-1", "--priority", "9"},
		{"create", "T", "--priority", "7"},
		{"dep", "add", "x-1"},
	} {
		if _, ok := BatchScript([][]string{{"close", "x-9"}, op}); ok {
			t.Errorf("%v should not translate", op)
		}
	}
}

type calls struct {
	mu      sync.Mutex
	ran     [][]string
	scripts []string
	failAt  int // 1-based op number whose argv run fails; 0 = none
	batchEr error
}

func (c *calls) run(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ran = append(c.ran, args)
	if c.failAt == len(c.ran) {
		return nil, []byte("boom"), errors.New("exit 1")
	}
	return []byte("ok;"), nil, nil
}

func (c *calls) batch(ctx context.Context, dir, script string) ([]byte, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scripts = append(c.scripts, script)
	return nil, nil, c.batchEr
}

func TestTxn_FittingOpsAreOneBatch(t *testing.T) {
	c := &calls{}
	resp := ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1"}, {"create", "Next"}, {"update", "x-2", "--assignee", "a"}}, true, true, c.run, c.batch)
	if resp.Exit != 0 || !resp.Atomic || resp.Applied != 3 || len(c.scripts) != 1 || len(c.ran) != 0 {
		t.Fatalf("resp=%+v scripts=%v ran=%v", resp, c.scripts, c.ran)
	}
}

// All or nothing: a failing batch reports nothing applied and never falls back to one by one.
func TestTxn_FailedBatchAppliesNothing(t *testing.T) {
	c := &calls{batchEr: errors.New("line 2: no such issue")}
	resp := ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1"}, {"close", "nope"}}, false, true, c.run, c.batch)
	if resp.Exit == 0 || resp.Applied != 0 || !strings.Contains(resp.Error, "rolled back") || len(c.ran) != 0 {
		t.Fatalf("resp=%+v ran=%v", resp, c.ran)
	}
}

func TestTxn_AtomicRefusesWhatCannotBeOneTransaction(t *testing.T) {
	c := &calls{}
	ops := [][]string{{"close", "x-1"}, {"label", "add", "x-1", "done"}}
	for _, batchOK := range []bool{true, false} {
		resp := ApplyOps(context.Background(), "/p", ops, true, batchOK, c.run, c.batch)
		if resp.Exit == 0 || resp.Applied != 0 || !strings.Contains(resp.Error, ErrNotAtomic.Error()) {
			t.Fatalf("batchOK=%v resp=%+v", batchOK, resp)
		}
	}
	if len(c.ran) != 0 || len(c.scripts) != 0 {
		t.Fatalf("something ran: %v %v", c.ran, c.scripts)
	}
}

// Without Atomic the rest runs in order, stops at the first failure and says so.
func TestTxn_NonAtomicStopsAtFirstFailureAndSaysWhatApplied(t *testing.T) {
	c := &calls{failAt: 2}
	ops := [][]string{{"close", "x-1"}, {"label", "add", "x-1", "a"}, {"close", "x-2"}}
	resp := ApplyOps(context.Background(), "/p", ops, false, true, c.run, c.batch)
	if resp.Exit == 0 || resp.Applied != 1 || resp.Atomic || len(c.ran) != 2 || !strings.Contains(resp.Error, "not atomic") {
		t.Fatalf("resp=%+v ran=%v", resp, c.ran)
	}
}

func TestTxn_SingleOpIsPlainWrite(t *testing.T) {
	c := &calls{}
	resp := ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1"}}, false, true, c.run, c.batch)
	if resp.Exit != 0 || resp.Applied != 1 || len(c.ran) != 1 || len(c.scripts) != 0 {
		t.Fatalf("resp=%+v", resp)
	}
}

// Writes of one project are not queued: they overlap, bounded only by the limiter.
func TestWrite_RunWriteDoesNotSerialize(t *testing.T) {
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("COMMANDER_HOME", t.TempDir())
	bin := fakeBr(t, `sleep 0.3`)
	dir := t.TempDir()
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := RunWrite(context.Background(), dir, bin, "close", "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 550*time.Millisecond {
		t.Fatalf("two writes of one project took %v: they ran one after the other", d)
	}
}

// Against a real bd server in a throwaway project (COMMANDER_BD_INTEGRATION=1):
// a batch with one bad line leaves nothing behind; a good one applies whole.
func TestTxn_BDBatchIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	a, err := c.Create(ctx, dir, "A", CreateOptions{Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		bs, err := c.List(ctx, dir, ListOptions{Status: "all"})
		if err != nil {
			t.Fatal(err)
		}
		return len(bs)
	}
	before := count()
	_, err = Txn(ctx, dir, [][]string{{"create", "Half"}, {"close", a}, {"close", "nope-999"}}, true)
	if err == nil {
		t.Fatal("a batch with an unknown id succeeded")
	}
	bs, _ := c.List(ctx, dir, ListOptions{Status: "all"})
	for _, b := range bs {
		if b.ID == a && b.Status == "closed" {
			t.Fatal("close was applied although the batch failed")
		}
	}
	if count() != before {
		t.Fatal("the create was applied although the batch failed")
	}
	resp, err := Txn(ctx, dir, [][]string{{"create", "Whole"}, {"close", a}}, true)
	if err != nil || !resp.Atomic || resp.Applied != 2 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if count() != before+1 {
		t.Fatal("the create of a good batch is missing")
	}
}

// bdFake mimics bd batch's close policy: `close` skips it, `update status=closed`
// applies it (refused with open children unless force=true), the whole batch
// rolling back.
type bdFake struct{ openChildren map[string]bool }

func (f bdFake) batch(ctx context.Context, dir, script string) ([]byte, []byte, error) {
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		w := strings.Fields(line)
		if w[0] == "update" && strings.Contains(line, "status=closed") && f.openChildren[w[1]] && !strings.Contains(line, "force=true") {
			return nil, []byte("cannot close " + w[1] + ": open children"), errors.New("exit 1")
		}
		if w[0] == "close" {
			return []byte("closed " + w[1] + " (policy skipped)"), nil, nil
		}
	}
	return []byte("ok"), nil, nil
}

// An atomic close refuses what a direct close refuses, and force overrides it.
func TestTxn_BatchCloseKeepsTheClosePolicy(t *testing.T) {
	c := &calls{}
	fake := bdFake{openChildren: map[string]bool{"x-1": true}}
	resp := ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1"}, {"create", "N"}}, true, true, c.run, fake.batch)
	if resp.Exit == 0 || resp.Applied != 0 || !strings.Contains(resp.Stderr, "open children") {
		t.Fatalf("a close with open children must be refused: %+v", resp)
	}
	resp = ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1", "--force"}, {"create", "N"}}, true, true, c.run, fake.batch)
	if resp.Exit != 0 || !resp.Atomic || resp.Applied != 2 {
		t.Fatalf("forced close: %+v", resp)
	}
}

func TestTxn_CutBatchSaysItMayHaveBeenApplied(t *testing.T) {
	cut := func(ctx context.Context, dir, script string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("%w: signal: killed", context.DeadlineExceeded)
	}
	resp := ApplyOps(context.Background(), "/p", [][]string{{"close", "x-1"}, {"close", "x-2"}}, true, true, nil, cut)
	if resp.Exit == 0 || strings.Contains(resp.Error, "rolled back") || !strings.Contains(resp.Error, "may have been applied") {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestTxn_OpsCap(t *testing.T) {
	var ops [][]string
	for i := 0; i < MaxOps+1; i++ {
		ops = append(ops, []string{"close", "x"})
	}
	if OpsWritable(ops) || !OpsWritable(ops[:MaxOps]) {
		t.Fatal("cap not enforced")
	}
}

func TestTxn_NonAtomicStopsAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		cancel()
		return nil, nil, nil
	}
	resp := ApplyOps(ctx, "/p", [][]string{{"label", "add", "x", "a"}, {"label", "add", "x", "b"}}, false, true, run, nil)
	if resp.Exit == 0 || resp.Applied != 1 || !strings.Contains(resp.Error, "deadline") {
		t.Fatalf("resp=%+v", resp)
	}
}

// Renewing a stale workflow step closes a closed bead again: it must stay a
// no-op success on the real backend, or the door would refuse the renewal.
func TestCloseOfAClosedBead_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	a, err := c.Create(ctx, dir, "A", CreateOptions{Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := c.Close(ctx, dir, a); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	b, err := c.Show(ctx, dir, a)
	if err != nil || b == nil || b.Status != "closed" {
		t.Fatalf("%v %v", b, err)
	}
}

// Spawn finds a create whose answer was lost by its marker label: the real
// backend must take the label on a child create and list it back, with a
// creation time lookForCreate can read.
func TestCreateMarkerListsBack_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	parent, err := c.Create(ctx, dir, "step", CreateOptions{Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Create(ctx, dir, "t", CreateOptions{Parent: parent, Labels: []string{"kind:task", "spawn-attempt:0123456789abcdef"}, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(ctx, dir, ListOptions{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range list {
		if b.ID != id {
			continue
		}
		if !slices.Contains(b.Labels, "spawn-attempt:0123456789abcdef") {
			t.Fatalf("labels %v", b.Labels)
		}
		if _, err := time.Parse(time.RFC3339, b.CreatedAt); err != nil {
			t.Fatalf("created at %q: %v", b.CreatedAt, err)
		}
		return
	}
	t.Fatalf("%s not listed in %+v", id, list)
}

// The reads a close is judged on, against a real bd: --id with --status all keeps a
// closed bead and drops a missing one, and --direction up finds what waits.
func TestNarrowReads_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	var ids []string
	for _, title := range []string{"block", "target", "other"} {
		id, err := c.Create(ctx, dir, title, CreateOptions{Labels: []string{"kind:objective"}, Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	block, target, other := ids[0], ids[1], ids[2]
	if err := c.DependOn(ctx, dir, target, block); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, dir, other); err != nil {
		t.Fatal(err)
	}
	listed, err := c.List(ctx, dir, ListOptions{Status: "all", IDs: []string{other, block, "nobody-1"}})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, b := range listed {
		got = append(got, b.ID)
	}
	slices.Sort(got)
	want := []string{other, block}
	slices.Sort(want)
	if !slices.Equal(got, want) || !slices.Contains(listed[0].Labels, "kind:objective") {
		t.Fatalf("listed %+v, want %v with labels", listed, want)
	}
	deps, err := c.Dependents(ctx, dir, block)
	if err != nil || !slices.Equal(deps, []Dep{{IssueID: target, DependsOnID: block, Type: "blocks"}}) {
		t.Fatalf("dependents %+v err %v", deps, err)
	}
}
