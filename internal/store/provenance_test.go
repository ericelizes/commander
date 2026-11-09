package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProvenanceSchema20ReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE projects(id INTEGER PRIMARY KEY,name TEXT,type TEXT,path TEXT,activated_at INTEGER,status TEXT,repo_path TEXT)`,
		`CREATE TABLE pitches(project_id INTEGER,bead_id TEXT,version INTEGER,decision TEXT,answer TEXT,answered_at INTEGER)`,
		`CREATE TABLE needs_resolutions(project_id INTEGER,bead_id TEXT,generation TEXT,payload TEXT,state INTEGER)`,
		`PRAGMA user_version=20`,
		`INSERT INTO projects VALUES(1,'sample','code','/fixture',0,'active','')`,
		`INSERT INTO pitches VALUES(1,'demo-gate',2,'{}','Approved',1)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r := NeedsResolution{ProjectID: 1, BeadID: "demo-gate", Generation: "2", AnswerID: "fixture-answer", Action: "approve"}
	payload, _ := json.Marshal(r)
	if _, err = db.Exec(`INSERT INTO needs_resolutions VALUES(1,'demo-gate','2',?,3)`, string(payload)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := st.GetProjectByName(ctx, "sample")
	if err != nil || p == nil {
		t.Fatalf("%+v %v", p, err)
	}
	pitch, receipt, err := st.ApprovedContextEvidence(ctx, p.ID, "demo-gate")
	if err != nil || pitch.Version != 2 || receipt.State != ResolutionMailed {
		t.Fatalf("%+v %+v %v", pitch, receipt, err)
	}
	if _, err = st.DB().Exec(`UPDATE pitches SET answer='Changed'`); err == nil {
		t.Fatal("reader allowed write")
	}
	st.Close()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("state bytes changed")
	}
	if _, err = os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("reader created WAL: %v", err)
	}
}

func TestProvenanceDoesNotCreateMissingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if st, err := OpenProvenance(path); err == nil {
		st.Close()
		t.Fatal("opened missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created missing database")
	}
}

func TestProvenanceRefusesNewerUnansweredPitch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.InsertProject(ctx, Project{Name: "sample", Type: "code", Path: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordPitch(ctx, id, "demo", "old", `{"question":"Old?","input":true}`, nil); err != nil {
		t.Fatal(err)
	}
	r := NeedsResolution{ProjectID: id, BeadID: "demo", Generation: "1", AnswerID: "fixture", Action: "approve"}
	if err = st.SaveNeedsResolution(ctx, r, "Approved"); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordPitch(ctx, id, "demo", "new", `{"question":"New?","input":true}`, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ApprovedContextEvidence(ctx, id, "demo"); err == nil {
		t.Fatal("accepted older approval")
	}
}
