package beads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

const AutomaticGatePath = "/api/gate/check"

type AutomaticGateRequest struct {
	Dir  string   `json:"dir"`
	Bead string   `json:"bead"`
	Env  []string `json:"env,omitempty"`
}
type AutomaticGateResponse struct {
	Exit      int    `json:"exit"`
	Error     string `json:"error,omitempty"`
	Bead      string `json:"bead,omitempty"`
	Status    string `json:"status,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	CheckedAt int64  `json:"checked_at,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
}

// CheckAutomaticGate delegates once to the canonical service. A failed gate is
// a recorded outcome, not a transport error. There is no local fallback.
func CheckAutomaticGate(ctx context.Context, dir, id string) (AutomaticGateResponse, error) {
	if !PostgresConfigured() {
		return AutomaticGateResponse{}, errors.New("canonical automatic checks require PostgreSQL")
	}
	base, e := os.ReadFile(filepath.Join(brHome(), "api-url"))
	if e != nil {
		return AutomaticGateResponse{}, e
	}
	token, e := os.ReadFile(filepath.Join(brHome(), "api-token"))
	if e != nil {
		return AutomaticGateResponse{}, e
	}
	if strings.TrimSpace(string(base)) == "" || strings.TrimSpace(string(token)) == "" {
		return AutomaticGateResponse{}, errors.New("automatic check requires the Commander service")
	}
	raw, _ := json.Marshal(AutomaticGateRequest{Dir: dir, Bead: id, Env: callerEnv()})
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, e := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(base)), "/")+AutomaticGatePath, bytes.NewReader(raw))
	if e != nil {
		return AutomaticGateResponse{}, e
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, e := doorHTTP.Do(req)
	if e != nil {
		return AutomaticGateResponse{}, e
	}
	defer resp.Body.Close()
	var out AutomaticGateResponse
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("automatic gate endpoint returned HTTP %d; no local check was run", resp.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); e != nil {
		return out, e
	}
	if out.Exit != 0 {
		return out, errors.New(out.Error)
	}
	if out.Bead != id || (out.Status != "passed" && out.Status != "failed") {
		return out, errors.New("automatic gate service returned no supported recorded result")
	}
	return out, nil
}

// AutomaticGateResult is server-owned context authority, never accepted from
// HTTP JSON, argv, a freeform comment, or a model's claimed outcome.
type AutomaticGateResult struct {
	ProjectID                 int64
	Bead, Definition          string
	PlanRevision, ProgressSeq int64
	Status, Evidence          string
	CheckedAt                 int64
}
type automaticGateResultKey struct{}
type automaticGateResultContext struct {
	result    AutomaticGateResult
	unchanged bool
}

func WithAutomaticGateResult(ctx context.Context, r AutomaticGateResult) context.Context {
	return context.WithValue(ctx, automaticGateResultKey{}, &automaticGateResultContext{result: r})
}
func AutomaticGateResultOf(ctx context.Context) (AutomaticGateResult, bool) {
	r, ok := ctx.Value(automaticGateResultKey{}).(*automaticGateResultContext)
	if !ok {
		return AutomaticGateResult{}, false
	}
	return r.result, true
}
func pgCheckAutomaticGateResult(ctx context.Context, q store.Querier, pid int64, dir string) error {
	result, ok := AutomaticGateResultOf(ctx)
	if !ok {
		return nil
	}
	if result.ProjectID != pid || result.Bead == "" || (result.Status != "passed" && result.Status != "failed") {
		return errors.New("invalid automatic gate result authority")
	}
	var locked string
	if e := q.QueryRowContext(ctx, `SELECT id FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, pid, result.Bead).Scan(&locked); e != nil {
		return e
	}
	readCtx := context.WithValue(ctx, postgresReaderKey{}, q)
	c := &postgresClient{}
	b, e := c.Show(readCtx, dir, result.Bead)
	if e != nil {
		return e
	}
	if b == nil {
		return ErrNotFound
	}
	d, e := c.Details(readCtx, dir, []string{result.Bead})
	if e != nil {
		return e
	}
	detail, ok := d[result.Bead]
	if !ok || b.PlanRevision != result.PlanRevision || b.ProgressSeq != result.ProgressSeq || CompletionDefinition(*b, detail) != result.Definition {
		return errors.New("automatic gate changed while its check ran; the result was not applied")
	}
	var definition string
	var raw []byte
	e = q.QueryRowContext(ctx, `SELECT fact_definition,fact_value FROM bead_comments WHERE project_id=? AND bead_id=? AND fact_kind='automatic-gate' ORDER BY created_at DESC,id DESC LIMIT 1`, pid, result.Bead).Scan(&definition, &raw)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil && definition == result.Definition {
		var prior AutomaticGateResult
		if e = json.Unmarshal(raw, &prior); e != nil {
			return e
		}
		matches := result.Status == "passed" && b.Status == "closed" && !slices.Contains(b.Labels, "gate-status:failed") || result.Status == "failed" && b.Status == "open" && slices.Contains(b.Labels, "gate-status:failed")
		if matches && prior.Status == result.Status && prior.Evidence == result.Evidence {
			value, _ := ctx.Value(automaticGateResultKey{}).(*automaticGateResultContext)
			value.unchanged = true
		}
	}
	return nil
}

// CanonicalAutomaticGateOperations is the only mutation batch authorized by a
// server-owned automatic result; callers cannot attach it to unrelated edits.
func CanonicalAutomaticGateOperations(r AutomaticGateResult) [][]string {
	update := []string{"update", r.Bead, "--status", "closed", "--remove-label", "gate-status:failed"}
	if r.Status != "passed" {
		update = []string{"update", r.Bead, "--status", "open", "--add-label", "gate-status:failed"}
	}
	return [][]string{{"comments", "add", r.Bead, AutomaticGateComment(r)}, update}
}
func AutomaticGateComment(r AutomaticGateResult) string {
	return fmt.Sprintf("gate %s by auto at %d: %s", r.Status, r.CheckedAt, r.Evidence)
}
func AutomaticGateUnchanged(ctx context.Context) bool {
	v, ok := ctx.Value(automaticGateResultKey{}).(*automaticGateResultContext)
	return ok && v.unchanged
}
func validateAutomaticGateOperations(ctx context.Context, ops [][]string) error {
	if r, ok := AutomaticGateResultOf(ctx); ok && !reflect.DeepEqual(ops, CanonicalAutomaticGateOperations(r)) {
		return errors.New("automatic result authority permits only its canonical gate fact batch")
	}
	return nil
}

func pgAutomaticGateComment(ctx context.Context, q store.Querier, pid int64, id, text string) (string, string, string, error) {
	normalized := strings.TrimSpace(text)
	if !strings.HasPrefix(normalized, "gate passed by ") && !strings.HasPrefix(normalized, "gate failed by ") {
		return "", "", "{}", nil
	}
	var automatic bool
	if e := q.QueryRowContext(ctx, `SELECT jsonb_exists(labels,'verifier:auto') FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&automatic); e != nil {
		return "", "", "{}", e
	}
	if !automatic {
		return "", "", "{}", nil
	}
	r, ok := AutomaticGateResultOf(ctx)
	if !ok {
		active, e := pgActiveExecution(ctx, q)
		if e != nil {
			return "", "", "{}", e
		}
		if active {
			return "", "", "{}", errors.New("automatic result comments are service-owned; use the canonical gate check")
		}
		return "", "", "{}", nil
	}
	if r.ProjectID != pid || r.Bead != id || text != AutomaticGateComment(r) {
		return "", "", "{}", errors.New("automatic result comment differs from the verified outcome")
	}
	raw, e := json.Marshal(r)
	return "automatic-gate", r.Definition, string(raw), e
}

type completionPurposeKey struct{}

func CompletionPurpose(ctx context.Context) string {
	v, _ := ctx.Value(completionPurposeKey{}).(string)
	return v
}
