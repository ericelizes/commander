package beads

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/example-user/commander/internal/store"
)

type planEnvelope struct{ Key, Hash string }
type planEnvelopeKey struct{}

// Preserve original intent and operation order for replay identity even when
// holding checks reorder the operations before applying them.
func WithPlanRequest(ctx context.Context, b *PlanBasis, ops [][]string, intent *DoorIntent) context.Context {
	ctx = WithPlanBasis(ctx, b)
	if b == nil {
		return ctx
	}
	copyBasis := *b
	copyBasis.CoordinationID = ""
	raw, _ := json.Marshal(struct {
		Basis  *PlanBasis
		Ops    [][]string
		Intent *DoorIntent
	}{&copyBasis, ops, intent})
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	key := b.RequestID
	if key == "" {
		key = hash
	}
	return context.WithValue(ctx, planEnvelopeKey{}, planEnvelope{Key: key, Hash: hash})
}
func receiptEnvelope(ctx context.Context, ops [][]string) (planEnvelope, bool, error) {
	b := planBasisOf(ctx)
	if b == nil {
		return planEnvelope{}, false, nil
	}
	v, ok := ctx.Value(planEnvelopeKey{}).(planEnvelope)
	if !ok {
		v, _ = WithPlanRequest(ctx, b, ops, nil).Value(planEnvelopeKey{}).(planEnvelope)
	}
	if len(v.Key) > 128 || strings.TrimSpace(v.Key) == "" {
		return v, true, errors.New("plan request_id must be nonempty and at most 128 bytes")
	}
	return v, true, nil
}
func readPlanReceipt(ctx context.Context, q store.Querier, pid int64, v planEnvelope) (*DoorResponse, error) {
	var hash string
	var raw []byte
	e := q.QueryRowContext(ctx, `SELECT payload_hash,response FROM commander_plan_receipts WHERE project_id=? AND actor=? AND request_key=?`, pid, pgCallerValue(ctx, "COMMANDER_ROLE"), v.Key).Scan(&hash, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if hash != v.Hash {
		return nil, errors.New("plan request_id was reused with different intent")
	}
	var result DoorResponse
	if e = json.Unmarshal(raw, &result); e != nil {
		return nil, e
	}
	result.Replayed = true
	return &result, nil
}

// LookupPlanReceipt is read-only. A replay returns the original committed
// result even if the plan has since advanced or ownership has transferred.
func LookupPlanReceipt(ctx context.Context, dir string, ops [][]string) (*DoorResponse, error) {
	if e := requirePlanningCaller(ctx); e != nil {
		return nil, e
	}
	v, on, e := receiptEnvelope(ctx, ops)
	if e != nil || !on {
		return nil, e
	}
	s, e := postgresStore()
	if e != nil {
		return nil, e
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		return nil, e
	}
	return readPlanReceipt(ctx, s.DB(), pid, v)
}
func savePlanReceipt(ctx context.Context, q store.Querier, pid int64, v planEnvelope, result DoorResponse) error {
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO commander_plan_receipts(project_id,actor,request_key,payload_hash,response) VALUES(?,?,?,?,?::jsonb)`, pid, pgCallerValue(ctx, "COMMANDER_ROLE"), v.Key, v.Hash, string(raw))
	return e
}
