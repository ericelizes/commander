package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// repairBD is the real thing around one invented project: the built cmdr, a real
// write door and a real bd on a private temp server.
type repairBD struct {
	t        *testing.T
	c        beads.Client
	dir      string
	home     string
	bin      string
	root     string
	wrappers []string // the track, then four wrappers, outermost first
	leaf     string
	gate     string
	drop     atomic.Pointer[func(beads.DoorRequest) bool] // apply the write, lose its answer
	writes   atomic.Int64
}

func newRepairBD(t *testing.T, unrelated int) *repairBD {
	t.Helper()
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_ROLE", "")
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	// This process serves the door, so its own writes run directly.
	beads.SetDoorServer(true)
	t.Cleanup(func() { beads.SetDoorServer(false) })

	ctx := context.Background()
	r := &repairBD{t: t, c: beads.New(), dir: t.TempDir(), home: home, bin: filepath.Join(t.TempDir(), "cmdr")}
	if out, err := exec.Command("git", "-C", r.dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := r.c.Init(ctx, r.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", r.dir, "dolt", "stop").Run() })

	for done := 0; done < unrelated; done += 100 {
		var ops [][]string
		for i := range min(100, unrelated-done) {
			ops = append(ops, []string{"create", fmt.Sprintf("Unrelated work %d", done+i)})
		}
		script, _ := beads.BatchScript(ops)
		if _, stderr, err := beads.RunBatch(ctx, r.dir, script); err != nil {
			t.Fatalf("seed: %v %s", err, stderr)
		}
	}

	r.root = r.create("Sample flow", "", "kind:objective")
	parent := r.root
	for i, title := range []string{"Sample track", "Wrapper a", "Wrapper b", "Wrapper c", "Wrapper d"} {
		labels := []string{"kind:objective"}
		if i == 0 {
			labels = append(labels, beads.TrackLabel)
		}
		id := r.create(title, "", labels...)
		r.must(r.c.SetParent(ctx, r.dir, id, parent))
		r.wrappers = append(r.wrappers, id)
		parent = id
	}
	r.leaf = r.create("Sample action", parent, "kind:objective")
	r.gate = r.create("Approve the sample pilot", "", "kind:gate", "gate-of:"+beads.EscapeLabel(r.leaf), "gate-name:pilot", "verifier:user", "approver:user", "proposed:gate:1")
	r.must(r.c.AddComment(ctx, r.dir, r.gate, "Original decision history"))
	for _, w := range append(slices.Clone(r.wrappers), r.leaf) {
		r.must(r.c.DependOn(ctx, r.dir, w, r.gate))
	}

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(ctx, store.Project{Name: "sample", Type: "code", Path: r.dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}

	const token = "sample-token"
	door := api.New(api.Options{Token: token, Source: func(context.Context) livedata.Snapshot { return livedata.Snapshot{} }}).Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != beads.DoorPath {
			door.ServeHTTP(w, q)
			return
		}
		r.writes.Add(1)
		body, _ := io.ReadAll(q.Body)
		q.Body = io.NopCloser(bytes.NewReader(body))
		var req beads.DoorRequest
		_ = json.Unmarshal(body, &req)
		if f := r.drop.Load(); f != nil && (*f)(req) {
			r.drop.Store(nil)
			door.ServeHTTP(httptest.NewRecorder(), q)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		door.ServeHTTP(w, q)
	}))
	t.Cleanup(ts.Close)
	r.must(os.WriteFile(filepath.Join(home, "api-url"), []byte(ts.URL), 0o600))
	r.must(os.WriteFile(filepath.Join(home, "api-token"), []byte(token), 0o600))

	if out, err := exec.Command("go", "build", "-p", "1", "-o", r.bin, "../../cmd/cmdr").CombinedOutput(); err != nil {
		t.Fatalf("build cmdr: %v\n%s", err, out)
	}
	return r
}

func (r *repairBD) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *repairBD) create(title, parent string, labels ...string) string {
	r.t.Helper()
	id, err := r.c.Create(context.Background(), r.dir, title, beads.CreateOptions{Parent: parent, Labels: labels, Description: title + " scope", Silent: true})
	r.must(err)
	return id
}

// publish runs the real verb as the owning Manager.
func (r *repairBD) publish(extra ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command(r.bin, append([]string{"internal-gate", "publish", "--project", "sample", "--bead", r.gate, "--parent", r.wrappers[len(r.wrappers)-1]}, extra...)...)
	cmd.Dir = r.t.TempDir()
	cmd.Env = append(os.Environ(), "COMMANDER_HOME="+r.home, "COMMANDER_BEADS_BIN=bd", "COMMANDER_ROLE=manager-"+r.root)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *repairBD) repair(extra ...string) (string, error) {
	r.t.Helper()
	return r.publish(append([]string{"--repair-ancestor-waits", strings.Join(r.wrappers, ","), "--reason", "Publish the existing decision"}, extra...)...)
}

func (r *repairBD) details() map[string]beads.Detail {
	r.t.Helper()
	ds, err := beads.FreshDetails(context.Background(), r.c, r.dir, append(slices.Clone(r.wrappers), r.leaf, r.gate))
	r.must(err)
	return ds
}

func (r *repairBD) waiting() (out []string) {
	r.t.Helper()
	ds := r.details()
	for _, id := range append(slices.Clone(r.wrappers), r.leaf) {
		for _, e := range ds[id].Deps {
			if e.Type == "blocks" && e.DependsOnID == r.gate {
				out = append(out, id)
			}
		}
	}
	return out
}

func (r *repairBD) ready() []string {
	r.t.Helper()
	cmd := exec.Command("bd", "ready", "--json", "-n", "0")
	cmd.Dir = r.dir
	raw, err := cmd.Output()
	r.must(err)
	var rows []struct {
		ID string `json:"id"`
	}
	r.must(json.Unmarshal(raw, &rows))
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// The five-wrapper shape on real bd: its cycle rule, its edge and comment reads,
// and every write through the real door, with lost answers on the way.
func TestPublicationRepairFiveWrappers_BDIntegration(t *testing.T) {
	r := newRepairBD(t, 0)
	ctx := context.Background()
	card := func() beads.Bead {
		b, err := r.c.Show(ctx, r.dir, r.gate)
		r.must(err)
		cp := *b
		cp.ParentID = ""
		slices.Sort(cp.Labels)
		return cp
	}
	before := card()
	all := append(slices.Clone(r.wrappers), r.leaf)

	// bd's own cycle rule refuses the plain publish, and the refusal says what to do.
	if out, err := r.publish(); err == nil || !strings.Contains(out, "cycle") || !strings.Contains(out, "--repair-ancestor-waits") {
		t.Fatalf("plain publish: %v %s", err, out)
	}

	// Other work under a wrapper refuses until it has a wait of its own.
	sibling := r.create("Sample sibling", r.wrappers[2], "kind:objective")
	if out, err := r.repair("--dry-run"); err == nil || !strings.Contains(out, "would release live "+sibling) {
		t.Fatalf("sibling: %v %s", err, out)
	}
	if out, err := r.repair(); err == nil || !strings.Contains(out, "would release live "+sibling) {
		t.Fatalf("sibling, apply: %v %s", err, out)
	}
	if got := r.waiting(); !reflect.DeepEqual(got, all) {
		t.Fatalf("a refusal wrote: %v", got)
	}
	r.must(r.c.DependOn(ctx, r.dir, sibling, r.gate))

	// Dry run: fresh reads pass, nothing written, the door never called.
	r.writes.Store(0)
	if out, err := r.repair("--dry-run"); err != nil {
		t.Fatalf("dry run: %v %s", err, out)
	}
	if got := r.writes.Load(); got != 0 || !reflect.DeepEqual(r.waiting(), all) {
		t.Fatalf("dry run wrote: %d door calls, waits %v", got, r.waiting())
	}

	// Apply: the answer to the third removal is lost after bd applied it.
	removals := 0
	dropThird := func(q beads.DoorRequest) bool {
		if len(q.Args) > 1 && q.Args[0] == "dep" && q.Args[1] == "remove" {
			removals++
			return removals == 3
		}
		return false
	}
	r.drop.Store(&dropThird)
	if out, err := r.repair(); err == nil || !strings.Contains(out, "repeat exact request") {
		t.Fatalf("lost removal: %v %s", err, out)
	}
	if got := r.waiting(); len(got) != 3 || !slices.Contains(got, r.leaf) {
		t.Fatalf("after three removals: %v", got)
	}
	if r.details()[r.gate].Parent != "" {
		t.Fatal("published before the removals finished")
	}

	// The exact repeat finishes the removals; now the parent write's answer is lost.
	dropParent := func(q beads.DoorRequest) bool { return slices.Contains(q.Args, "--parent") }
	r.drop.Store(&dropParent)
	if out, err := r.repair(); err == nil || !strings.Contains(out, "repeat exact request") {
		t.Fatalf("lost parent: %v %s", err, out)
	}
	if r.details()[r.gate].Parent != r.wrappers[len(r.wrappers)-1] {
		t.Fatal("the lost parent write did not land")
	}

	// Two more repeats finish it and change nothing further.
	for range 2 {
		if out, err := r.repair(); err != nil {
			t.Fatalf("repeat: %v %s", err, out)
		}
	}
	if got := r.waiting(); !reflect.DeepEqual(got, []string{r.leaf}) {
		t.Fatalf("waits left: %v", got)
	}
	ds := r.details()
	if ds[r.gate].Parent != r.wrappers[len(r.wrappers)-1] {
		t.Fatalf("parent %q", ds[r.gate].Parent)
	}
	var history []string
	for _, c := range ds[r.gate].Comments {
		history = append(history, c.Text)
	}
	if len(history) != 3 || history[0] != "Original decision history" || !strings.HasSuffix(history[2], " completed") {
		t.Fatalf("comments: %q", history)
	}
	if after := card(); !reflect.DeepEqual(before, after) {
		t.Fatalf("the card changed:\n%+v\n%+v", before, after)
	}
	for _, id := range []string{sibling, r.leaf} {
		if slices.Contains(r.ready(), id) {
			t.Fatalf("%s is ready with the gate still open", id)
		}
	}
	if _, err := os.Stat(filepath.Join(r.home, "logs", "write-door-fallback.jsonl")); err == nil {
		raw, _ := os.ReadFile(filepath.Join(r.home, "logs", "write-door-fallback.jsonl"))
		t.Fatalf("a write skipped the door: %s", raw)
	}
}

// COMMANDER_REPAIR_SCALE_BEADS=5000 times one straight repair of the same shape
// in a project that size.
func TestPublicationRepairScale_BDIntegration(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("COMMANDER_REPAIR_SCALE_BEADS"))
	if n <= 0 {
		t.Skip("set COMMANDER_REPAIR_SCALE_BEADS to time a repair at that size")
	}
	r := newRepairBD(t, n)
	start := time.Now()
	if out, err := r.repair("--dry-run"); err != nil {
		t.Fatalf("dry run: %v %s", err, out)
	}
	dry := time.Since(start)
	start = time.Now()
	if out, err := r.repair(); err != nil {
		t.Fatalf("repair: %v %s", err, out)
	}
	t.Logf("%d unrelated beads: dry run %s, repair %s, door writes %d", n, dry.Round(time.Second), time.Since(start).Round(time.Second), r.writes.Load())
}
