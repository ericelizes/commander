package place

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgbind"
	"github.com/example-user/commander/internal/store"
)

// Regression adapted from the independent .206.4.16 service reproduction.
func TestPostgresDelayedAcceptanceAuthority(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/place-offer-authority.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Changes []string `json:"changes"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Changes) != 6 {
		t.Fatal("incomplete authority scenario")
	}
	for _, stage := range []string{"accept", "authorize"} {
		for _, change := range scenario.Changes {
			t.Run(stage+"/"+change, func(t *testing.T) {
				dsn := os.Getenv("COMMANDER_POSTGRES_TEST_DSN")
				if dsn == "" {
					t.Skip("private PG required")
				}
				ctx := context.Background()
				u, e := url.Parse(dsn)
				if e != nil || u.Path != "/commander_trial" {
					t.Fatal("private commander_trial database required", e)
				}
				admin, e := sql.Open(pgbind.Driver, dsn)
				if e != nil {
					t.Fatal(e)
				}
				defer admin.Close()
				schema := fmt.Sprintf("review_accept_%d", time.Now().UnixNano())
				if _, e = admin.Exec(`CREATE SCHEMA ` + schema); e != nil {
					t.Fatal(e)
				}
				defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
				q := u.Query()
				q.Set("search_path", schema)
				u.RawQuery = q.Encode()
				st, e := store.OpenPostgres(u.String())
				if e != nil {
					t.Fatal(e)
				}
				defer st.Close()
				pid, e := st.InsertProject(ctx, store.Project{Name: "review", Type: "code", Path: t.TempDir(), Status: "active"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,created_at,updated_at,labels) VALUES(?,'review-task','Task',?,'now','now','[]'),(?,'review-step','Step','','now','now','["run-on:mac:review"]')`, pid, brief, pid); e != nil {
					t.Fatal(e)
				}
				if e = st.AddPlace(ctx, store.Place{Name: "mac:review", Kind: "mac", PublicKey: "key", PairedBy: "user", PairedAt: 1}); e != nil {
					t.Fatal(e)
				}
				id, e := RecordOffer(ctx, st, OfferRequest{ProjectID: pid, TaskID: "review-task", AgentName: "operator-review", Place: "mac:review", StepID: "review-step", ManagerRole: "commander", Brief: brief})
				if e != nil {
					t.Fatal(e)
				}

				rec := NewRecorder()
				rec.Beads["review-task"] = Bead{Title: "Task", Description: brief, Status: "open"}
				rec.Beads["review-step"] = Bead{Title: "Step", Labels: []string{"run-on:mac:review"}}
				svc := NewService(rec.Wire(Deps{Store: st, Revision: "review"}))
				var token string
				if stage == "authorize" {
					token, e = svc.Accept(ctx, "mac:review", id, BriefSHA(brief), "")
					if e != nil || token == "" {
						t.Fatal("unchanged offer refused", e)
					}
					if _, e = svc.Authorize(ctx, id, token); e != nil {
						t.Fatal("valid token refused", e)
					}
				}
				switch change {
				case "hold":
					_, e = st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='review-task'`, pid)
				case "closed":
					_, e = st.DB().Exec(`UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id='review-task'`, pid)
				case "exited":
					_, e = st.DB().Exec(`UPDATE agent_runs SET status='exited' WHERE id=?`, id)
				case "reclaimed":
					_, e = st.DB().Exec(`UPDATE bead_tasks SET active_run_id=NULL WHERE project_id=? AND id='review-task'`, pid)
				default:
					_, e = st.DB().Exec(`UPDATE orchestration_control SET mode=?`, change)
				}
				if e != nil {
					t.Fatal(e)
				}
				if stage == "authorize" {
					if _, e = svc.Authorize(ctx, id, token); !errors.Is(e, ErrUnauthorized) {
						t.Fatalf("revoked token authorized: %v", e)
					}
					return
				}
				tok, e := svc.Accept(ctx, "mac:review", id, BriefSHA(brief), "")
				if e == nil || tok != "" {
					t.Fatalf("revoked offer accepted: token=%q err=%v", tok, e)
				}
				pr, e := st.GetPlaceRun(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				if pr.TokenHash != "" || pr.State != store.PlaceOffered {
					t.Fatalf("revoked offer mutated: %+v", pr)
				}
			})
		}
	}
}
