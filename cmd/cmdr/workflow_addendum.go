package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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

func authorizeAddendum(ctx context.Context, project string, owners []string) (string, error) {
	actor := spawn.CanonicalRole(os.Getenv("COMMANDER_ROLE"))
	owner := ownerBeadOf(actor, owners)
	if callerIsOperator("") || owner == "" {
		return "", fmt.Errorf("only the registered owning Manager may record a review addendum")
	}
	st, done, err := needsYouStoreFn()
	if err != nil {
		return "", err
	}
	defer done()
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", fmt.Errorf("unknown project")
	}
	row, err := st.GetWorkstreamManager(ctx, p.ID, owner)
	if err != nil {
		return "", err
	}
	if row == nil || spawn.CanonicalRole(row.AmRole) != actor || row.AmRole != store.WorkstreamManagerRole(owner) {
		return "", fmt.Errorf("owning Manager registration unavailable")
	}
	return actor, nil
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
		ds, err := workflowFreshDetails(ctx, bc, dir, []string{cur})
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

func validateWorkflowAddendum(ctx context.Context, bc beads.Client, dir, project string, block beads.Bead, stepID string, binding workflowtemplate.WriterBinding, r workflowtemplate.AddendumRecord, h workflowtemplate.Head, review workflowtemplate.Review) error {
	if r.ID == "" || r.Project != project || r.Block != block.ID || r.Step != stepID || r.Binding != binding.Revision || r.Assessment.Head != h.SHA || r.Assessment.Base != h.Base || binding.Head != h.SHA || r.Assessment.Original.Bead != review.ID {
		return fmt.Errorf("addendum identity, head, base or writer revision differs")
	}
	root, err := workflowRoot(ctx, bc, dir, block.ID)
	if err != nil {
		return err
	}
	// A recorded addendum is judged against every workstream that could have
	// owned the block when it was recorded, not today's owner chain alone: a
	// later --undo, or its nested workstream closing, does not void it. The
	// registration check below still binds the actor to that workstream.
	owners, err := recordedOwnerRoles(ctx, bc, dir, block.ID, root)
	if err != nil {
		return err
	}
	owner := ownerBeadOf(r.Actor, owners)
	if owner == "" {
		return fmt.Errorf("addendum recording principal is not owning registered Manager")
	}
	// Recording attribution is checked against the current registration, within
	// Commander's documented local advisory role/hook trust boundary.
	st, done, err := needsYouStoreFn()
	if err != nil {
		return err
	}
	defer done()
	proj, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return err
	}
	if proj == nil {
		return fmt.Errorf("unknown project")
	}
	mgr, err := st.GetWorkstreamManager(ctx, proj.ID, owner)
	if err != nil {
		return err
	}
	if mgr == nil || mgr.AmRole != store.WorkstreamManagerRole(owner) || spawn.CanonicalRole(r.Actor) != spawn.CanonicalRole(mgr.AmRole) {
		return fmt.Errorf("addendum recording principal is not owning registered Manager")
	}
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
	if other, err := workflowRoot(ctx, bc, dir, r.PolicyComment.Bead); err != nil || other != root {
		return fmt.Errorf("policy comment outside workstream")
	}
	roles, err := workflowRolesInScope(ctx, bc, dir, project, root, review.ID)
	if err != nil {
		return err
	}
	assessRoles, err := workflowRolesInScope(ctx, bc, dir, project, root, assessor.ID)
	if err != nil {
		return err
	}
	if !slices.Equal(roles, r.OriginalRoles) || !slices.Equal(assessRoles, r.AssessorRoles) || roleOverlap(roles, assessRoles) {
		return fmt.Errorf("original/assessor task-run identity differs or overlaps")
	}
	for _, task := range binding.Tasks {
		writers, e := workflowRolesInScope(ctx, bc, dir, project, root, task)
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

func resolveWorkflowAddendum(ctx context.Context, bc beads.Client, dir, project string, block beads.Bead, stepID string, binding workflowtemplate.WriterBinding, h workflowtemplate.Head, p *workflowtemplate.Proof, review workflowtemplate.Review) error {
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
	return validateWorkflowAddendum(ctx, bc, dir, project, block, stepID, binding, record, h, review)
}

func runWorkflowAddendum(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow add-review-addendum", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project")
	blockID := fs.String("block", "", "block")
	stepID := fs.String("step", "", "review step bead")
	originalID := fs.String("original-comment", "", "retained original comment")
	assessorID := fs.String("assessment-bead", "", "independent assessor")
	assessmentID := fs.String("assessment-comment", "", "assessment report")
	policyBead := fs.String("policy-bead", "", "existing policy bead")
	evidenceFile := fs.String("evidence-file", "", "current Manager transcription JSON (source and claims)")
	policyID := fs.String("policy-comment", "", "existing withdrawn policy comment")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	fail := func(err error) int { emitErr(stderr, err.Error()); return 1 }
	if *project == "" || *blockID == "" || *stepID == "" || *originalID == "" || *assessorID == "" || *assessmentID == "" || *policyBead == "" || *policyID == "" || fs.NArg() != 0 {
		return fail(fmt.Errorf("required: --project --block --step --original-comment --assessment-bead --assessment-comment --policy-bead --policy-comment"))
	}
	if callerIsOperator("") {
		return fail(fmt.Errorf("Operators cannot record review addenda"))
	}
	proj, err := resolveProjectRecFn(*project)
	if err != nil {
		return fail(err)
	}
	unlock := lockPlug("addendum-" + *blockID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bc := freshReader(newBeadsClientFn())
	root, err := workflowRoot(ctx, bc, proj.Path, *blockID)
	if err != nil {
		return fail(err)
	}
	owners, err := workflowOwnerRoles(ctx, bc, proj.Path, *blockID, root)
	if err != nil {
		return fail(err)
	}
	actor, err := authorizeAddendum(ctx, *project, owners)
	if err != nil {
		return fail(err)
	}
	nodes, err := workflowNodes(ctx, bc, proj.Path, *stepID)
	if err != nil {
		return fail(err)
	}
	var block, step *workflowtemplate.Node
	for i := range nodes {
		if nodes[i].Bead.ID == *blockID {
			block = &nodes[i]
		}
		if nodes[i].Bead.ID == *stepID {
			step = &nodes[i]
		}
	}
	if block == nil || step == nil || step.Bead.ParentID != block.Bead.ID || workflowtemplate.Label(block.Bead, workflowtemplate.LabelTemplate) != "pr-review" {
		return fail(fmt.Errorf("step must belong to existing review block"))
	}
	tpl, err := workflowtemplate.Definition(block.Bead)
	if err != nil {
		return fail(err)
	}
	verifier := ""
	for _, s := range tpl.Steps {
		if s.ID == workflowtemplate.Label(step.Bead, workflowtemplate.LabelStep) {
			verifier = s.Verifier
		}
	}
	if verifier != "review-proof" && verifier != "review-clean" && verifier != "recheck-clean" {
		return fail(fmt.Errorf("review step required"))
	}
	binding, err := workflowtemplate.Binding(block.Comments)
	if err != nil {
		return fail(err)
	}
	cs, err := bc.Comments(ctx, proj.Path, *assessorID)
	if err != nil {
		return fail(err)
	}
	var assessment beads.Comment
	for _, c := range cs {
		if c.ID == *assessmentID {
			assessment = c
		}
	}
	a, err := workflowtemplate.ParseAssessment(assessment.Text)
	representation := ""
	if *evidenceFile != "" {
		data, e := os.ReadFile(*evidenceFile)
		if e != nil {
			return fail(e)
		}
		var transcription workflowtemplate.AddendumTranscription
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&transcription); e != nil {
			return fail(e)
		}
		if e = dec.Decode(new(any)); e != io.EOF {
			return fail(fmt.Errorf("one transcription object required"))
		}
		source := workflowtemplate.CommentRef{Bead: *assessorID, ID: *assessmentID, SHA256: workflowtemplate.TextDigest(assessment.Text)}
		if transcription.Source != source {
			return fail(fmt.Errorf("transcription source identity or digest differs"))
		}
		a = transcription.Claims
		representation = workflowtemplate.ManagerTranscription
		err = nil
	}
	if err != nil {
		return fail(err)
	}
	if a.Original.ID != *originalID {
		return fail(fmt.Errorf("original comment differs from assessor attestation"))
	}
	policy, err := bc.Comments(ctx, proj.Path, *policyBead)
	if err != nil {
		return fail(err)
	}
	var policyComment beads.Comment
	for _, c := range policy {
		if c.ID == *policyID {
			policyComment = c
		}
	}
	r := workflowtemplate.AddendumRecord{Representation: representation, Project: *project, Block: *blockID, Step: *stepID, Binding: binding.Revision, Assessment: a, AssessmentComment: workflowtemplate.CommentRef{Bead: *assessorID, ID: *assessmentID, SHA256: workflowtemplate.TextDigest(assessment.Text)}, PolicyComment: workflowtemplate.CommentRef{Bead: *policyBead, ID: *policyID, SHA256: workflowtemplate.TextDigest(policyComment.Text)}, Actor: actor, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	originalComment, e := addendumComment(ctx, bc, proj.Path, a.Original)
	if e != nil {
		return fail(e)
	}
	r.OriginalAuthor = originalComment.Author
	r.OriginalCreatedAt = originalComment.CreatedAt
	r.AssessmentAuthor = assessment.Author
	r.AssessmentCreatedAt = assessment.CreatedAt
	r.OriginalRoles, err = workflowRolesInScope(ctx, bc, proj.Path, *project, root, a.Original.Bead)
	if err != nil {
		return fail(err)
	}
	r.AssessorRoles, err = workflowRolesInScope(ctx, bc, proj.Path, *project, root, *assessorID)
	if err != nil {
		return fail(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return fail(err)
	}
	r.ID = hex.EncodeToString(nonce)
	original, err := bc.Show(ctx, proj.Path, a.Original.Bead)
	if err != nil {
		return fail(err)
	}
	if original == nil {
		return fail(fmt.Errorf("original task missing"))
	}
	review := workflowtemplate.Review{ID: original.ID, Status: original.Status, Owner: strings.Join(r.OriginalRoles, ",")}
	if original.Status != "closed" {
		return fail(fmt.Errorf("original review must be closed"))
	}
	reports, err := bc.Comments(ctx, proj.Path, original.ID)
	if err != nil {
		return fail(err)
	}
	for _, c := range reports {
		review.Comments = append(review.Comments, c.Text)
	}
	if err = loadReviewResults(ctx, *project, original.ID, &review); err != nil {
		return fail(err)
	}
	h, err := workflowtemplate.ReadHead(ctx, workflowGH, binding.PR)
	if err != nil {
		return fail(err)
	}
	if err = validateWorkflowAddendum(ctx, bc, proj.Path, *project, block.Bead, *stepID, binding, r, h, review); err != nil {
		return fail(err)
	}
	after, err := workflowtemplate.ReadHead(ctx, workflowGH, binding.PR)
	if err != nil {
		return fail(err)
	}
	if after.SHA != h.SHA || after.Base != h.Base {
		return fail(fmt.Errorf("PR changed while recording"))
	}
	// Identical validated requests reuse the prior append-only record.
	existing, err := bc.Comments(ctx, proj.Path, *blockID)
	if err != nil {
		return fail(err)
	}
	for _, c := range existing {
		raw, ok := strings.CutPrefix(c.Text, workflowtemplate.AddendumPrefix)
		if !ok {
			continue
		}
		var old workflowtemplate.AddendumRecord
		if json.Unmarshal([]byte(raw), &old) != nil {
			continue
		}
		candidate := r
		candidate.ID = old.ID
		candidate.RecordedAt = old.RecordedAt
		if candidate.Payload() == old.Payload() {
			if err = json.NewEncoder(stdout).Encode(old.Reference()); err != nil {
				return fail(err)
			}
			return 0
		}
	}
	if err = bc.AddComment(ctx, proj.Path, *blockID, r.Comment()); err != nil {
		return fail(err)
	}
	if err = json.NewEncoder(stdout).Encode(r.Reference()); err != nil {
		return fail(err)
	}
	return 0
}
