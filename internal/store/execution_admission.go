package store

import (
	"context"
	"errors"
	"time"
)

// CheckExecutionAdmission is an early launch preflight, before preparing a
// workspace or starting a provider. InsertAgentRun repeats the mode check under
// a database lock; this read does not itself grant execution authority.
func (s *Store) CheckExecutionAdmission(ctx context.Context, projectID int64, bead, token string) error {
	if !s.postgres {
		return nil
	}
	control, e := s.OrchestrationControl(ctx)
	if e != nil {
		return e
	}
	if e = checkExecutionAdmission(control.Mode, token); e != nil {
		return e
	}
	if token != "" {
		var eligible bool
		if e = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_slots WHERE claim_token=? AND project_id=? AND bead_id=? AND enabled AND run_id IS NULL AND lease_until>?)`, token, projectID, bead, time.Now().Unix()).Scan(&eligible); e != nil {
			return e
		}
		if !eligible {
			return errors.New("execution startup claim is unavailable, expired, disabled, bound or belongs to another bead")
		}
	}
	return nil
}
func checkExecutionAdmission(mode, token string) error {
	if mode == "off" {
		return errors.New("execution is disabled by orchestration control")
	}
	if mode == "active" && token == "" {
		return errors.New("active execution requires a ready-bead capacity claim; use execution ready and let the scheduler admit the work")
	}
	if token != "" && mode != "active" {
		return errors.New("execution startup claims require active orchestration")
	}
	return nil
}
