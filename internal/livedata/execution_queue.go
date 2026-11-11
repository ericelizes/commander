package livedata

import (
 "context"
 "github.com/example-user/commander/internal/beads"

 "github.com/example-user/commander/internal/store"
)

// Runtime slot state is read in one batch independently of the cached task
// graph. A lease heartbeat never needs to invalidate the whole bead tree.
// Matching plan revisions prevent slot facts from attaching to an older plan.
func attachExecutionState(ctx context.Context,st *store.Store,rows []ObjectiveRow)error{
 if !st.IsPostgres(){return nil}
 type key struct{project int64;bead string}
 type view struct{revision,attempts int64;reserved,capable,eligible bool;detail,mode string}
 states:=map[key]view{}
 result,e:=st.DB().QueryContext(ctx,`SELECT b.project_id,b.id,b.plan_revision,b.execution_attempts,b.execution_error,c.mode,
 EXISTS(SELECT 1 FROM execution_slots x WHERE x.project_id=b.project_id AND x.bead_id=b.id AND x.run_id IS NULL AND x.lease_until>EXTRACT(EPOCH FROM clock_timestamp())::bigint),
 EXISTS(SELECT 1 FROM execution_slots x JOIN projects p ON p.id=b.project_id WHERE x.enabled AND jsonb_exists(x.capabilities,p.type) AND b.execution_capabilities <@ x.capabilities),
 commander_execution_allowed(b.project_id,b.id) AND NOT EXISTS(SELECT 1 FROM bead_tasks child WHERE child.project_id=b.project_id AND child.parent_id=b.id AND child.status NOT IN('closed','deferred') AND child.deleted_at IS NULL)
 FROM bead_tasks b JOIN projects p ON p.id=b.project_id CROSS JOIN orchestration_control c WHERE c.id=1 AND b.execution_ready AND b.deleted_at IS NULL AND p.status='active'`);if e!=nil{return e};defer result.Close()
 for result.Next(){var k key;var v view;if e=result.Scan(&k.project,&k.bead,&v.revision,&v.attempts,&v.detail,&v.mode,&v.reserved,&v.capable,&v.eligible);e!=nil{return e};states[k]=v};if e=result.Err();e!=nil{return e}
 for i:=range rows{r:=&rows[i];r.ExecutionReady=false;r.ExecutionEligible=false
  if v,ok:=states[key{r.ProjectID,r.BeadID}];ok&&v.revision==r.PlanRevision{r.ExecutionMode=v.mode;r.ExecutionReady=true;r.ExecutionEligible=v.eligible;r.ExecutionReserved=v.reserved;r.ExecutionCapable=v.capable;r.ExecutionAttempts=v.attempts;r.ExecutionError=v.detail}
 }
 questions,e:=st.DB().QueryContext(ctx,`SELECT DISTINCT project_id,bead_id FROM bead_questions WHERE blocking AND state='pending'`);if e!=nil{return e};defer questions.Close();waiting:=map[key]bool{}
 for questions.Next(){var k key;if e=questions.Scan(&k.project,&k.bead);e!=nil{return e};waiting[k]=true};if e=questions.Err();e!=nil{return e}
 for i:=range rows{rows[i].QuestionWaiting=waiting[key{rows[i].ProjectID,rows[i].BeadID}]}
 answers,e:=st.DB().QueryContext(ctx,`SELECT b.project_id,b.id FROM bead_tasks b WHERE b.deleted_at IS NULL AND commander_pending_human_answer(b.project_id,b.id)`);if e!=nil{return e};defer answers.Close();pending:=map[key]bool{}
 for answers.Next(){var k key;if e=answers.Scan(&k.project,&k.bead);e!=nil{return e};pending[k]=true};if e=answers.Err();e!=nil{return e}
 for i:=range rows{r:=&rows[i];r.HumanAnswerPending=pending[key{r.ProjectID,r.BeadID}];if r.HumanAnswerPending&&r.Status!="closed"{r.State=beads.StateReview}}
 return nil
}

func executionQueueLine(ws SnapWorkstream)(line,bool){
 candidates:=[]SnapStep{{ExecutionMode:ws.ExecutionMode,ExecutionReady:ws.ExecutionReady,ExecutionEligible:ws.ExecutionEligible,ExecutionReserved:ws.ExecutionReserved,ExecutionCapable:ws.ExecutionCapable,ExecutionAttempts:ws.ExecutionAttempts,ExecutionError:ws.ExecutionError}}
 for s:=range stepValues(ws.Steps){candidates=append(candidates,s)}
 for _,s:=range candidates{if !s.ExecutionReady||!s.ExecutionEligible{continue}
  if s.ExecutionMode=="shadow"{return line{kind:"waiting",text:"Ready; automatic execution is in shadow mode"},true}
  if s.ExecutionMode=="off"{return line{kind:"waiting",text:"Ready; automatic execution is disabled"},true}
  if s.ExecutionAttempts>=5&&s.ExecutionError!=""{return line{kind:"stalled",text:"Execution launch needs repair: "+s.ExecutionError},true}
  if !s.ExecutionCapable{return line{kind:"waiting",text:"Waiting for the required execution capability"},true}
  if s.ExecutionReserved{return line{kind:"waiting",text:"Preparing an execution"},true}
  if s.ExecutionError!=""{return line{kind:"waiting",text:"Execution launch will retry",why:s.ExecutionError},true}
  return line{kind:"waiting",text:"Queued for available execution capacity"},true
 };return line{},false
}

func planningQuestionLine(ws SnapWorkstream)(line,bool){
 if ws.HumanAnswerPending{return line{kind:"waiting",text:"Applying your recorded answer"},true}
 for s:=range stepValues(ws.Steps){if s.HumanAnswerPending&&!s.Superseded{return line{kind:"waiting",text:"Applying your recorded answer"},true}}
 if ws.QuestionWaiting{return line{kind:"waiting",text:"Waiting for the responsible Commander's decision"},true}
 for s:=range stepValues(ws.Steps){if s.QuestionWaiting&&!stepClosed(s)&&!s.Superseded{return line{kind:"waiting",text:"Waiting for the responsible Commander's decision"},true}}
 return line{},false
}
