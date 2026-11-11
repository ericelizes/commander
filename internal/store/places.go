package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Place is a paired device that runs Operators on the user's own machine
// (kind "mac"). Revoked places keep their row: a revoked place gets no offer.
type Place struct {
	Name      string // "mac:<slug>"
	Kind      string // "mac"
	PublicKey string // Ed25519 public key, base64
	PairedAt  int64
	PairedBy  string
	RevokedAt int64 // 0 = paired
}

// Paired reports whether the place may take runs.
func (p Place) Paired() bool { return p.RevokedAt == 0 }

// ErrPlaceExists is returned by AddPlace for a name already paired.
var ErrPlaceExists = errors.New("store: place already paired")

// AddPlace records a pairing. A revoked place of the same name is replaced
// (a new key); a paired one is refused.
func (s *Store) AddPlace(ctx context.Context, p Place) error {
	return s.WithTx(ctx, func(q Querier) error {
		if s.postgres {
			// Serialize even an absent name so concurrent first pairings cannot overwrite a key.
			if _, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?,734031823))`, p.Name); err != nil {
				return err
			}
		}
		var revoked sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT revoked_at FROM places WHERE name = ?`, p.Name).Scan(&revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("store: read place %s: %w", p.Name, err)
		case !revoked.Valid:
			return fmt.Errorf("%w: %s", ErrPlaceExists, p.Name)
		}
		_, err = q.ExecContext(ctx, `
			INSERT INTO places(name, kind, public_key, paired_at, paired_by, revoked_at)
			VALUES (?, ?, ?, ?, ?, NULL)
			ON CONFLICT(name) DO UPDATE SET kind = excluded.kind, public_key = excluded.public_key,
				paired_at = excluded.paired_at, paired_by = excluded.paired_by, revoked_at = NULL`,
			p.Name, p.Kind, p.PublicKey, p.PairedAt, p.PairedBy)
		if err != nil {
			return fmt.Errorf("store: add place %s: %w", p.Name, err)
		}
		return nil
	})
}

// RevokePlace marks a place revoked; false when it is unknown or already revoked.
func (s *Store) RevokePlace(ctx context.Context, name string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE places SET revoked_at = ? WHERE name = ? AND revoked_at IS NULL`, at, name)
	if err != nil {
		return false, fmt.Errorf("store: revoke place %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// GetPlace returns (nil, nil) for an unknown name.
func (s *Store) GetPlace(ctx context.Context, name string) (*Place, error) {
	var p Place
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT name, kind, public_key, paired_at, paired_by, revoked_at FROM places WHERE name = ?`, name).
		Scan(&p.Name, &p.Kind, &p.PublicKey, &p.PairedAt, &p.PairedBy, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get place %s: %w", name, err)
	}
	p.RevokedAt = revoked.Int64
	return &p, nil
}

// GetPlaceByKey returns the paired (not revoked) place holding a device key,
// (nil, nil) when none does.
func (s *Store) GetPlaceByKey(ctx context.Context, publicKey string) (*Place, error) {
	var p Place
	err := s.db.QueryRowContext(ctx, `SELECT name, kind, public_key, paired_at, paired_by FROM places WHERE public_key = ? AND revoked_at IS NULL`, publicKey).
		Scan(&p.Name, &p.Kind, &p.PublicKey, &p.PairedAt, &p.PairedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: place by key: %w", err)
	}
	return &p, nil
}

// ListPlaces returns every place, paired and revoked, by name.
func (s *Store) ListPlaces(ctx context.Context) ([]Place, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, kind, public_key, paired_at, paired_by, revoked_at FROM places ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list places: %w", err)
	}
	defer rows.Close()
	var out []Place
	for rows.Next() {
		var p Place
		var revoked sql.NullInt64
		if err := rows.Scan(&p.Name, &p.Kind, &p.PublicKey, &p.PairedAt, &p.PairedBy, &revoked); err != nil {
			return nil, err
		}
		p.RevokedAt = revoked.Int64
		out = append(out, p)
	}
	return out, rows.Err()
}

// Place run states. Offered and running hold the place (one run at a time);
// the rest are ended.
const (
	PlaceOffered  = "offered"
	PlaceRunning  = "running"
	PlaceDeclined = "declined"
	PlaceExited   = "exited"
	PlaceStopped  = "stopped"
	PlaceDied     = "died"
)

// PlaceRunActive reports whether a state still holds its place.
func PlaceRunActive(state string) bool { return state == PlaceOffered || state == PlaceRunning }

// PlaceRun is a run offered to a place. RunID is its agent_runs row.
type PlaceRun struct {
	RunID             int64
	Place             string
	State             string
	Reason            string // why it ended: usage_limit, declined, timeout, user, no-report ...
	StepID            string
	ManagerRole       string
	ProdGate          string
	BriefSHA          string
	TokenHash         string
	OfferedAt         int64
	AcceptedAt        int64
	LastBeat          int64
	EndedAt           int64
	Reported          bool
	UnreachableMailed bool
	Settled           bool
}

const placeRunCols = `run_id, place, state, reason, step_id, manager_role, prod_gate, brief_sha, token_hash,
	offered_at, accepted_at, last_beat, ended_at, reported, unreachable_mailed, settled`

func scanPlaceRun(row rowScanner) (*PlaceRun, error) {
	var r PlaceRun
	err := row.Scan(&r.RunID, &r.Place, &r.State, &r.Reason, &r.StepID, &r.ManagerRole, &r.ProdGate, &r.BriefSHA, &r.TokenHash,
		&r.OfferedAt, &r.AcceptedAt, &r.LastBeat, &r.EndedAt, &r.Reported, &r.UnreachableMailed, &r.Settled)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ErrPlaceBusy is returned by InsertPlaceRun when the place already holds an
// offered or running run (one at a time).
var ErrPlaceBusy = errors.New("store: place already has a run")

// InsertPlaceRun records the offer, refusing a second active run on the place
// in the same transaction that checks it.
func (s *Store) InsertPlaceRun(ctx context.Context, r PlaceRun) error {
	if r.State == "" {
		r.State = PlaceOffered
	}
	return s.WithTx(ctx, func(q Querier) error {
		if s.postgres {
			var name string
			if err := q.QueryRowContext(ctx, `SELECT name FROM places WHERE name=? FOR UPDATE`, r.Place).Scan(&name); err != nil {
				return err
			}
		}
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM place_runs WHERE place = ? AND state IN (?, ?)`,
			r.Place, PlaceOffered, PlaceRunning).Scan(&n); err != nil {
			return fmt.Errorf("store: count place runs: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s", ErrPlaceBusy, r.Place)
		}
		_, err := q.ExecContext(ctx, `INSERT INTO place_runs(`+placeRunCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.RunID, r.Place, r.State, r.Reason, r.StepID, r.ManagerRole, r.ProdGate, r.BriefSHA, r.TokenHash,
			r.OfferedAt, r.AcceptedAt, r.LastBeat, r.EndedAt, placeFlag(r.Reported), placeFlag(r.UnreachableMailed), placeFlag(r.Settled))
		if err != nil {
			return fmt.Errorf("store: insert place run %d: %w", r.RunID, err)
		}
		return nil
	})
}

// GetPlaceRun returns (nil, nil) for an unknown run.
func (s *Store) GetPlaceRun(ctx context.Context, runID int64) (*PlaceRun, error) {
	r, err := scanPlaceRun(s.db.QueryRowContext(ctx, `SELECT `+placeRunCols+` FROM place_runs WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get place run %d: %w", runID, err)
	}
	return r, nil
}

// ListPlaceRuns lists runs, all when place is "", newest last. activeOnly
// keeps offered and running ones; unsettled adds ended runs no settle took yet.
func (s *Store) ListPlaceRuns(ctx context.Context, place string, activeOnly bool) ([]PlaceRun, error) {
	q := `SELECT ` + placeRunCols + ` FROM place_runs WHERE 1=1`
	var args []any
	if place != "" {
		q += ` AND place = ?`
		args = append(args, place)
	}
	if activeOnly {
		q += ` AND state IN (?, ?)`
		args = append(args, PlaceOffered, PlaceRunning)
	}
	q += ` ORDER BY run_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list place runs: %w", err)
	}
	defer rows.Close()
	var out []PlaceRun
	for rows.Next() {
		r, err := scanPlaceRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// AcceptPlaceRun moves an offered run to running with its token's hash.
// False when the run is not offered (declined, ended, accepted already).
func (s *Store) AcceptPlaceRun(ctx context.Context, runID int64, tokenHash string, at int64) (bool, error) {
	_, accepted, err := s.AcceptPlaceRunWithToken(ctx, runID, at, func() (string, string) { return "", tokenHash })
	return accepted, err
}

// AcceptPlaceRunWithToken invokes mint only after canonical launch authority
// has been fenced. The plaintext leaves this function only after commit.
func (s *Store) AcceptPlaceRunWithToken(ctx context.Context, runID, at int64, mint func() (string, string)) (string, bool, error) {
	var accepted bool
	var token string
	err := s.WithTx(ctx, func(q Querier) error {
		allowed, err := s.placeExecutionAllowed(ctx, q, runID)
		if err != nil || !allowed {
			return err
		}
		var state string
		if err := q.QueryRowContext(ctx, `SELECT state FROM place_runs WHERE run_id=?`, runID).Scan(&state); err != nil {
			return err
		}
		if state != PlaceOffered {
			return nil
		}
		var tokenHash string
		token, tokenHash = mint()
		res, err := q.ExecContext(ctx, `UPDATE place_runs SET state = ?, token_hash = ?, accepted_at = ?, last_beat = ?
			WHERE run_id = ? AND state = ?`, PlaceRunning, tokenHash, at, at, runID, PlaceOffered)
		if err != nil {
			return fmt.Errorf("store: accept place run %d: %w", runID, err)
		}
		n, err := res.RowsAffected()
		accepted = n > 0
		return err
	})
	if err != nil || !accepted {
		return "", false, err
	}
	return token, accepted, nil
}

// PlaceExecutionAllowed revalidates current custody for a run-token request.
// Acceptance uses the same predicate inside the transaction that installs the
// token. An offered run is never evidence of a surviving physical process.
func (s *Store) PlaceExecutionAllowed(ctx context.Context, runID int64) (bool, error) {
	var allowed bool
	err := s.WithTx(ctx, func(q Querier) error {
		var err error
		allowed, err = s.placeExecutionAllowed(ctx, q, runID)
		return err
	})
	return allowed, err
}

func (s *Store) placeExecutionAllowed(ctx context.Context, q Querier, runID int64) (bool, error) {
	var allowed bool
	if !s.postgres {
		// Legacy task authority is read through the service's bead reader.
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=? AND status='running')`, runID).Scan(&allowed)
		return allowed, err
	}
	// WithTx holds graph custody before this mode fence. Direct canonical
	// writes also take that graph lock through migration 5's triggers.
	var mode string
	if err := q.QueryRowContext(ctx, `SELECT mode FROM orchestration_control WHERE id=1 FOR SHARE`).Scan(&mode); err != nil {
		return false, err
	}
	if mode != "active" && mode != "shadow" {
		return false, nil
	}
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM agent_runs r JOIN bead_tasks b ON b.project_id=r.project_id AND b.id=r.task_bead_id
	 JOIN projects p ON p.id=b.project_id
	 JOIN place_runs pr ON pr.run_id=r.id JOIN places device ON device.name=pr.place
	 WHERE device.revoked_at IS NULL AND r.id=? AND r.status='running' AND b.active_run_id=r.id AND b.assigned_agent=r.agent_name
	 AND b.deleted_at IS NULL AND p.status='active' AND b.result_run_id IS NULL
	 AND commander_execution_allowed(b.project_id,b.id,TRUE)
	 AND ((?='shadow' AND NOT EXISTS(SELECT 1 FROM execution_slots s WHERE s.run_id=r.id))
	 OR (?='active' AND EXISTS(SELECT 1 FROM execution_slots s WHERE s.run_id=r.id
	   AND s.project_id=b.project_id AND s.bead_id=b.id
	   AND s.enabled AND s.plan_revision=b.plan_revision AND s.responsibility_epoch=b.responsibility_epoch FOR SHARE OF s)))
	 FOR SHARE OF device
	)`, runID, mode, mode).Scan(&allowed)
	return allowed, err
}

// BeatPlaceRun records a heartbeat on a running run; false when not running.
func (s *Store) BeatPlaceRun(ctx context.Context, runID, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE place_runs SET last_beat = ?, unreachable_mailed = 0 WHERE run_id = ? AND state = ?`,
		at, runID, PlaceRunning)
	if err != nil {
		return false, fmt.Errorf("store: beat place run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EndPlaceRun moves an active run to an ended state, clears its token hash (the
// token dies with the run) and closes its agent_runs row. False when the run
// had already ended: the first ending wins.
func (s *Store) EndPlaceRun(ctx context.Context, runID int64, state, reason string, at int64) (bool, error) {
	if PlaceRunActive(state) {
		return false, fmt.Errorf("store: EndPlaceRun: %q is not an ended state", state)
	}
	ended := false
	err := s.WithTx(ctx, func(q Querier) error {
		res, err := q.ExecContext(ctx, `UPDATE place_runs SET state = ?, reason = ?, ended_at = ?, token_hash = ''
			WHERE run_id = ? AND state IN (?, ?)`, state, reason, at, runID, PlaceOffered, PlaceRunning)
		if err != nil {
			return fmt.Errorf("store: end place run %d: %w", runID, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		ended = true
		// The agent_runs row follows: the cap, LiveRuns and the reapers read it.
		runStatus := state
		if state == PlaceDied {
			runStatus = "orphan-reaped"
		}
		if _, err := q.ExecContext(ctx, `UPDATE agent_runs SET status = ? WHERE id = ? AND status = 'running'`, runStatus, runID); err != nil {
			return fmt.Errorf("store: close agent_run %d: %w", runID, err)
		}
		return nil
	})
	return ended, err
}

// MarkPlaceRun sets one of the flag columns (reported, unreachable_mailed, settled).
func (s *Store) MarkPlaceRun(ctx context.Context, runID int64, flag string, on bool) error {
	switch flag {
	case "reported", "unreachable_mailed", "settled":
	default:
		return fmt.Errorf("store: MarkPlaceRun: unknown flag %q", flag)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE place_runs SET `+flag+` = ? WHERE run_id = ?`, placeFlag(on), runID)
	if err != nil {
		return fmt.Errorf("store: mark place run %d %s: %w", runID, flag, err)
	}
	return nil
}

// RecordPlaceComment remembers a comment's client id; false when the run
// already posted that id (a retried send).
func (s *Store) RecordPlaceComment(ctx context.Context, runID int64, clientID string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO place_comments(run_id, client_id, created_at) VALUES (?, ?, ?) ON CONFLICT(run_id, client_id) DO NOTHING`, runID, clientID, at)
	if err != nil {
		return false, fmt.Errorf("store: record place comment: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ForgetPlaceComment drops a client id whose write failed, so a retry writes it.
func (s *Store) ForgetPlaceComment(ctx context.Context, runID int64, clientID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM place_comments WHERE run_id = ? AND client_id = ?`, runID, clientID)
	return err
}

// InsertPlaceOffer records a place run whole: its agent_runs row (running, no
// pane, no container) and its place_runs row, refusing a second active run on
// the place in the same transaction. It returns the run id.
func (s *Store) InsertPlaceOffer(ctx context.Context, run AgentRun, pr PlaceRun) (int64, error) {
	var id int64
	err := s.WithTx(ctx, func(q Querier) error {
		if s.postgres {
			var name string
			if err := q.QueryRowContext(ctx, `SELECT name FROM places WHERE name=? AND revoked_at IS NULL FOR UPDATE`, pr.Place).Scan(&name); err != nil {
				return err
			}
		}
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM place_runs WHERE place = ? AND state IN (?, ?)`,
			pr.Place, PlaceOffered, PlaceRunning).Scan(&n); err != nil {
			return fmt.Errorf("store: count place runs: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s", ErrPlaceBusy, pr.Place)
		}
		var err error
		if s.postgres {
			run.Status = "running"
			id, err = claimRun(ctx, q, run)
		} else {
			err = q.QueryRowContext(ctx, `INSERT INTO agent_runs(project_id, task_bead_id, agent_name, opencode_session_id,
				container_id, tmux_pane_id, spawn_time, tokens_used, usd_used, consecutive_failures, status, worktree_path)
				VALUES (?, ?, ?, ?, NULL, NULL, ?, 0, 0, 0, 'running', NULL) RETURNING id`,
				run.ProjectID, run.TaskBeadID, run.AgentName, run.OpencodeSessionID, run.SpawnTime).Scan(&id)
		}
		if err != nil {
			return fmt.Errorf("store: insert place agent_run: %w", err)
		}
		_, err = q.ExecContext(ctx, `INSERT INTO place_runs(`+placeRunCols+`) VALUES (?, ?, ?, '', ?, ?, ?, ?, '', ?, 0, 0, 0, 0, 0, 0)`,
			id, pr.Place, PlaceOffered, pr.StepID, pr.ManagerRole, pr.ProdGate, pr.BriefSHA, pr.OfferedAt)
		if err != nil {
			return fmt.Errorf("store: insert place run: %w", err)
		}
		return nil
	})
	return id, err
}

func placeFlag(on bool) int {
	if on {
		return 1
	}
	return 0
}
