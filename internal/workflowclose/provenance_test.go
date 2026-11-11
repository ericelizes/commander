package workflowclose

import (
 "context"
 "errors"
 "os"
 "path/filepath"
 "github.com/example-user/commander/internal/beads"
 "github.com/example-user/commander/internal/workflowtemplate"
 "reflect"
 "testing"
 "github.com/example-user/commander/internal/pgtest"
 "github.com/example-user/commander/internal/store"
)
func TestProvenancePreservesHistoricalRoleOrderAndGenericAttribution(t *testing.T){
 st,_:=pgtest.Open(t);ctx:=context.Background();pid,e:=st.InsertProject(ctx,store.Project{Name:"provenance",Path:t.TempDir(),Type:"knowledge",Status:"active"});if e!=nil{t.Fatal(e)}
 // IDs and chronological order deliberately differ. Existing addendum records
 // retain the historical newest-run-first role list from the typed CLI.
 for _,r:=range []struct{id,time int64;actor string}{{100,20,"executor-generic"},{101,10,"operator-op-legacy"}}{if _,e=st.DB().ExecContext(ctx,`INSERT INTO agent_runs(id,project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(?,?,'review',?,'session',?,'closed')`,r.id,pid,r.actor,r.time);e!=nil{t.Fatal(e)}}
 p:=Provenance{Q:st.DB(),Home:t.TempDir()};roles,e:=p.TaskRoles(ctx,"provenance","review");if e!=nil||!reflect.DeepEqual(roles,[]string{"executor-generic","operator-op-legacy"}){t.Fatalf("historical attribution order changed: %v %v",roles,e)}
 runs,e:=p.ResultRuns(ctx,"provenance","review");if e!=nil||runs[100]!="executor-generic"||runs[101]!="operator-op-legacy"{t.Fatal(runs,e)}
 other,e:=st.InsertProject(ctx,store.Project{Name:"other",Path:t.TempDir(),Type:"knowledge",Status:"active"});if e!=nil{t.Fatal(e)}
 if _,e=st.DB().ExecContext(ctx,`INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(?,'review','executor-foreign','foreign',30,'closed')`,other);e!=nil{t.Fatal(e)}
 if _,e=p.TaskRoles(ctx,"provenance","review");e==nil{t.Fatal("foreign-project run was accepted as review provenance")}
}

func TestProvenanceMinorFollowUpsShareCompletionTransaction(t *testing.T){
 st,dsn:=pgtest.Open(t);ctx:=context.Background();home:=t.TempDir();t.Setenv("COMMANDER_HOME",home)
 if e:=os.WriteFile(filepath.Join(home,"state.postgres"),[]byte(dsn),0600);e!=nil{t.Fatal(e)}
 dir:=t.TempDir();pid,e:=st.InsertProject(ctx,store.Project{Name:"minor-follow-ups",Path:dir,Type:"knowledge",Status:"active"});if e!=nil{t.Fatal(e)}
 if _,e=st.DB().ExecContext(ctx,`INSERT INTO bead_tasks(project_id,id,parent_id,title,labels,created_at,updated_at) VALUES(?,'root',NULL,'Goal','["kind:workstream"]','now','now'),(?,'block','root','Review block','["kind:objective"]','now','now'),(?,'trigger','root','Review completion','["kind:objective"]','now','now')`,pid,pid,pid);e!=nil{t.Fatal(e)}
 if _,e=st.SetOrchestrationControl(ctx,1,"active","minor follow-up transaction test");e!=nil{t.Fatal(e)}
 rollback:=true;var first string
 checkCtx:=beads.WithCompletionValidator(beads.WithDoorEnv(ctx,[]string{"COMMANDER_ROLE=commander"}),func(txCtx context.Context,q store.Querier,projectID int64,id string)error{
  p:=Provenance{Q:q,Home:home};client:=beads.CompletionClient(q,projectID)
  findings:=[]workflowtemplate.ReviewFinding{{ID:"minor-1",Severity:"minor",Summary:"Improve the retained diagnostic wording"}}
  ids,e:=p.FileFollowUps(txCtx,client,dir,beads.Bead{ID:"block"},"instance","review","review-task","head",findings);if e!=nil{return e};if len(ids)!=1{t.Fatalf("follow-up ids=%v",ids)};first=ids[0]
  retry,e:=p.FileFollowUps(txCtx,client,dir,beads.Bead{ID:"block"},"instance","review","review-task","head",findings);if e!=nil{return e};if !reflect.DeepEqual(ids,retry){t.Fatalf("same transaction duplicated a finding: %v %v",ids,retry)}
  if rollback{return errors.New("later completion check refused")};return nil
 })
 if _,_,e=beads.RunBr(checkCtx,dir,"close","trigger");e==nil{t.Fatal("completion unexpectedly committed")}
 var n int;if e=st.DB().QueryRowContext(ctx,`SELECT COUNT(*) FROM bead_tasks WHERE project_id=? AND id=?`,pid,first).Scan(&n);e!=nil||n!=0{t.Fatal("failed completion leaked follow-up",n,e)}
 rollback=false;if _,_,e=beads.RunBr(checkCtx,dir,"close","trigger");e!=nil{t.Fatal("transaction-bound follow-up could not commit",e)}
 if e=st.DB().QueryRowContext(ctx,`SELECT COUNT(*) FROM bead_tasks WHERE project_id=? AND jsonb_exists(labels,'workflow-followup-of:block')`,pid).Scan(&n);e!=nil||n!=1{t.Fatal("minor finding was lost or duplicated",n,e)}
}
