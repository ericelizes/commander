package workflowclose

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "fmt"
 "path/filepath"
 "slices"
 "strings"

 "github.com/example-user/commander/internal/beads"
 "github.com/example-user/commander/internal/reviewrun"
 "github.com/example-user/commander/internal/spawn"
 "github.com/example-user/commander/internal/store"
 "github.com/example-user/commander/internal/workflowtemplate"
)

// Provenance reads the same transaction as the guarded graph mutation. It never
// infers an executor's identity from the mutable bead assignee or a role prefix.
type Provenance struct { Canonical bool; Q store.Querier; Home string; Checks *Verifier }
func(p Provenance) project(ctx context.Context,name string)(int64,string,error){var id int64;var path string;e:=p.Q.QueryRowContext(ctx,`SELECT id,COALESCE(NULLIF(repo_path,''),path) FROM projects WHERE name=?`,name).Scan(&id,&path);return id,path,e}
func(p Provenance) runs(ctx context.Context,project,task string)([]store.AgentRun,error){pid,_,e:=p.project(ctx,project);if e!=nil{return nil,e};query:=`SELECT id,project_id,task_bead_id,agent_name FROM agent_runs WHERE project_id=?`;args:=[]any{pid};if task!=""{query=`SELECT id,project_id,task_bead_id,agent_name FROM agent_runs WHERE task_bead_id=?`;args=[]any{task}};query+=` ORDER BY spawn_time DESC`;rows,e:=p.Q.QueryContext(ctx,query,args...);if e!=nil{return nil,e};defer rows.Close();var out []store.AgentRun;for rows.Next(){var a store.AgentRun;if e=rows.Scan(&a.ID,&a.ProjectID,&a.TaskBeadID,&a.AgentName);e!=nil{return nil,e};if a.ID<=0||a.ProjectID!=pid||strings.TrimSpace(a.AgentName)==""||(task!=""&&a.TaskBeadID!=task){return nil,fmt.Errorf("ambiguous task-run provenance for %s",task)};out=append(out,a)};return out,rows.Err()}
// Actor preserves the historical op-/operator- alias while supporting immutable
// generic executor identities. A bare or missing legacy prefix is ambiguous.
func Actor(name string)(string,error){name=strings.ToLower(strings.TrimSpace(name));if name==""||name=="op-"||name=="operator-"||strings.HasPrefix(name,"manager-"){return "",errors.New("invalid execution run actor")};if strings.HasPrefix(name,"op-"){name="operator-"+name};return spawn.CanonicalRole(name),nil}
func(p Provenance) TaskRoles(ctx context.Context,project,task string)([]string,error){runs,e:=p.runs(ctx,project,task);if e!=nil{return nil,e};var roles []string;for _,run:=range runs{role,e:=Actor(run.AgentName);if e!=nil{return nil,e};if !slices.Contains(roles,role){roles=append(roles,role)}};if len(roles)==0{return nil,fmt.Errorf("task %s matches no execution run",task)};return roles,nil}
func(p Provenance) ResultRuns(ctx context.Context,project,task string)(map[int64]string,error){runs,e:=p.runs(ctx,project,task);if e!=nil{return nil,e};out:=map[int64]string{};for _,run:=range runs{actor,e:=Actor(run.AgentName);if e!=nil{return nil,e};out[run.ID]=actor};return out,nil}
func(p Provenance) OperatorCommits(ctx context.Context,project,task string,shas []string)(map[string]string,error){_,repo,e:=p.project(ctx,project);if e!=nil{return nil,e};runs,e:=p.runs(ctx,project,task);if e!=nil{return nil,e};return OperatorAuthorship(ctx,repo,runs,task=="",shas)}
func(p Provenance) LoadResults(ctx context.Context,project,task string,review *workflowtemplate.Review)error{
 for _,comment:=range review.Comments{if _,present,_:=workflowtemplate.ParseReviewResult(comment);present{runs,e:=p.ResultRuns(ctx,project,task);if e!=nil{return e};review.Runs=runs;break}}
 review.Recorded=map[string]reviewrun.Recorded{}
 for _,comment:=range review.Comments{result,present,e:=workflowtemplate.ParseReviewResult(comment);if !present||e!=nil{continue};original,e:=reviewrun.ReadRecorded(filepath.Join(p.Home,"review-executions"),result.RunID,result.ManifestHash);if e==nil{review.Recorded[result.ManifestHash]=original}}
 return nil
}
// New builds the production verifier. The caller supplies transaction-bound
// bead reads/writes and the query handle for runtime provenance.
func New(q store.Querier,home string)Verifier{p:=Provenance{Canonical:true,Q:q,Home:home};return Verifier{RecheckRetired:true,TaskRoles:p.TaskRoles,OperatorCommits:p.OperatorCommits,LoadResults:p.LoadResults,FileFollowUps:p.FileFollowUps,ResolveAddendum:p.ResolveAddendum}}

// FileFollowUps records ordinary planning work outside the review block, using
// the same transaction and authority checks as the gate completion itself.
func(p Provenance) FileFollowUps(ctx context.Context,bc beads.Client,dir string,block beads.Bead,instance,step,review,head string,findings []workflowtemplate.ReviewFinding)([]string,error){
 owner:=beads.Owner(ctx,bc,dir,nil,nil,block.ID);if owner==""{return nil,fmt.Errorf("cannot find the workstream that owns block %s",block.ID)}
 var ids []string
 for _,f:=range findings{if f.Severity!="minor"{return nil,fmt.Errorf("finding %s is %s: only minor findings become follow-ups",f.ID,f.Severity)}
 label:="workflow-followup:"+followUpKey(instance,step,review,head,f.ID)
 found,e:=bc.List(ctx,dir,beads.ListOptions{Status:"all",Labels:[]string{label}});if e!=nil{return nil,e};if len(found)>1{return nil,fmt.Errorf("multiple follow-up beads carry %s",label)};if len(found)==1{ids=append(ids,found[0].ID);continue}
 title:="Review follow-up: "+oneLine(f.Summary,80);desc:=fmt.Sprintf("Minor finding %s left unfixed by review %s on head %s (block %s, step %s). Filed under %s; the responsible Commander plans this work.\n\n%s",f.ID,review,head,block.ID,step,workflowtemplate.MinorPolicy,f.Summary)
 id,e:=bc.Create(ctx,dir,title,beads.CreateOptions{Parent:owner,Type:"task",Labels:[]string{"kind:objective",label,"workflow-followup-of:"+beads.EscapeLabel(block.ID)},Description:desc,Silent:true});if e!=nil{return nil,e};if e=bc.Link(ctx,dir,id,block.ID,"discovered-from");e!=nil{return nil,e};ids=append(ids,id)
 };return ids,nil
}
func oneLine(s string,n int)string{s=strings.Join(strings.Fields(s)," ");if r:=[]rune(s);len(r)>n{return string(r[:n-1])+"…"};return s}

func followUpKey(instance,step,review,head,finding string)string{sum:=sha256.Sum256([]byte(strings.Join([]string{instance,step,review,head,finding},"\x00")));return hex.EncodeToString(sum[:8])}

func(p Provenance) checker()Verifier{if p.Checks!=nil{return *p.Checks};return New(p.Q,p.Home)}
