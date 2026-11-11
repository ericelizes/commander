package livedata

import (
 "slices"
 "github.com/example-user/commander/internal/beads"
)

// dependencyBead preserves the distinction between parked work and an
// unpassed requirement. UI phase text is not completion evidence.
func dependencyBead(s SnapStep) beads.Bead {
 b:=beads.Bead{ID:s.BeadID,Status:s.Status,HumanAnswerPending:s.HumanAnswerPending}
 if s.WorkflowCheck!=nil { b.Labels=slices.Clone(s.WorkflowCheck.Bead.Labels) }
 if s.Gate { b.Labels=append(b.Labels,"kind:gate") }
 if s.Superseded { b.Labels=append(b.Labels,"superseded") }
 return b
}

func dependencyComplete(s SnapStep) bool {
 return beads.DependencyComplete(dependencyBead(s))
}

// requiredCheckLine follows real dependency edges, including across sidebar
// rows. Parking a check does not pass it or create another human decision.
// Only active work is a starting point: an intentionally parked whole scope
// remains parked and is never implicitly resumed by this derivation.
func (b board) requiredCheckLine(ws SnapWorkstream) (line,bool) {
 byID:=b.projectSteps[ws.Project]
 if ws.Status==beads.StatusDeferred||ws.Status=="closed"||ws.Hold{return line{},false}
 seen:=map[string]bool{}
 var find func([]string)(SnapStep,bool)
 find=func(ids []string)(SnapStep,bool){
  for _,id:=range ids {
   if seen[id] {continue};seen[id]=true
   s,ok:=byID[id];if !ok||dependencyComplete(s){continue}
   if parked(s)||s.Superseded {return s,true}
   if hit,ok:=find(s.BlockedBy);ok{return hit,true}
  }
  return SnapStep{},false
 }
 roots:=[]SnapStep{{Status:ws.Status,BlockedBy:ws.BlockedBy}}
 var collect func([]SnapStep)
 collect=func(steps []SnapStep){for _,s:=range steps{if stepClosed(s)||parked(s)||s.Superseded{continue};roots=append(roots,s);collect(s.Children)}}
 collect(ws.Steps)
 for _,s:=range roots {
  if stepClosed(s)||parked(s)||s.Superseded{continue}
  if hit,ok:=find(s.BlockedBy);ok {
   text:="Waiting on deferred required check: "+liveTitle(hit.Title)
   if hit.Superseded{text="Required check needs reconciliation: "+liveTitle(hit.Title)}
   return line{kind:"waiting",text:text},true
  }
 }
 return line{},false
}
