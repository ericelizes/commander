package workflowclose
import (
 "context"
 "encoding/json"
 "fmt"
 "slices"
 "strings"
 "time"
 "github.com/example-user/commander/internal/beads"
 "github.com/example-user/commander/internal/spawn"
 "github.com/example-user/commander/internal/store"
 "github.com/example-user/commander/internal/workflowtemplate"
)
func addendumComment(ctx context.Context, bc beads.Client, dir string, ref workflowtemplate.CommentRef) (beads.Comment, error) {
	cs, err := bc.Comments(ctx, dir, ref.Bead)
	if err != nil {
		return beads.Comment{}, err
	}
	var found *beads.Comment
	for _, c := range cs {
		if c.ID == ref.ID {
			if found != nil {
				return beads.Comment{}, fmt.Errorf("ambiguous comment id")
			}
			copy := c
			found = &copy
		}
	}
	if ref.ID == "" || found == nil || workflowtemplate.TextDigest(found.Text) != ref.SHA256 {
		return beads.Comment{}, fmt.Errorf("comment %s identity or digest differs", ref.ID)
	}
	return *found, nil
}

// recordedOwnerRoles are the Manager roles of every kind:workstream bead over id
// (id first), open or not, and of the root, whatever the project's switch says
// now: who may have owned id at any time.
func recordedOwnerRoles(ctx context.Context, bc beads.Client, dir, id, root string) ([]string, error) {
	var roles []string
	seen := map[string]bool{}
	for cur := id; cur != "" && cur != root; {
		if seen[cur] {
			return nil, fmt.Errorf("ancestry cycle at %s", cur)
		}
		seen[cur] = true
		b, err := bc.Show(ctx, dir, cur)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, fmt.Errorf("missing ancestor %s", cur)
		}
		if slices.Contains(b.Labels, beads.WorkstreamLabel) {
			roles = append(roles, "manager-"+cur)
		}
		ds, err := beads.FreshDetails(ctx, bc, dir, []string{cur})
		if err != nil {
			return nil, err
		}
		d, ok := ds[cur]
		if !ok {
			return nil, fmt.Errorf("unreadable ancestry %s", cur)
		}
		cur = d.Parent
	}
	return append(roles, "manager-"+root), nil
}

// ownerBeadOf is the owner bead whose Manager role is actor, "" for none.
func ownerBeadOf(actor string, owners []string) string {
	for _, r := range owners {
		if spawn.CanonicalRole(r) == spawn.CanonicalRole(actor) {
			return strings.TrimPrefix(r, "manager-")
		}
	}
	return ""
}

func(p Provenance) ValidateAddendum(ctx context.Context,bc beads.Client,dir,project string,block beads.Bead,stepID string,binding workflowtemplate.WriterBinding,r workflowtemplate.AddendumRecord,h workflowtemplate.Head,review workflowtemplate.Review)error{return p.validateAddendum(ctx,bc,dir,project,block,stepID,binding,r,h,review,false)}
// ValidateNewAddendum checks current ownership before recording; stored records
// instead carry immutable service attribution and survive subsequent handoffs.
func(p Provenance) ValidateNewAddendum(ctx context.Context,bc beads.Client,dir,project string,block beads.Bead,stepID string,binding workflowtemplate.WriterBinding,r workflowtemplate.AddendumRecord,h workflowtemplate.Head,review workflowtemplate.Review)error{return p.validateAddendum(ctx,bc,dir,project,block,stepID,binding,r,h,review,true)}
func(p Provenance) validateAddendum(ctx context.Context, bc beads.Client, dir, project string, block beads.Bead, stepID string, binding workflowtemplate.WriterBinding, r workflowtemplate.AddendumRecord, h workflowtemplate.Head, review workflowtemplate.Review,recording bool) error {
	if r.ID == "" || r.Project != project || r.Block != block.ID || r.Step != stepID || r.Binding != binding.Revision || r.Assessment.Head != h.SHA || r.Assessment.Base != h.Base || binding.Head != h.SHA || r.Assessment.Original.Bead != review.ID {
		return fmt.Errorf("addendum identity, head, base or writer revision differs")
	}
	root, err := p.checker().Root(ctx, bc, dir, block.ID)
	if err != nil {
		return err
	}
 if err=p.addendumPrincipal(ctx,bc,dir,project,block,root,r,recording);err!=nil{return err}

	if _, err = time.Parse(time.RFC3339Nano, r.RecordedAt); err != nil {
		return fmt.Errorf("invalid recording time")
	}
	original, err := addendumComment(ctx, bc, dir, r.Assessment.Original)
	if err != nil {
		return err
	}
	assessor, err := bc.Show(ctx, dir, r.AssessmentComment.Bead)
	if err != nil {
		return err
	}
	if assessor == nil || assessor.Status != "closed" || assessor.ID == review.ID {
		return fmt.Errorf("closed independent assessor required")
	}
	assessment, err := addendumComment(ctx, bc, dir, r.AssessmentComment)
	if err != nil {
		return err
	}
	if original.Author != r.OriginalAuthor || original.CreatedAt != r.OriginalCreatedAt || assessment.Author != r.AssessmentAuthor || assessment.CreatedAt != r.AssessmentCreatedAt {
		return fmt.Errorf("original or assessment attribution changed")
	}
	var a workflowtemplate.AddendumAssessment
	switch r.Representation {
	case "":
		a, err = workflowtemplate.ParseAssessment(assessment.Text)
	case workflowtemplate.ManagerTranscription:
		// Validate bounded claims without attributing their serialization to the
		// original assessor. Exact source identity/bytes and attribution were
		// checked above; actor/time belong to this current Manager record.
		if strings.Contains(assessment.Text, workflowtemplate.AssessmentPrefix) || strings.TrimSpace(assessment.Text) == "" {
			return fmt.Errorf("Manager transcription requires retained prose assessment")
		}
		raw, _ := json.Marshal(r.Assessment)
		a, err = workflowtemplate.ParseAssessment(workflowtemplate.AssessmentPrefix + string(raw))
	default:
		return fmt.Errorf("unsupported assessment representation")
	}
	if err != nil {
		return err
	}
	ab, _ := json.Marshal(a)
	rb, _ := json.Marshal(r.Assessment)
	if string(ab) != string(rb) {
		return fmt.Errorf("assessment attestation differs")
	}
	// This existing policy comment is evidence of the clause; the current door
	// records the owning Manager's withdrawal and links it to that exact comment.
	if _, err = addendumComment(ctx, bc, dir, r.PolicyComment); err != nil {
		return err
	}
	if other, err := p.checker().Root(ctx, bc, dir, r.PolicyComment.Bead); err != nil || other != root {
		return fmt.Errorf("policy comment outside workstream")
	}
	roles, err := p.checker().rolesInScope(ctx, bc, dir, project, root, review.ID)
	if err != nil {
		return err
	}
	assessRoles, err := p.checker().rolesInScope(ctx, bc, dir, project, root, assessor.ID)
	if err != nil {
		return err
	}
	if !slices.Equal(roles, r.OriginalRoles) || !slices.Equal(assessRoles, r.AssessorRoles) || roleOverlap(roles, assessRoles) {
		return fmt.Errorf("original/assessor task-run identity differs or overlaps")
	}
	for _, task := range binding.Tasks {
		writers, e := p.checker().rolesInScope(ctx, bc, dir, project, root, task)
		if e != nil {
			return e
		}
		if roleOverlap(roles, writers) || roleOverlap(assessRoles, writers) {
			return fmt.Errorf("original or assessor overlaps writer")
		}
	}
	if err = workflowtemplate.AddendumVerdict(review, h.SHA, original.Text); err != nil {
		return err
	}
	return workflowtemplate.VerifyAddendumArtifacts(a, original.Text)
}

func(src Provenance) ResolveAddendum(ctx context.Context, bc beads.Client, dir, project string, block beads.Bead, stepID string, binding workflowtemplate.WriterBinding, h workflowtemplate.Head, p *workflowtemplate.Proof, review workflowtemplate.Review) error {
	cs, err := bc.Comments(ctx, dir, block.ID)
	if err != nil {
		return err
	}
	matches := 0
	var record workflowtemplate.AddendumRecord
	for _, c := range cs {
		raw, ok := strings.CutPrefix(c.Text, workflowtemplate.AddendumPrefix)
		if !ok {
			continue
		}
		var candidate workflowtemplate.AddendumRecord
		if json.Unmarshal([]byte(raw), &candidate) != nil || candidate.ID != p.Addendum.ID {
			continue
		}
		matches++
		if err = workflowtemplate.DecodeAddendumPayload(raw, *p.Addendum, &record); err != nil {
			return err
		}
	}
	if matches != 1 {
		return fmt.Errorf("exactly one supported addendum record required")
	}
	if record.Assessment.Original.Bead != p.ReviewBead {
		return fmt.Errorf("original review attribution differs")
	}
	return src.ValidateAddendum(ctx, bc, dir, project, block, stepID, binding, record, h, review)
}


func(p Provenance) addendumPrincipal(ctx context.Context,bc beads.Client,dir,project string,block beads.Bead,root string,r workflowtemplate.AddendumRecord,recording bool)error{
 if p.Canonical{
  pid,_,e:=p.project(ctx,project);if e!=nil{return e}
  if recording{
   var owner string;var registered bool
   e=p.Q.QueryRowContext(ctx,`SELECT commander_planning_owner(?,?),EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`,pid,block.ID,r.Actor).Scan(&owner,&registered)
   if e!=nil{return e};if !registered||owner!=r.Actor{return fmt.Errorf("addendum recorder is not the current planning Commander")};return nil
  }
  stamped,e:=beads.RecordedReviewAddendum(ctx,p.Q,pid,block.ID,r.Actor,r.Comment());if e!=nil{return e};if stamped{return nil}
  // Preserve only the pre-jurisdiction Manager attribution path for legacy
  // history. A current Commander record always needs its canonical stamp.
  if !strings.HasPrefix(r.Actor,"manager-"){return fmt.Errorf("addendum has no service-recorded planning authority")}
 }
 owners,e:=recordedOwnerRoles(ctx,bc,dir,block.ID,root);if e!=nil{return e}
 owner:=ownerBeadOf(r.Actor,owners);if owner==""{return fmt.Errorf("addendum recording principal is not owning registered Manager")}
 var registered string
 e=p.Q.QueryRowContext(ctx,`SELECT m.am_role FROM workstream_managers m JOIN projects p ON p.id=m.project_id WHERE p.name=? AND m.workstream_bead_id=?`,project,owner).Scan(&registered)
 if e!=nil||registered!=store.WorkstreamManagerRole(owner)||spawn.CanonicalRole(r.Actor)!=spawn.CanonicalRole(registered){return fmt.Errorf("addendum recording principal is not owning registered Manager")}
 return nil
}
