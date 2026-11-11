package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestActiveExecutionMailRetiredManagerFollowsPlanningOwner(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	pid, e := st.InsertProject(ctx, store.Project{Name: "mail", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,created_at,updated_at) VALUES(?,'root',NULL,'Root','now','now'),(?,'leaf','root','Leaf','now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: "operator-mail-source", OpencodeSessionID: "private", SpawnTime: time.Now().Unix()}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private routing fixture"); e != nil {
		t.Fatal(e)
	}
	old := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return beads.New() }
	t.Cleanup(func() { newBeadsClientFn = old })
	got, active, e := routeActiveExecutionMail(ctx, st, "operator-mail-source", []string{"manager-root", "manager-leaf", "commander", "commander-other"})
	if e != nil || !active || !slices.Equal(got, []string{"commander", "commander-other"}) {
		t.Fatal("retired Manager routing survived cutover", got, active, e)
	}
	newBeadsClientFn = func() beads.Client { return &beads.MockClient{ShowErr: errors.New("private ancestry unavailable")} }
	if _, _, e = routeActiveExecutionMail(ctx, st, "operator-mail-source", []string{"manager-root"}); e == nil {
		t.Fatal("unreadable ancestry guessed retired recipient")
	}
}
