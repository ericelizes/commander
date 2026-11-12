package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"regexp"
	"strings"
	"time"
)

// Gate statuses.
const (
	GateOpen      = "open"
	GatePassed    = "passed"
	GateFailed    = "failed"
	GateWithdrawn = "withdrawn" // cancellation never satisfies a gate
)

// Gate verifiers: auto runs Check as a command; reviewer:<login> and user are people.
const (
	VerifierAuto = "auto"
	VerifierUser = "user"
	// VerifierOperator is wired by an Operator when the track starts: it never
	// auto-runs, and is passed with evidence by an Operator or Manager.
	VerifierOperator = "operator"
	// VerifierReviewerPrefix starts "reviewer:<login>".
	VerifierReviewerPrefix = "reviewer:"
)

// Gate is one check a stage must pass before it closes.
type Gate struct {
	ProjectID int64  `json:"-"`
	BeadID    string `json:"-"`
	Name      string `json:"name"`
	Check     string `json:"check"`
	Verifier  string `json:"verifier"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence,omitempty"`
	By        string `json:"by,omitempty"` // who passed it ("auto" for a command)
	At        int64  `json:"at,omitempty"` // unix seconds of the last result
}

var (
	gateNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)
	gateLoginRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// ValidateGate checks a gate's name, check and verifier.
func ValidateGate(g Gate) error {
	if !gateNameRe.MatchString(g.Name) {
		return fmt.Errorf("gate name %q: want letters, digits, - and _ (40 max)", g.Name)
	}
	if strings.TrimSpace(g.Check) == "" {
		return errors.New("a gate needs --check: a shell command (auto) or the rule to verify")
	}
	g.Verifier = ownercompat.Normalize(g.Verifier)
	switch {
	case g.Verifier == VerifierAuto, g.Verifier == VerifierUser, g.Verifier == VerifierOperator:
	case strings.HasPrefix(g.Verifier, VerifierReviewerPrefix) && gateLoginRe.MatchString(strings.TrimPrefix(g.Verifier, VerifierReviewerPrefix)):
	default:
		return fmt.Errorf("--verifier %q: want auto, operator, reviewer:<login> or user", g.Verifier)
	}
	return nil
}

// UpsertGate adds a gate, or redefines it (reset to open) when the name exists.
func (s *Store) UpsertGate(ctx context.Context, g Gate) error {
	if g.BeadID == "" {
		return errors.New("store: UpsertGate requires a bead id")
	}
	g.Verifier = ownercompat.Normalize(g.Verifier)
	if err := ValidateGate(g); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gates(project_id, bead_id, name, check_text, verifier, created) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, bead_id, name) DO UPDATE SET
		  check_text = excluded.check_text, verifier = excluded.verifier,
		  status = 'open', evidence = '', by_who = '', at = 0`,
		g.ProjectID, g.BeadID, g.Name, strings.TrimSpace(g.Check), g.Verifier, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store: upsert gate %s/%s: %w", g.BeadID, g.Name, err)
	}
	return nil
}

// SetGateResult records a gate's status, evidence and who set it.
func (s *Store) SetGateResult(ctx context.Context, projectID int64, beadID, name, status, evidence, by string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE gates SET status = ?, evidence = ?, by_who = ?, at = ?
		WHERE project_id = ? AND bead_id = ? AND name = ?`,
		status, evidence, by, at.Unix(), projectID, beadID, name)
	if err != nil {
		return fmt.Errorf("store: set gate result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no gate %q on %s", name, beadID)
	}
	return nil
}

// DeleteGate removes one gate.
func (s *Store) DeleteGate(ctx context.Context, projectID int64, beadID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM gates WHERE project_id = ? AND bead_id = ? AND name = ?`, projectID, beadID, name)
	if err != nil {
		return fmt.Errorf("store: delete gate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no gate %q on %s", name, beadID)
	}
	return nil
}

const gateCols = `project_id, bead_id, name, check_text, verifier, status, evidence, by_who, at`

func (s *Store) queryGates(ctx context.Context, where string, args ...any) ([]Gate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+gateCols+` FROM gates `+where+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list gates: %w", err)
	}
	defer rows.Close()
	var out []Gate
	for rows.Next() {
		var g Gate
		if err := rows.Scan(&g.ProjectID, &g.BeadID, &g.Name, &g.Check, &g.Verifier, &g.Status, &g.Evidence, &g.By, &g.At); err != nil {
			return nil, err
		}
		g.Verifier = ownercompat.Normalize(g.Verifier)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListGates returns a bead's gates in the order they were added.
func (s *Store) ListGates(ctx context.Context, projectID int64, beadID string) ([]Gate, error) {
	return s.queryGates(ctx, `WHERE project_id = ? AND bead_id = ?`, projectID, beadID)
}

// ListGatesByBead returns the gates of a bead id in any project (bead ids carry the project's prefix).
func (s *Store) ListGatesByBead(ctx context.Context, beadID string) ([]Gate, error) {
	return s.queryGates(ctx, `WHERE bead_id = ?`, beadID)
}

// ListAllGates returns every gate.
func (s *Store) ListAllGates(ctx context.Context) ([]Gate, error) {
	return s.queryGates(ctx, ``)
}

// UnpassedGates names the gates that block closing: not passed, or passed with no evidence.
func UnpassedGates(gs []Gate) []Gate {
	var out []Gate
	for _, g := range gs {
		if g.Status != GatePassed || strings.TrimSpace(g.Evidence) == "" {
			g.Verifier = ownercompat.Normalize(g.Verifier)
			out = append(out, g)
		}
	}
	return out
}
