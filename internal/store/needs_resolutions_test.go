package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestResolveNeeds_AnswerAndPendingMailAreAtomic(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO projects(id, name, type, path, activated_at, status) VALUES (2, 'p', 'code', '/tmp/example', 1, 'active')`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 2, BeadID: "p-1", Ask: "Review", Decision: `{"question":"Review?"}`}); err != nil {
		t.Fatal(err)
	}
	r := NeedsResolution{ProjectID: 2, BeadID: "p-1", Generation: "1", AnswerID: "answer-one", Action: "decide", Choice: "Change", Text: "original answer", Manager: "manager-p"}
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_answer BEFORE UPDATE ON pitches BEGIN SELECT RAISE(FAIL, 'answer failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNeedsResolution(ctx, r, "Change\noriginal answer"); err == nil {
		t.Fatal("expected failed history write")
	}
	got, err := st.GetNeedsResolution(ctx, 2, "p-1")
	if err != nil || got != nil {
		t.Fatalf("partial journal committed: %+v %v", got, err)
	}
	if _, err := st.DB().Exec(`DROP TRIGGER fail_answer`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNeedsResolution(ctx, r, "Change\noriginal answer"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNeedsYou(ctx, 2, "p-1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetNeedsResolution(ctx, 2, "p-1")
	if err != nil || got == nil || got.Text != r.Text || got.Applied || got.Mailed {
		t.Fatalf("journal lost with queue: %+v %v", got, err)
	}
	if err := st.AdvanceNeedsResolution(ctx, r, ResolutionClosed); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceNeedsResolution(ctx, r, ResolutionMailed); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetNeedsResolution(ctx, 2, "p-1")
	if err != nil || !got.Applied || !got.Mailed {
		t.Fatalf("receipt not persisted: %+v %v", got, err)
	}
	// A re-flagged card starts a new resolution and resets both receipts.
	r.Choice = "Approve"
	r.Generation, r.AnswerID = "2", "answer-two"
	if err := st.SaveNeedsResolution(ctx, r, "Approve"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetNeedsResolution(ctx, 2, "p-1")
	if err != nil || got.Applied || got.Mailed || got.Choice != "Approve" {
		t.Fatalf("old receipt leaked into new answer: %+v %v", got, err)
	}
}

func TestResolveNeeds_GenerationsImmutableAndMonotonic(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO projects(id, name, type, path, activated_at, status) VALUES (2, 'p', 'code', '/tmp/example', 1, 'active')`); err != nil {
		t.Fatal(err)
	}
	r := NeedsResolution{ProjectID: 2, BeadID: "p-1", Generation: "1", AnswerID: "one", Choice: "Change"}
	if err := st.SaveNeedsResolution(ctx, r, "Change"); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceNeedsResolution(ctx, r, ResolutionClosed); err != nil {
		t.Fatal(err)
	}
	r.Choice = "Approve"
	if err := st.SaveNeedsResolution(ctx, r, "Approve"); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceNeedsResolution(ctx, r, ResolutionBeadWritten); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNeedsResolution(ctx, 2, "p-1")
	if err != nil || got.Choice != "Change" || got.State != ResolutionClosed {
		t.Fatalf("regressed entry: %+v %v", got, err)
	}
	r.Generation, r.AnswerID = "2", "two"
	if err := st.SaveNeedsResolution(ctx, r, "Approve"); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListNeedsResolutions(ctx, 2, "p-1")
	if err != nil || len(entries) != 2 || entries[0].AnswerID != "one" || entries[1].AnswerID != "two" || entries[1].State != ResolutionJournaled {
		t.Fatalf("generations merged: %+v %v", entries, err)
	}
}

func TestResolveNeeds_MigrationPreservesLegacyReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the predecessor schema in this private fixture only.
	_, err = st.DB().Exec(`DROP TABLE place_comments;
 DROP TABLE place_runs;
 DROP TABLE places;
 DROP TABLE bead_ranks;
 DROP TABLE manager_unregistrations;
 ALTER TABLE agent_runs DROP COLUMN worktree_path;
 ALTER TABLE pitches DROP COLUMN basis;
 DROP TABLE needs_resolutions;
 CREATE TABLE needs_resolutions (project_id INTEGER NOT NULL, bead_id TEXT NOT NULL, payload TEXT NOT NULL, applied INTEGER NOT NULL DEFAULT 0, mailed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(project_id, bead_id));
 INSERT INTO projects(id, name, type, path, activated_at, status) VALUES (2, 'p', 'code', '/tmp/example', 1, 'active');
 INSERT INTO needs_resolutions VALUES (2, 'p-1', '{"ProjectID":2,"BeadID":"p-1","Choice":"Approve"}', 1, 0);
 PRAGMA user_version = 16;`)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, err := st.GetNeedsResolution(context.Background(), 2, "p-1")
	if err != nil || r == nil || r.Generation != "legacy" || r.AnswerID == "" || !r.Applied || r.Mailed || r.Choice != "Approve" {
		t.Fatalf("legacy receipt lost: %+v %v", r, err)
	}
}

func TestPitch_ReflagSameAnsweredCardStartsNewGeneration(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	n := NeedsYou{ProjectID: 2, BeadID: "p-1", Ask: "Review", Decision: `{"question":"Review?"}`}
	if err := st.UpsertNeedsYou(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerPitch(ctx, 2, n.BeadID, "Change"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNeedsYou(ctx, 2, n.BeadID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNeedsYou(ctx, n); err != nil {
		t.Fatal(err)
	}
	pitches, err := st.ListPitches(ctx, 2, n.BeadID)
	if err != nil || len(pitches) != 2 || pitches[0].Answer != "Change" || pitches[1].Answer != "" {
		t.Fatalf("reused answered generation: %+v %v", pitches, err)
	}
}
