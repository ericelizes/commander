package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

type OrchestrationControl struct {
	Mode      string `json:"mode"`
	Revision  int64  `json:"revision"`
	Reason    string `json:"reason"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Store) OrchestrationControl(ctx context.Context) (OrchestrationControl, error) {
	c := OrchestrationControl{Mode: "legacy"}
	if !s.postgres {
		return c, nil
	}
	e := s.db.QueryRowContext(ctx, `SELECT mode,revision,reason,updated_at FROM orchestration_control WHERE id=1`).Scan(&c.Mode, &c.Revision, &c.Reason, &c.UpdatedAt)
	return c, e
}

func (s *Store) ActiveOrchestration(ctx context.Context) (bool, error) {
	c, e := s.OrchestrationControl(ctx)
	return c.Mode == "active", e
}

// SetOrchestrationControl records one installation-wide choice. The daemon
// reads it before starting workers; a controlled restart applies mode changes.
// Every CLI and chat resolver can read the same database value independently.
func (s *Store) SetOrchestrationControl(ctx context.Context, revision int64, mode, reason string) (OrchestrationControl, error) {
	if !s.postgres {
		return OrchestrationControl{}, errors.New("orchestration control requires PostgreSQL")
	}
	if mode != "off" && mode != "shadow" && mode != "active" {
		return OrchestrationControl{}, errors.New("mode must be off, shadow or active")
	}
	if strings.TrimSpace(reason) == "" {
		return OrchestrationControl{}, errors.New("mode change requires a recorded reason")
	}
	result, e := s.db.ExecContext(ctx, `UPDATE orchestration_control SET mode=?,revision=revision+1,reason=?,updated_at=? WHERE id=1 AND revision=?`, mode, reason, time.Now().Unix(), revision)
	if e != nil {
		return OrchestrationControl{}, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return OrchestrationControl{}, e
	}
	if n != 1 {
		return OrchestrationControl{}, ErrJurisdictionConflict
	}
	return s.OrchestrationControl(ctx)
}
