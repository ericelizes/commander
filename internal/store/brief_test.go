package store

import (
	"context"
	"testing"
	"time"
)

func TestBriefBeadsReadsChangesReportsAndTheTopWorkstream(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "brief", Type: "code", Path: "/brief", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	old, recent := "2026-11-01T10:00:00Z", "2026-11-19T15:30:00.123456Z"
	for _, row := range [][]any{
		{"ws", "", "Top workstream", "open", `["kind:workstream"]`, "", old},
		{"ws.1", "ws", "Nested workstream", "open", `["kind:workstream"]`, "", old},
		{"ws.1.2", "ws.1", "Ship the fix", "closed", `["kind:objective","ticket:ABC-1"]`, "Deployed to prod", recent},
		{"ws.1.3", "ws.1", "Old step", "closed", `[]`, "", old},
		{"ws.1.4", "ws.1", "Agent task", "closed", `["kind:task"]`, "", old},
		{"loose", "", "No workstream", "closed", `[]`, "", recent},
	} {
		if _, err := s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,status,labels,close_reason,created_at,updated_at) VALUES(?,?,NULLIF(?,''),?,?,?::jsonb,?,?,?)`,
			pid, row[0], row[1], row[2], row[3], row[4], row[5], old, row[6]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,?,?,?,?),(?,?,?,?,?,?)`,
		pid, "ws.1.4", "c1", "operator-x", "Final report: PASS, the fix is live.", recent,
		pid, "ws.1.4", "c2", "operator-x", "just a note", recent); err != nil {
		t.Fatal(err)
	}
	beads, reports, err := s.BriefBeads(ctx, time.Date(2026, 11, 16, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]BriefBead{}
	for _, b := range beads {
		got[b.ID] = b
	}
	if len(got) != 3 {
		t.Fatalf("want the changed step, the loose bead and the reported task, got %+v", beads)
	}
	fix := got["ws.1.2"]
	if fix.Workstream != "ws" || fix.WorkstreamTitle != "Top workstream" || fix.Parent != "Nested workstream" || fix.Project != "brief" || fix.CloseReason != "Deployed to prod" {
		t.Fatalf("%+v", fix)
	}
	if len(fix.Labels) != 2 || fix.Labels[1] != "ticket:ABC-1" || !fix.UpdatedAt.Equal(time.Date(2026, 11, 19, 15, 30, 0, 123456000, time.UTC)) {
		t.Fatalf("%+v", fix)
	}
	if got["loose"].Workstream != "" || got["ws.1.4"].Workstream != "ws" {
		t.Fatalf("%+v", beads)
	}
	if len(reports) != 1 || reports[0].BeadID != "ws.1.4" || reports[0].Author != "operator-x" {
		t.Fatalf("%+v", reports)
	}
}
