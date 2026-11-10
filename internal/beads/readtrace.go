package beads

import (
	"context"
	"sync/atomic"
)

type readCallerKey struct{}
type detailTraceKey struct{}
type detailTrace struct{ batches atomic.Int64 }

// WithReadCaller labels slow detail-read diagnostics. It changes no read policy.
func WithReadCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, readCallerKey{}, caller)
}

func readCaller(ctx context.Context, noExport bool) string {
	if caller, ok := ctx.Value(readCallerKey{}).(string); ok {
		return caller
	}
	if noExport {
		return "incremental"
	}
	return "display"
}
