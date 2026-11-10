package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
)

// OpenProvenance opens existing metadata read-only, without WAL configuration or
// migrations. This compatibility reader deliberately supports schema 20 onward;
// the queries below use only columns already present in schema 20.
func OpenProvenance(path string) (*Store, error) {
	dsn, e := PostgresDSN(path)
	if e != nil {
		return nil, e
	}
	if dsn != "" {
		return OpenPostgres(dsn)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version < 20 {
		err = fmt.Errorf("provenance requires schema 20 or later, found %d", version)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// ApprovedContextEvidence reads the latest pitch and its latest resolution in
// one SQLite snapshot. A newer unmatched pitch or resolution fails closed.
func (s *Store) ApprovedContextEvidence(ctx context.Context, projectID int64, beadID string) (Pitch, NeedsResolution, error) {
	var p Pitch
	var r NeedsResolution
	var payload, generation string
	var state int
	err := s.db.QueryRowContext(ctx, `SELECT p.project_id,p.bead_id,p.version,p.decision,p.answer,p.answered_at,r.generation,r.payload,r.state
 FROM pitches p JOIN needs_resolutions r ON r.project_id=p.project_id AND r.bead_id=p.bead_id AND r.generation=CAST(p.version AS TEXT)
 WHERE p.project_id=? AND p.bead_id=?
 AND p.version=(SELECT MAX(version) FROM pitches WHERE project_id=p.project_id AND bead_id=p.bead_id)
 AND r.rowid=(SELECT MAX(rowid) FROM needs_resolutions WHERE project_id=p.project_id AND bead_id=p.bead_id)`, projectID, beadID).Scan(&p.ProjectID, &p.BeadID, &p.Version, &p.Decision, &p.Answer, &p.AnsweredAt, &generation, &payload, &state)
	if err != nil {
		return p, r, fmt.Errorf("latest pitch/answer provenance unavailable: %w", err)
	}
	if err = json.Unmarshal([]byte(payload), &r); err != nil {
		return p, r, err
	}
	if r.ProjectID != projectID || r.BeadID != beadID || r.Generation != generation {
		return p, r, fmt.Errorf("resolution identity does not match stored pitch")
	}
	r.State = state
	return p, r, nil
}
