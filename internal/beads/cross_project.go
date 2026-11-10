package beads

import (
	"context"
	"errors"
	"fmt"
)

// CrossProjectError is a failure of the bounded cross-project identity/read or
// custody contract. Unlike legacy local read errors, it must never fail open.
// Err retains the underlying cause for errors.Is/As without classifying ordinary
// local failures as cross-project failures.
type CrossProjectError struct {
	Operation string
	Bead      string
	Err       error
}

func (e *CrossProjectError) Error() string {
	return fmt.Sprintf("refused: cross-project %s for %s: %v", e.Operation, e.Bead, e.Err)
}
func (e *CrossProjectError) Unwrap() error { return e.Err }

func IsCrossProjectError(err error) bool {
	var cross *CrossProjectError
	return errors.As(err, &cross)
}

// CustodySnapshotReader is implemented by a reader supporting foreign edges.
// It must validate qualified endpoint provenance before flattening IDs, reject
// ambiguous reached IDs, and bind all reads and RequireLocalCustody calls made
// by read to one MVCC snapshot through ctx. Nested calls must reuse that snapshot.
// A failed read must return a CrossProjectError when foreign provenance cannot
// be established, including unavailable or exhausted cross-project closures.
// This interface grants no authority to write another project's beads.
type CustodySnapshotReader interface {
	ReadCustodySnapshot(ctx context.Context, dir string, read func(context.Context) error) error
	RequireLocalCustody(ctx context.Context, dir string, ids []string) error
}

// ReadCustodySnapshot preserves legacy clients' read policy. Cross-project
// clients must expose CustodySnapshotReader, including through reader wrappers.
func ReadCustodySnapshot(ctx context.Context, bc Client, dir string, read func(context.Context) error) error {
	if r, ok := bc.(CustodySnapshotReader); ok {
		return r.ReadCustodySnapshot(ctx, dir, read)
	}
	return read(ctx)
}

// RequireLocalCustody validates the entire receipt set before the first write.
// A foreign or unproven source must return CrossProjectError; callers never
// redirect receipts to foreign projects. Legacy local-only clients are unchanged.
func RequireLocalCustody(ctx context.Context, bc Client, dir string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if r, ok := bc.(CustodySnapshotReader); ok {
		return r.RequireLocalCustody(ctx, dir, ids)
	}
	return nil
}
