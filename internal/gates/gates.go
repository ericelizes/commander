// Package gates stores a stage's gates as beads: one kind:gate bead per gate.
//
//	title        "<stage>: gate <name>"
//	description  the check (a shell command for auto, else the rule to verify)
//	labels       kind:gate, gate-of:<stage>, gate-name:<name>, verifier:<v>,
//	             approver:<who> for a person's gate, gate-status:failed after a failed check
//	status       closed = passed; open = open or failed
//	comments     "gate <status> by <who> at <unix>: <evidence>"; the latest one is the evidence
//
// A stage gate shares its guarded stage’s parent. Approval gates with an
// approver can join the objective tree without changing stored labels.
package gates

import (
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Labels a gate bead carries.
const (
	KindLabel      = "kind:gate"
	FailedLabel    = "gate-status:failed"
	RetiredLabel   = "retired"
	stageLabelPfx  = "gate-of:"
	nameLabelPfx   = "gate-name:"
	verifierLblPfx = "verifier:"
)

// Node is one gate bead read back: the gate (BeadID = its stage) and its own bead id.
type Node struct {
	ID   string
	Gate store.Gate
	Viz  int // the gate bead's viz:<n> label, 0 when it has none
}

var resultRe = regexp.MustCompile(`(?s)^gate (passed|failed) by (\S+) at (\d+): ?(.*)$`)

// Labels returns the labels of a gate's bead.
func Labels(g store.Gate) []string {
	g.Verifier = ownercompat.Normalize(g.Verifier)
	labels := []string{KindLabel, stageLabelPfx + escapeLabel(g.BeadID), nameLabelPfx + g.Name, verifierLblPfx + g.Verifier}
	if g.Verifier == store.VerifierUser {
		labels = append(labels, beads.ApproverLabelPrefix+"user")
	} else if login, ok := strings.CutPrefix(g.Verifier, store.VerifierReviewerPrefix); ok {
		labels = append(labels, beads.ApproverLabelPrefix+login)
	}
	return labels
}

func escapeLabel(v string) string   { return beads.EscapeLabel(v) }
func unescapeLabel(v string) string { return beads.UnescapeLabel(v) }

// IsGate reports whether a bead is a gate bead.
func IsGate(b beads.Bead) bool { return slices.Contains(b.Labels, KindLabel) }

// IsRetired reports whether a gate bead was retired: labeled retired or
// superseded, or closed with a reason that says superseded. A retired gate is
// neither passed nor open; it is out of the stage's checks.
func IsRetired(b beads.Bead) bool {
	return slices.Contains(b.Labels, RetiredLabel) || slices.Contains(b.Labels, "superseded") ||
		b.Status == "closed" && strings.Contains(strings.ToLower(b.CloseReason), "superseded")
}

func labelValue(labels []string, prefix string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return v
		}
	}
	return ""
}

// parse turns a gate bead into a Node; comments (may be nil) carry the evidence.
func parse(b beads.Bead, projectID int64, comments []beads.Comment) Node {
	g := store.Gate{
		ProjectID: projectID,
		BeadID:    unescapeLabel(labelValue(b.Labels, stageLabelPfx)),
		Name:      labelValue(b.Labels, nameLabelPfx),
		Verifier:  ownercompat.Normalize(labelValue(b.Labels, verifierLblPfx)),
		Check:     b.Description,
		Status:    store.GateOpen,
	}
	switch {
	case slices.Contains(b.Labels, beads.WithdrawnLabel):
		g.Status = store.GateWithdrawn
	case b.Status == "closed":
		g.Status = store.GatePassed
	case slices.Contains(b.Labels, FailedLabel):
		g.Status = store.GateFailed
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if m := resultRe.FindStringSubmatch(comments[i].Text); m != nil {
			g.By, g.Evidence = m[2], m[4]
			g.At, _ = strconv.ParseInt(m[3], 10, 64)
			break
		}
	}
	return Node{ID: b.ID, Gate: g}
}

// hasEvidence is whether a gate has been decided, so its comments carry the evidence.
func hasEvidence(b beads.Bead) bool {
	return b.Status == "closed" || slices.Contains(b.Labels, FailedLabel)
}

func list(ctx context.Context, bc beads.Client, dir string, projectID int64, labels ...string) ([]Node, error) {
	bs, err := bc.List(ctx, dir, beads.ListOptions{Labels: append([]string{KindLabel}, labels...), Status: "all"})
	if err != nil {
		return nil, err
	}
	live := make([]beads.Bead, 0, len(bs))
	var decided []string
	for _, b := range bs {
		if b.Status == "tombstone" || labelValue(b.Labels, stageLabelPfx) == "" || IsRetired(b) {
			continue
		}
		live = append(live, b)
		if hasEvidence(b) {
			decided = append(decided, b.ID)
		}
	}
	// One br show for every decided gate's comments, not a br call per gate.
	var details map[string]beads.Detail
	if len(decided) > 0 {
		var partial *beads.PartialDetailsError
		if details, err = bc.Details(ctx, dir, decided); err != nil && !errors.As(err, &partial) {
			return nil, err
		}
	}
	out := make([]Node, 0, len(live))
	for _, b := range live {
		n := parse(b, projectID, details[b.ID].Comments)
		n.Viz = beads.VizOf(b.Labels)
		out = append(out, n)
	}
	// Creation order, as the table listed them (ids sort by creation within a stage).
	slices.SortStableFunc(out, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// All lists every gate of a project for display, accepting bounded detail lag.
func All(ctx context.Context, bc beads.Client, dir string, projectID int64) ([]Node, error) {
	return list(ctx, bc, dir, projectID)
}

// AllFresh lists every gate with DB-backed evidence for enforcement or writes.
func AllFresh(ctx context.Context, bc beads.Client, dir string, projectID int64) ([]Node, error) {
	return All(ctx, beads.ForActions(bc), dir, projectID)
}

// AutomaticFresh reads only automatic condition gates for the bounded watcher.
func AutomaticFresh(ctx context.Context, bc beads.Client, dir string, projectID int64) ([]Node, error) {
	return list(ctx, beads.ForActions(bc), dir, projectID, "verifier:auto")
}

// ForStage lists the gates of one stage bead for display.
func ForStage(ctx context.Context, bc beads.Client, dir string, projectID int64, stageID string) ([]Node, error) {
	return list(ctx, bc, dir, projectID, stageLabelPfx+escapeLabel(stageID))
}

// ForStageFresh lists a stage's gates with DB-backed evidence for enforcement
// or writes. Like AllFresh, it retains the client's List policy.
func ForStageFresh(ctx context.Context, bc beads.Client, dir string, projectID int64, stageID string) ([]Node, error) {
	return ForStage(ctx, beads.ForActions(bc), dir, projectID, stageID)
}

// Upsert adds the gate, or redefines it (reset to open) when the stage already
// has one by that name: the old bead is tombstoned and a new one made.
func Upsert(ctx context.Context, bc beads.Client, dir string, g store.Gate) (string, error) {
	if g.BeadID == "" {
		return "", fmt.Errorf("gates: Upsert requires a stage bead id")
	}
	g.Verifier = ownercompat.Normalize(g.Verifier)
	g.Check = strings.TrimSpace(g.Check)
	if err := store.ValidateGate(g); err != nil {
		return "", err
	}
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{g.BeadID})
	if err != nil {
		return "", err
	}
	stage, ok := ds[g.BeadID]
	if !ok {
		return "", fmt.Errorf("missing stage %s", g.BeadID)
	}
	existing, err := ForStageFresh(ctx, bc, dir, g.ProjectID, g.BeadID)
	if err != nil {
		return "", err
	}
	for _, e := range existing {
		if e.Gate.Name == g.Name {
			if err := bc.Delete(ctx, dir, e.ID, "gate redefined"); err != nil {
				return "", err
			}
		}
	}
	return bc.Create(ctx, dir, g.BeadID+": gate "+g.Name, beads.CreateOptions{
		Labels: Labels(g), Description: g.Check, Parent: stage.Parent, Silent: true,
	})
}

// SetResult records a gate's outcome: the result comment first (so a crash
// leaves the evidence), then the bead's state. Passed closes the bead; open or
// failed leaves (or reopens) it.
func SetResult(ctx context.Context, bc beads.Client, dir string, n Node, status, evidence, by string, at time.Time) error {
	b, err := beads.FreshShow(ctx, bc, dir, n.ID)
	if err != nil {
		return err
	}
	if b == nil || slices.Contains(b.Labels, beads.WithdrawnLabel) {
		return fmt.Errorf("gate %s is missing or withdrawn; withdrawal never authorizes approval", n.ID)
	}
	if by == "" {
		by = "unknown"
	}
	text := fmt.Sprintf("gate %s by %s at %d: %s", status, by, at.Unix(), strings.TrimSpace(evidence))
	if err := bc.AddComment(ctx, dir, n.ID, text); err != nil {
		return err
	}
	switch status {
	case store.GatePassed:
		if n.Gate.Status == store.GateFailed {
			if err := bc.SetLabel(ctx, dir, n.ID, FailedLabel, false); err != nil {
				return err
			}
		}
		if n.Gate.Status != store.GatePassed {
			return bc.Close(ctx, dir, n.ID)
		}
	case store.GateFailed:
		if n.Gate.Status == store.GatePassed {
			if err := bc.SetStatus(ctx, dir, n.ID, "open"); err != nil {
				return err
			}
		}
		if n.Gate.Status != store.GateFailed {
			return bc.SetLabel(ctx, dir, n.ID, FailedLabel, true)
		}
	}
	return nil
}

// MergedCheck is the standard merged gate's check: every PR, in merge order, reads MERGED.
func MergedCheck(prs []string, repo string) string {
	flag := ""
	if repo != "" {
		flag = " --repo " + repo
	}
	var parts []string
	for _, pr := range prs {
		parts = append(parts, fmt.Sprintf(`[ "$(gh pr view %s%s --json state --jq .state)" = MERGED ]`, pr, flag))
	}
	return strings.Join(parts, " && ")
}

var mergedCheckRe = regexp.MustCompile(`^\[ "\$\(gh pr view [0-9]+(?: --repo [\w.-]+/[\w.-]+)? --json state --jq \.state\)" = MERGED \]$`)

// IsMergedCheck says whether check is exactly the standard merged check, not one redefined to pass.
func IsMergedCheck(check string) bool {
	for _, part := range strings.Split(check, " && ") {
		if !mergedCheckRe.MatchString(part) {
			return false
		}
	}
	return true
}

// Remove retires a stage's gate by name: it is labeled retired and closed, so
// every listing skips it and it no longer counts as passed or open.
func Remove(ctx context.Context, bc beads.Client, dir string, projectID int64, stageID, name string) error {
	ns, err := ForStageFresh(ctx, bc, dir, projectID, stageID)
	if err != nil {
		return err
	}
	for _, n := range ns {
		if n.Gate.Name == name {
			if beads.PostgresConfigured() {
				closed := "closed"
				return bc.UpdateFields(ctx, dir, n.ID, beads.Fields{Status: &closed, AddLabels: []string{RetiredLabel}})
			}
			if err := bc.SetLabel(ctx, dir, n.ID, RetiredLabel, true); err != nil {
				return err
			}
			if n.Gate.Status == store.GatePassed {
				return nil
			}
			return bc.Close(ctx, dir, n.ID)
		}
	}
	return fmt.Errorf("no gate %q on %s", name, stageID)
}

// Migrate moves every gates-table row of the project into a gate bead, then
// drops the row. A row whose bead already exists (a run that died between the
// two steps) is just dropped, so re-running is safe. A row that fails stays in
// the table and the rest carry on; the failures come back joined in the error.
// The table itself stays in the schema, unused.
func Migrate(ctx context.Context, st *store.Store, bc beads.Client, project store.Project) (int, error) {
	rows := []store.Gate{}
	all, err := st.ListAllGates(ctx)
	if err != nil {
		return 0, err
	}
	for _, g := range all {
		if g.ProjectID == project.ID {
			rows = append(rows, g)
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	existing, err := AllFresh(ctx, bc, project.Path, project.ID)
	if err != nil {
		return 0, err
	}
	migrated := 0
	var failures []error
	for _, g := range rows {
		have := false
		for _, e := range existing {
			have = have || (e.Gate.BeadID == g.BeadID && e.Gate.Name == g.Name)
		}
		if !have {
			id, err := Upsert(ctx, bc, project.Path, g)
			if err != nil {
				failures = append(failures, fmt.Errorf("migrate gate %s/%s: %w", g.BeadID, g.Name, err))
				continue
			}
			if g.Status != store.GateOpen || g.Evidence != "" {
				n := Node{ID: id, Gate: store.Gate{Status: store.GateOpen}}
				if err := SetResult(ctx, bc, project.Path, n, g.Status, g.Evidence, g.By, time.Unix(g.At, 0)); err != nil {
					failures = append(failures, fmt.Errorf("migrate gate %s/%s result: %w", g.BeadID, g.Name, err))
					continue
				}
			}
			migrated++
		}
		if err := st.DeleteGate(ctx, g.ProjectID, g.BeadID, g.Name); err != nil {
			failures = append(failures, err)
		}
	}
	return migrated, errors.Join(failures...)
}

// MigrateAll runs Migrate for every project; a project that fails does not
// stop the rest. The error lists every failure.
func MigrateAll(ctx context.Context, st *store.Store, bc beads.Client) (int, error) {
	ps, err := st.ListProjects(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var failures []error
	for _, p := range ps {
		n, err := Migrate(ctx, st, bc, p)
		total += n
		if err != nil {
			failures = append(failures, fmt.Errorf("project %s: %w", p.Name, err))
		}
	}
	return total, errors.Join(failures...)
}

// Publish repairs only the missing parent edge of an existing stage gate.
// It never recreates the bead or changes its labels, pitch, status or waits.
func Publish(ctx context.Context, bc beads.Client, dir, id, parent string) error {
	return PublishPlan(ctx, bc, dir, id, parent, false)
}

// PublishPlan validates exactly the same fresh evidence for dry runs and writes.
func PublishPlan(ctx context.Context, bc beads.Client, dir, id, parent string, dryRun bool) error {
	if parent == "" || id == parent {
		return fmt.Errorf("publish: --parent must name the guarded stage's parent")
	}
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{id, parent})
	if err != nil {
		return err
	}
	d, ok := ds[id]
	if !ok {
		return fmt.Errorf("no gate %s", id)
	}
	if _, ok := ds[parent]; !ok {
		return fmt.Errorf("no stage %s", parent)
	}
	b, err := beads.FreshShow(ctx, bc, dir, id)
	if err != nil {
		return err
	}
	if b == nil || !IsGate(*b) {
		return fmt.Errorf("publish: %s must be kind:gate", id)
	}
	guarded := unescapeLabel(labelValue(b.Labels, stageLabelPfx))
	if guarded == "" {
		return fmt.Errorf("publish: missing gate-of")
	}
	gd, err := beads.FreshDetails(ctx, bc, dir, []string{guarded})
	if err != nil {
		return err
	}
	stage, ok := gd[guarded]
	if !ok || stage.Parent != parent {
		return fmt.Errorf("publish: parent must be guarded stage %s's existing parent", guarded)
	}
	sb, err := beads.FreshShow(ctx, bc, dir, guarded)
	if err != nil {
		return err
	}
	if sb == nil || !publicationObjective(*sb) || slices.Contains(sb.Labels, "hold") {
		return fmt.Errorf("publish: invalid or held guarded stage %s", guarded)
	}
	if slices.Contains(b.Labels, "hold") {
		return fmt.Errorf("publish: %s is held", id)
	}
	if d.Parent != "" && d.Parent != parent {
		return fmt.Errorf("publish: %s already has parent %s", id, d.Parent)
	}
	seen := map[string]bool{id: true}
	for cur := parent; cur != ""; {
		if seen[cur] {
			return fmt.Errorf("publish: parent would create a cycle; %s", publishCycleHint)
		}
		seen[cur] = true
		got, err := beads.FreshDetails(ctx, bc, dir, []string{cur})
		if err != nil {
			return err
		}
		ancestor, ok := got[cur]
		if !ok {
			return fmt.Errorf("publish: cannot read ancestor %s", cur)
		}
		ab, err := beads.FreshShow(ctx, bc, dir, cur)
		if err != nil {
			return err
		}
		if ab == nil || !publicationObjective(*ab) {
			return fmt.Errorf("publish: cannot read ancestor %s", cur)
		}
		if ancestor.Parent == "" && slices.Contains(ab.Labels, beads.TrackLabel) {
			return fmt.Errorf("publish: root %s is a track, not a workstream", cur)
		}
		if slices.Contains(ab.Labels, "hold") {
			return fmt.Errorf("publish: ancestor %s is held", cur)
		}
		cur = ancestor.Parent
	}
	if d.Parent == parent || dryRun {
		return nil
	}
	if err := bc.SetParent(ctx, dir, id, parent); err != nil {
		if strings.Contains(err.Error(), "cycle") {
			return fmt.Errorf("%w; %s", err, publishCycleHint)
		}
		return err
	}
	return nil
}

// publishCycleHint is the way out when a bead above the stage waits on its gate.
const publishCycleHint = "if the guarded stage's ancestors wait on this gate, internal-gate publish --repair-ancestor-waits W1,W2 --reason R --dry-run checks removing those waits (docs/cli/publication-repair.md)"

// publicationObjective excludes synthetic roots and approval-only ancestors.
func publicationObjective(b beads.Bead) bool {
	return slices.Contains(b.Labels, "kind:objective") && !slices.Contains(b.Labels, "kind:task") && !IsGate(b) && !slices.Contains(b.Labels, beads.GateLabel) && !beads.Superseded(b) && b.Status != "closed" && b.Status != "tombstone" && b.Status != beads.StatusDeferred
}
