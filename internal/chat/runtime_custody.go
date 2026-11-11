package chat

import "context"

// PreserveRuntime prevents legacy cleanup from taking custody of a live run.
// It includes raw legacy runs and ambiguous role bindings, not only native ones.
func (r StoreResolver) PreserveRuntime(ctx context.Context, role string) (bool, error) {
	if r.Store == nil || !r.Store.IsPostgres() {
		return false, nil
	}
	var running bool
	err := r.Store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE status='running' AND replace(agent_name,'.','-')=replace(? ,'.','-'))`, role).Scan(&running)
	return running, err
}

func (e *Engine) preserveRuntime(ctx context.Context, role string) bool {
	if guard, ok := e.opts.Resolver.(interface {
		PreserveRuntime(context.Context, string) (bool, error)
	}); ok {
		keep, err := guard.PreserveRuntime(ctx, role)
		if err != nil {
			e.opts.Logf("chat: preserving uncertain runtime custody for %s: %v", role, err)
		}
		return keep || err != nil
	}
	spec, err := e.opts.Resolver.Resolve(ctx, role)
	return err != nil || spec.ExecutionRunID > 0
}
