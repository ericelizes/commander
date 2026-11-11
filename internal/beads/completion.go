package beads

import (
	"context"

	"github.com/example-user/commander/internal/store"
)

type completionValidatorKey struct{}

// CompletionValidator checks fresh workflow evidence under the graph lock. Its
// writes are part of this transaction, so a failed close leaves no half-proof.
type CompletionValidator func(context.Context, store.Querier, int64, string) error

func WithCompletionValidator(ctx context.Context, v CompletionValidator) context.Context {
	return context.WithValue(ctx, completionValidatorKey{}, v)
}
func pgCheckCompletion(ctx context.Context, q store.Querier, pid int64, id, oldStatus, newStatus string, retired bool) error {
	if newStatus != "closed" || oldStatus == "closed" {
		return nil
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
