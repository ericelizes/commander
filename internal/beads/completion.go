package beads

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/store"
)

type completionValidatorKey struct{}

// CompletionValidator checks fresh workflow evidence under the graph lock. Its
// writes are part of this transaction, so a failed close leaves no half-proof.
type CompletionValidator func(context.Context, store.Querier, int64, string) error

func WithCompletionValidator(ctx context.Context, v CompletionValidator) context.Context {
	return context.WithValue(ctx, completionValidatorKey{}, v)
}
func pgCheckCompletion(ctx context.Context, q store.Querier, pid int64, id, oldStatus, newStatus string, retired bool, labels []string) error {
	if newStatus != "closed" || oldStatus == "closed" {
		return nil
	}
	// All close callers, including roleless and legacy callers, share these
	// invariants. pgWrite holds the project graph lock before reading the bead.
	for _, w := range WaitsOf(labels) {
		if w.Kind == WaitReview {
			return fmt.Errorf("cannot close %s: Waiting for review (%s); record the required review first", id, w.Ref)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT b.id FROM bead_tasks b WHERE b.project_id=? AND b.deleted_at IS NULL AND b.status NOT IN ('closed','tombstone') AND (b.parent_id=? OR EXISTS (SELECT 1 FROM bead_dependencies d WHERE d.project_id=b.project_id AND d.issue_id=b.id AND d.depends_on_id=? AND d.type='parent-child')) ORDER BY b.id`, pid, id, id)
	if err != nil {
		return err
	}
	var unfinished []string
	for rows.Next() {
		var child string
		if err = rows.Scan(&child); err != nil {
			rows.Close()
			return err
		}
		unfinished = append(unfinished, child)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(unfinished) > 0 {
		return fmt.Errorf("cannot close %s: unfinished children (%s); finish them first (deferred work is unfinished)", id, strings.Join(unfinished, ", "))
	}
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	purpose := "pass"
	if retired {
		purpose = "retire"
	}
	ctx = context.WithValue(ctx, completionPurposeKey{}, purpose)
	return pgVerifyCompletion(ctx, q, pid, id)
}

// CompletionClient is a transaction-local client used exclusively by trusted
// completion verifiers. It retains ordinary scope/human/executor checks and
// never calls back into HTTP or opens a nested write transaction.
func CompletionClient(q store.Querier, pid int64) Client {
	c := &cmdClient{run: func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if len(args) == 2 && args[0] == "create" && args[1] == "--help" {
			return []byte("--no-inherit-labels"), nil, nil
		}
		out, e := pgWrite(context.WithValue(ctx, postgresReaderKey{}, q), q, pid, args)
		return out, nil, e
	}}
	return &postgresClient{cmdClient: c}
}
