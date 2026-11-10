package beads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

type qualifiedFixture struct {
	c              Client
	dir, foreign   string
	s              *store.Store
	source, target string
	pid, other     int64
}

func newQualifiedFixture(t *testing.T) *qualifiedFixture {
	t.Helper()
	c, dir, s := postgresBeads(t)
	ctx := context.Background()
	foreign := t.TempDir()
	p, e := s.InsertProject(ctx, store.Project{Name: "fixture-target", Type: "code", Path: foreign, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	source, e := c.Create(ctx, dir, "Synthetic source", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	target, e := c.Create(ctx, foreign, "Synthetic target", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		t.Fatal(e)
	}
	return &qualifiedFixture{c, dir, foreign, s, source, target, pid, p}
}
func (f *qualifiedFixture) link(ctx context.Context) error {
	return QualifiedDependency(ctx, f.dir, f.foreign, "add", f.source, f.target)
}
func TestPostgresQualifiedLifecycle(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	for _, c := range []Client{f.c, NewCached(f.c), ExportReads(NewCached(f.c)), &StateWrite{Client: f.c}} {
		if e := ReadCustodySnapshot(ctx, c, f.dir, func(ctx context.Context) error {
			b, e := FreshShow(ctx, c, f.dir, f.target)
			if e != nil || b.ID != f.target {
				t.Fatalf("foreign show %v %v", b, e)
			}
			ds, e := FreshDetails(ctx, c, f.dir, []string{f.source, f.target})
			if e != nil || len(ds[f.source].Deps) != 1 {
				t.Fatalf("details %v %v", ds, e)
			}
			bs, e := c.List(ctx, f.dir, ListOptions{IDs: []string{f.target}, Status: "all"})
			if e != nil || len(bs) != 1 {
				t.Fatalf("list %v %v", bs, e)
			}
			deps, e := c.Dependents(ctx, f.foreign, f.target)
			if e != nil || len(deps) != 1 {
				t.Fatalf("dependents %v %v", deps, e)
			}
			if e = RequireLocalCustody(ctx, c, f.foreign, []string{f.source}); !IsCrossProjectError(e) {
				t.Fatalf("foreign receipt: %v", e)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	// Closing the blocker would need a receipt owned by another project; both
	// direct calls and no-Intent multi-op requests must roll back everything.
	var events int
	f.s.DB().QueryRow(`SELECT COUNT(*) FROM state_events`).Scan(&events)
	if e := f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("foreign release accepted: %v", e)
	}
	resp := postgresApplyOps(ctx, f.foreign, [][]string{{"update", f.target, "--title", "Rollback me"}, {"close", f.target}})
	if resp.Exit == 0 || resp.Applied != 0 {
		t.Fatalf("partial write: %+v", resp)
	}
	var now int
	f.s.DB().QueryRow(`SELECT COUNT(*) FROM state_events`).Scan(&now)
	if now != events {
		t.Fatalf("refusal wrote events %d -> %d", events, now)
	}
	b, e := f.c.Show(ctx, f.foreign, f.target)
	if e != nil || b.Status != "open" || b.Title != "Synthetic target" {
		t.Fatalf("rollback: %v %v", b, e)
	}
	// Genuine source-local custody permits the target close without any foreign write.
	if e = f.c.SetLabel(ctx, f.dir, f.source, (Wait{Kind: WaitReview, Ref: "fixture", Since: time.Now()}).Label(), true); e != nil {
		t.Fatal(e)
	}
	var rev int64
	rev, e = PostgresProjectRevision(ctx, f.dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.Close(ctx, f.foreign, f.target); e != nil {
		t.Fatal(e)
	}
	next, e := PostgresProjectRevision(ctx, f.dir)
	if e != nil || next <= rev {
		t.Fatalf("foreign revision %d %d %v", rev, next, e)
	}
	b, e = FreshShow(ctx, f.c, f.dir, f.target)
	if e != nil || b.Status != "closed" {
		t.Fatalf("foreign completion: %v %v", b, e)
	}
	if e = QualifiedDependency(ctx, f.dir, f.foreign, "rm", f.source, f.target); e != nil {
		t.Fatal(e)
	}
	ds, e := f.c.Deps(ctx, f.dir, f.source)
	if e != nil || len(ds) != 0 {
		t.Fatalf("remove: %v %v", ds, e)
	}
	if _, e = f.c.Show(ctx, f.dir, f.target); !errors.Is(e, ErrNotFound) {
		t.Fatalf("arbitrary redirect after unlink %v", e)
	}
}
func TestPostgresQualifiedIdentityAndGateRefusals(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := QualifiedDependency(ctx, f.foreign, f.dir, "add", f.source, f.target); !IsCrossProjectError(e) {
		t.Fatalf("wrong projects accepted: %v", e)
	}
	if e := f.c.SetLabel(ctx, f.foreign, f.target, GateLabel, true); e != nil {
		t.Fatal(e)
	}
	if e := f.link(ctx); !IsCrossProjectError(e) {
		t.Fatalf("foreign gate accepted: %v", e)
	}
	if e := f.c.SetLabel(ctx, f.foreign, f.target, GateLabel, false); e != nil {
		t.Fatal(e)
	}
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	// A later duplicate introduced by import remains legal storage, but invalidates
	// every reached read, even when the duplicate is in a third registered project.
	third := t.TempDir()
	p, e := f.s.InsertProject(ctx, store.Project{Name: "fixture-third", Type: "code", Path: third, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) SELECT ?,id,title,created_at,updated_at FROM bead_tasks WHERE project_id=? AND id=?`, p, f.other, f.target); e != nil {
		t.Fatal(e)
	}
	for name, read := range map[string]func() error{
		"list":       func() error { _, e := f.c.List(ctx, f.dir, ListOptions{}); return e },
		"show":       func() error { _, e := FreshShow(ctx, f.c, f.dir, f.source); return e },
		"details":    func() error { _, e := FreshDetails(ctx, f.c, f.dir, []string{f.source}); return e },
		"deps":       func() error { _, e := f.c.Deps(ctx, f.dir, f.source); return e },
		"dependents": func() error { _, e := f.c.Dependents(ctx, f.foreign, f.target); return e },
	} {
		if e := read(); !IsCrossProjectError(e) {
			t.Errorf("%s accepted ambiguity: %v", name, e)
		}
	}
	if e := f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("ambiguous mutation: %v", e)
	}
}
func TestPostgresQualifiedOppositeLinksSerialize(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, reverse := range []bool{false, true} {
		wg.Add(1)
		go func(reverse bool) {
			defer wg.Done()
			<-start
			if reverse {
				errs <- QualifiedDependency(ctx, f.foreign, f.dir, "add", f.target, f.source)
			} else {
				errs <- f.link(ctx)
			}
		}(reverse)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, refused := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if IsCrossProjectError(e) {
			refused++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("opposite links success=%d refused=%d", success, refused)
	}
}

func TestPostgresQualifiedSourceAuthority(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	root, e := f.c.Create(ctx, f.dir, "Synthetic workstream", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.SetParent(ctx, f.dir, f.source, root); e != nil {
		t.Fatal(e)
	}
	if e = f.s.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: f.pid, WorkstreamBeadID: root, AmRole: "manager-fixture-root", SessionFile: "fixture-root"}); e != nil {
		t.Fatal(e)
	}
	if e = f.s.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: f.pid, WorkstreamBeadID: f.source, AmRole: "manager-fixture-nested", SessionFile: "fixture-nested"}); e != nil {
		t.Fatal(e)
	}
	for _, role := range []string{"manager-fixture-root", "manager-fixture-other", "operator-fixture-unclaimed"} {
		actor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + role})
		if e = f.link(actor); !IsCrossProjectError(e) {
			t.Fatalf("accepted %s: %v", role, e)
		}
	}
	actor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=manager-fixture-nested"})
	if e = f.link(actor); e != nil {
		t.Fatal(e)
	}
	if e = QualifiedDependency(actor, f.dir, f.foreign, "rm", f.source, f.target); e != nil {
		t.Fatal(e)
	}
	run, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: "operator-fixture-owner", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	actor = WithDoorEnv(ctx, []string{"COMMANDER_ROLE=operator-fixture-owner"})
	if e = f.link(actor); e != nil {
		t.Fatal(e)
	}
	if e = QualifiedDependency(actor, f.dir, f.foreign, "rm", f.source, f.target); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`UPDATE agent_runs SET status='done' WHERE id=?`, run); e != nil {
		t.Fatal(e)
	}
	if e = f.link(actor); !IsCrossProjectError(e) {
		t.Fatalf("stale Operator accepted: %v", e)
	}
}

func TestPostgresQualifiedSnapshotAndDeletion(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	err := ReadCustodySnapshot(ctx, f.c, f.dir, func(read context.Context) error {
		before, e := PostgresProjectRevision(read, f.dir)
		if e != nil {
			return e
		}
		if e = f.c.Rename(ctx, f.foreign, f.target, "Later target"); e != nil {
			return e
		}
		b, e := FreshShow(read, f.c, f.dir, f.target)
		if e != nil {
			return e
		}
		if b.Title != "Synthetic target" {
			t.Fatalf("mixed MVCC snapshot: %v", b)
		}
		after, e := PostgresProjectRevision(read, f.dir)
		if e != nil {
			return e
		}
		if before != after {
			t.Fatalf("revision changed inside MVCC %d %d", before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, e := FreshShow(ctx, f.c, f.dir, f.target)
	if e != nil || b.Title != "Later target" {
		t.Fatalf("new snapshot: %v %v", b, e)
	}
	// A stale imported tombstone must never satisfy a reached qualified endpoint.
	if _, e = f.s.DB().Exec(`UPDATE bead_tasks SET deleted_at=1 WHERE project_id=? AND id=?`, f.other, f.target); e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.List(ctx, f.dir, ListOptions{}); !IsCrossProjectError(e) || !errors.Is(e, ErrNotFound) {
		t.Fatalf("tombstone read: %v", e)
	}
}

func TestPostgresQualifiedInteractionScenario(t *testing.T) {
	raw, e := os.ReadFile("../../tests/evals/scenarios/qualified-postgres-lifecycle.json")
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Cases []struct {
			Name   string
			Args   []string
			Status string
		}
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			f := newQualifiedFixture(t)
			ctx := context.Background()
			if e := f.link(ctx); e != nil {
				t.Fatal(e)
			}
			op := append([]string{tc.Args[0], f.target}, tc.Args[1:]...)
			if r := postgresApplyOps(ctx, f.foreign, [][]string{op}); r.Exit == 0 || r.Applied != 0 {
				t.Fatalf("foreign receipt accepted: %+v", r)
			}
			if e := f.c.SetLabel(ctx, f.dir, f.source, (Wait{Kind: WaitReview, Ref: "fixture-reviewer", Since: time.Now()}).Label(), true); e != nil {
				t.Fatal(e)
			}
			if r := postgresApplyOps(ctx, f.foreign, [][]string{op}); r.Exit != 0 {
				t.Fatalf("source-local custody ignored: %+v", r)
			}
			b, e := FreshShow(ctx, f.c, f.dir, f.target)
			if e != nil || b.Status != tc.Status {
				t.Fatalf("transition %v %v", b, e)
			}
		})
	}
}

func TestPostgresQualifiedBoundAndLocalParent(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.DB().Exec(`INSERT INTO bead_dependencies(project_id,issue_id,target_project_id,depends_on_id,type) VALUES(?,?,?,?,'parent-child')`, f.pid, f.source, f.other, f.target); e == nil {
		t.Fatal("foreign parent escaped database constraint")
	}
	if _, e := f.s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) SELECT ?, 'fixture-bound-'||n, 'Synthetic', 'now','now' FROM generate_series(1,4096) n`, f.pid); e != nil {
		t.Fatal(e)
	}
	if _, e := f.c.List(ctx, f.dir, ListOptions{}); !IsCrossProjectError(e) {
		t.Fatalf("oversized graph accepted: %v", e)
	}
}

func TestPostgresQualifiedLocalEditsKeepTheirSemantics(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	extra, e := f.c.Create(ctx, f.dir, "Synthetic local peer", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.link(ctx); e != nil {
		t.Fatal(e)
	}
	// A source having another foreign edge still accepts ordinary local edges.
	if e = f.c.DependOn(ctx, f.dir, f.source, extra); e != nil {
		t.Fatal(e)
	}
	if e = f.c.RemoveDep(ctx, f.dir, f.source, extra); e != nil {
		t.Fatal(e)
	}
	if e = QualifiedDependency(ctx, f.foreign, f.dir, "add", f.target, extra); e != nil {
		t.Fatal(e)
	}
	// This local edge would complete a cycle through both projects.
	if e = f.c.DependOn(ctx, f.dir, extra, f.source); !IsCrossProjectError(e) {
		t.Fatalf("local global-cycle completion accepted: %v", e)
	}
}

func TestPostgresQualifiedManagerNameIsNotForeignCustody(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := f.c.SetLabel(ctx, f.dir, f.source, "owner:manager-fixture", true); e != nil {
		t.Fatal(e)
	}
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("Manager name stood in for foreign custody: %v", e)
	}
}

func TestPostgresQualifiedStoppedAndUnknownCustody(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	role := "operator-fixture-presence"
	if e := f.c.SetLabel(ctx, f.dir, f.source, "owner:"+role, true); e != nil {
		t.Fatal(e)
	}
	run, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.link(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`INSERT INTO run_observations(run_id,alive,checked_at) VALUES(?,FALSE,?)`, run, time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	if e = f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("stopped process excused foreign receipt: %v", e)
	}
	if _, e = f.s.DB().Exec(`UPDATE run_observations SET checked_at=0 WHERE run_id=?`, run); e != nil {
		t.Fatal(e)
	}
	// Expired evidence is unknown, matching the existing holder policy. Do not
	// invent either stopped or a working turn from the missing current sample.
	if e = f.c.Close(ctx, f.foreign, f.target); e != nil {
		t.Fatalf("unknown evidence became stopped: %v", e)
	}
}

func TestPostgresQualifiedRunRetirementSerializesWithAuthority(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	run, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: "operator-fixture-race", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	actor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=operator-fixture-race"})
	e = f.s.WithTx(ctx, func(q store.Querier) error {
		// This is the same serialization interval as an actual qualified write.
		if _, e := pgGuardedOps(actor, q, f.dir, [][]string{{"dep", "add", f.source, f.target, "--source-project", f.dir, "--target-project", f.foreign}}); e != nil {
			return e
		}
		competitor, e := f.s.DB().BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer competitor.Rollback()
		if _, e = competitor.ExecContext(ctx, `SET LOCAL lock_timeout='50ms'`); e != nil {
			return e
		}
		if _, e = competitor.ExecContext(ctx, `UPDATE agent_runs SET status='done' WHERE id=?`, run); e == nil {
			t.Fatal("run retired inside an authorized dependency transaction")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`UPDATE agent_runs SET status='done' WHERE id=?`, run); e != nil {
		t.Fatal(e)
	}
	if e = QualifiedDependency(actor, f.dir, f.foreign, "rm", f.source, f.target); !IsCrossProjectError(e) {
		t.Fatalf("retired source actor retained authority: %v", e)
	}
}

func TestPostgresQualifiedPlaceCustody(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	role := "operator-fixture-remote"
	if e := f.c.SetLabel(ctx, f.dir, f.source, "owner:"+role, true); e != nil {
		t.Fatal(e)
	}
	if e := f.s.AddPlace(ctx, store.Place{Name: "mac:fixture", Kind: "mac", PublicKey: "fixture-key", PairedAt: 1, PairedBy: "user"}); e != nil {
		t.Fatal(e)
	}
	run, e := f.s.InsertPlaceOffer(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, SpawnTime: time.Now().Unix()}, store.PlaceRun{Place: "mac:fixture", StepID: "fixture-step", ManagerRole: "manager-fixture", BriefSHA: "fixture", OfferedAt: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	actor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + role})
	if e = QualifiedDependency(actor, f.dir, f.foreign, "add", f.source, f.target); e != nil {
		t.Fatal(e)
	}
	check := func(state string, known bool) {
		t.Helper()
		e := f.s.WithTx(ctx, func(q store.Querier) error {
			v, e := loadQualifiedView(ctx, q, f.pid, false)
			if e != nil {
				return e
			}
			live, e := qualifiedCustodyLive(ctx, q, v)
			if e != nil {
				return e
			}
			got, k := live(role)
			if got != state || k != known {
				t.Fatalf("remote custody=(%q,%v), want=(%q,%v)", got, k, state, known)
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	check("", true) // offered is known, but has no live heartbeat
	if _, e = f.s.AcceptPlaceRun(ctx, run, "fixture-token", time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	if e = f.s.RecordRuntimePresence(ctx, map[int64]bool{run: false}, time.Now(), nil); e != nil {
		t.Fatal(e)
	}
	check("present", true) // local absence cannot override remote heartbeat
	if _, e = f.s.BeatPlaceRun(ctx, run, time.Now().Add(-3*time.Minute).Unix()); e != nil {
		t.Fatal(e)
	}
	check("", false)
	if _, e = f.s.EndPlaceRun(ctx, run, store.PlaceStopped, "fixture", time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	check("", true)
	if e = QualifiedDependency(actor, f.dir, f.foreign, "rm", f.source, f.target); !IsCrossProjectError(e) {
		t.Fatalf("ended remote authority: %v", e)
	}
}
