package wsmgr

import (
 "context"
 "testing"

 "github.com/example-user/commander/internal/beads"
 "github.com/example-user/commander/internal/pgtest"
 "github.com/example-user/commander/internal/store"
)

func TestPostgresActiveWorkstreamsDoNotCreatePermanentManagers(t *testing.T){
 st,_:=pgtest.Open(t);ctx:=context.Background();pid,e:=st.InsertProject(ctx,store.Project{Name:"shared",Path:t.TempDir(),Type:"knowledge",Status:"active"});if e!=nil{t.Fatal(e)}
 if _,e=st.DB().ExecContext(ctx,`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'root','Goal','["kind:objective"]','now','now')`,pid);e!=nil{t.Fatal(e)}
 if _,e=st.SetOrchestrationControl(ctx,1,"active","fixture");e!=nil{t.Fatal(e)}
 native:=&fakeNative{started:map[string]bool{}}
 m:=&Manager{Store:st,Native:native,Beads:&beads.MockClient{}}
 if e=m.EnsureNative(ctx);e!=nil{t.Fatal(e)}
 if len(native.started)!=0{t.Fatalf("background ensure started fixed Manager roles: %v",native.started)}
 windows,e:=m.PrepareAll(ctx);if e!=nil||len(windows)!=0{t.Fatalf("layout recreated fixed Manager windows: %+v %v",windows,e)}
 result,e:=m.Open(ctx,"shared","root",OpenOptions{});if e!=nil||result.Role!="commander"||!result.Native{t.Fatalf("open did not use planning owner: %+v %v",result,e)}
 if !native.started["commander"]||native.started["manager-root"]{t.Fatalf("wrong conversation started: %v",native.started)}
 if _,e=st.DB().ExecContext(ctx,`INSERT INTO bead_tasks(project_id,id,title,parent_id,created_at,updated_at) VALUES(?,'independent-nested','Nested','root','now','now')`,pid);e!=nil{t.Fatal(e)}
 if e=st.RegisterCommander(ctx,"commander-urgent","Urgent");e!=nil{t.Fatal(e)}
 if _,e=st.TransferJurisdiction(ctx,store.JurisdictionTransfer{ProjectID:pid,BeadID:"independent-nested",ExpectedOwner:"commander",ExpectedRevision:1,NextCommander:"commander-urgent",Reason:"nested ownership"});e!=nil{t.Fatal(e)}
 result,e=m.Open(ctx,"shared","root",OpenOptions{StepID:"independent-nested"});if e!=nil||result.Role!="commander-urgent"||!native.started["commander-urgent"]{t.Fatalf("nested focus reached wrong owner: %+v %v",result,e)}
 if _,e=m.Open(ctx,"shared","independent-nested",OpenOptions{StepID:"root"});e==nil{t.Fatal("focus outside requested scope was accepted")}
 var managers int;if e=st.DB().QueryRowContext(ctx,`SELECT COUNT(*) FROM workstream_managers`).Scan(&managers);e!=nil||managers!=0{t.Fatalf("opening goal registered a Manager: %d %v",managers,e)}
}
