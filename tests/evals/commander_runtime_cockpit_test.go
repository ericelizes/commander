package evals

import (
 "context"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "testing"
 "time"
 "github.com/example-user/commander/internal/pgtest"
 "github.com/example-user/commander/internal/store"
)

// Real detached cockpit, private PostgreSQL and fake model binaries. This
// exercises the actual startup/text interface without touching the user's
// monitor, tmux server, state, accounts or model subscriptions.
func TestCommanderRuntimeContractInIsolatedPostgresCockpit(t *testing.T){
 r:=newBareCmdrRig(t);st,dsn:=pgtest.Open(t);ctx:=context.Background()
 iso,socket:=filepath.Join(r.dir,"pg-home"),filepath.Join(r.dir,"pg-socket")
 ch:=filepath.Join(iso,"ch");project:=filepath.Join(r.dir,"project")
 for _,dir:=range []string{ch,filepath.Join(project,".beads")}{if e:=os.MkdirAll(dir,0700);e!=nil{t.Fatal(e)}}
 for path,value:=range map[string]string{filepath.Join(iso,".cockpit-isolated"):"private test fixture",filepath.Join(ch,"state.postgres"):dsn}{if e:=os.WriteFile(path,[]byte(value),0600);e!=nil{t.Fatal(e)}}
 pid,e:=st.InsertProject(ctx,store.Project{Name:"runtime-cockpit",Path:project,Type:"knowledge",Status:"active"});if e!=nil{t.Fatal(e)}
 if _,e=st.DB().ExecContext(ctx,`INSERT INTO bead_tasks(project_id,id,parent_id,title,description,acceptance_criteria,labels,execution_ready,created_at,updated_at) VALUES(?,'root',NULL,'Runtime contract check','','','["kind:objective","kind:workstream"]',FALSE,'now','now'),(?,'task','root','Bounded execution','Use the assigned scope','Record evidence','["kind:objective"]',TRUE,'now','now')`,pid,pid);e!=nil{t.Fatal(e)}
 if _,e=st.SetOrchestrationControl(ctx,1,"active","isolated runtime cockpit");e!=nil{t.Fatal(e)}
 script:=func(verb string)(string,error){
  deadline,cancel:=context.WithTimeout(context.Background(),evalTimeout(t,60*time.Second));defer cancel()
  command:=exec.CommandContext(deadline,"bash",filepath.Join(repoRoot(),"scripts","cockpit-test.sh"),verb)
  command.Env=[]string{"HOME="+filepath.Join(r.dir,"home"),"PATH=/usr/bin:/bin","COCKPIT_HOME="+iso,"COCKPIT_TMUX_TMPDIR="+socket,"COCKPIT_CMDR="+filepath.Join(r.bin,"cmdr"),"COCKPIT_FAKE_OPENCODE="+filepath.Join(r.bin,"opencode")}
  command.WaitDelay=3*time.Second;out,e:=command.CombinedOutput();return string(out),e
 }
 t.Cleanup(func(){if out,e:=script("down");e!=nil{t.Logf("isolated teardown: %v %s",e,out)}})
 if out,e:=script("up");e!=nil{t.Fatalf("isolated startup: %v\n%s",e,out)}
 var text string
 deadline:=time.Now().Add(evalTimeout(t,20*time.Second))
 for time.Now().Before(deadline){text,e=script("text");if e==nil&&strings.Contains(text,"Runtime contract check"){break};time.Sleep(100*time.Millisecond)}
 if e!=nil||!strings.Contains(text,"Runtime contract check"){t.Fatalf("canonical workstream did not render: %v\n%s",e,text)}
 if strings.Contains(text,"real claude is disabled")||strings.Contains(text,"real codex is disabled"){t.Fatalf("cockpit attempted a real subscription runtime:\n%s",text)}
 prompt,e:=os.ReadFile(filepath.Join(iso,"home",".config","opencode","agents","commander.md"));if e!=nil{t.Fatal(e)}
 if !strings.Contains(string(prompt),"cmdr plan apply")||strings.Contains(string(prompt),"bias toward delegation"){t.Fatal("active cockpit installed the legacy delegation prompt")}
 command:=exec.Command(r.tmux,"-S",filepath.Join(socket,"cockpit.sock"),"list-windows","-t","commander","-F","#{window_name}")
 windows,e:=command.Output();if e!=nil||strings.TrimSpace(string(windows))!="commander"{t.Fatalf("active startup created legacy Manager windows: %q %v",windows,e)}
 t.Logf("Verified active PostgreSQL cockpit via scripts/cockpit-test.sh text:\n%s",text)
}
