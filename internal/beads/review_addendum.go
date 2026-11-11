package beads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/store"
	"strings"
	"time"
)

// Kept here to avoid a beads/workflowtemplate import cycle. This is attribution
// of the recorder, never a passing review or a substitute for artifact checks.
const reviewAddendumPrefix = "workflow-review-addendum@v1 "

func addendumDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func pgReviewAddendumComment(ctx context.Context, q store.Querier, pid int64, id, text string) (string, string, string, error) {
	raw, ok := strings.CutPrefix(text, reviewAddendumPrefix)
	if !ok {
		return "", "", "{}", nil
	}
	if e := requirePlanningCaller(ctx); e != nil {
		return "", "", "{}", e
	}
	var record struct {
		ID, Project, Block, Step, Actor string
		RecordedAt                      string `json:"recorded_at"`
	}
	if e := json.Unmarshal([]byte(raw), &record); e != nil {
		return "", "", "{}", e
	}
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	if actor == "" || record.Actor != actor || record.Block != id || record.ID == "" || record.Step == "" {
		return "", "", "{}", errors.New("review addendum must be recorded by its current planning Commander")
	}
	if _, e := time.Parse(time.RFC3339Nano, record.RecordedAt); e != nil {
		return "", "", "{}", fmt.Errorf("invalid addendum recording time: %w", e)
	}
	var owner, project string
	var revision int64
	var registered bool
	e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(?,?),p.name,COALESCE(j.revision,1),EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?) FROM projects p LEFT JOIN jurisdiction_projects j ON j.project_id=p.id WHERE p.id=?`, pid, id, actor, pid).Scan(&owner, &project, &revision, &registered)
	if e != nil {
		return "", "", "{}", e
	}
	if !registered || owner != actor || project != record.Project {
		return "", "", "{}", errors.New("review addendum planning ownership changed; recheck the current Commander")
	}
	fact, e := json.Marshal(struct {
		Actor    string `json:"actor"`
		Revision int64  `json:"jurisdiction_revision"`
	}{actor, revision})
	return "review-addendum", addendumDigest(text), string(fact), e
}

// RecordedReviewAddendum checks service-recorded attribution, which survives
// later jurisdiction transfers. Matching text alone is not a journal stamp.
func RecordedReviewAddendum(ctx context.Context, q store.Querier, pid int64, block, actor, text string) (bool, error) {
	var valid bool
	e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_comments WHERE project_id=? AND bead_id=? AND text=? AND author=? AND fact_kind='review-addendum' AND fact_definition=? AND fact_value->>'actor'=? AND (fact_value->>'jurisdiction_revision')::bigint>0)`, pid, block, text, actor, addendumDigest(text), actor).Scan(&valid)
	return valid, e
}
