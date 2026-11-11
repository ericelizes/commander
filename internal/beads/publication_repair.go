package beads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/store"
)

const PublicationPath = "/api/gate/publish"

type PublicationRequest struct {
	Dir     string    `json:"dir"`
	Gate    string    `json:"gate"`
	Parent  string    `json:"parent"`
	Waiters []string  `json:"waiters,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Dry     bool      `json:"dry,omitempty"`
	Propose bool      `json:"propose,omitempty"`
	Basis   PlanBasis `json:"basis"`
	Env     []string  `json:"env,omitempty"`
}

// PublishViaService reads an optimistic basis, then delegates exactly once.
// A missing/newer service route never falls back to separate local writes.
func PublishViaService(ctx context.Context, dir, gate, parent string, waiters []string, reason string, dry bool) error {
	if !PostgresConfigured() {
		return errors.New("canonical publication requires PostgreSQL")
	}
	req := PublicationRequest{Dir: dir, Gate: gate, Parent: parent, Waiters: slices.Clone(waiters), Reason: reason, Dry: dry, Env: callerEnv()}
	client := New()
	b, e := FreshShow(ctx, client, dir, gate)
	if e != nil {
		return e
	}
	if b == nil {
		return ErrNotFound
	}
	leaf, e := publicationLeaf(*b)
	if e != nil {
		return e
	}
	req.Basis = PlanBasis{JurisdictionRevision: b.JurisdictionRevision, Beads: map[string]int64{gate: b.PlanRevision}}
	ids := append([]string{parent, leaf}, waiters...)
	for _, id := range ids {
		if _, ok := req.Basis.Beads[id]; ok {
			continue
		}
		b, e := FreshShow(ctx, client, dir, id)
		if e != nil {
			return e
		}
		if b == nil {
			return ErrNotFound
		}
		req.Basis.Beads[id] = b.PlanRevision
	}
	result, e := SubmitPublication(ctx, req)
	if e != nil {
		return e
	}
	if result.Exit != 0 {
		return errors.New(result.Error)
	}
	return nil
}

// SubmitPublication sends one typed action to the canonical service. Attribution
// always comes from the calling session rather than saved proposal JSON.
func SubmitPublication(ctx context.Context, req PublicationRequest) (DoorResponse, error) {
	req.Env = callerEnv()
	base, e := os.ReadFile(filepath.Join(brHome(), "api-url"))
	if e != nil {
		return DoorResponse{}, e
	}
	token, e := os.ReadFile(filepath.Join(brHome(), "api-token"))
	if e != nil {
		return DoorResponse{}, e
	}
	raw, e := json.Marshal(req)
	if e != nil {
		return DoorResponse{}, e
	}
	requestCtx, cancel := context.WithTimeout(ctx, doorCallTimeout)
	defer cancel()
	request, e := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(base)), "/")+PublicationPath, bytes.NewReader(raw))
	if e != nil {
		return DoorResponse{}, e
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	response, e := doorHTTP.Do(request)
	if e != nil {
		return DoorResponse{}, fmt.Errorf("publication response unavailable; inspect or repeat the exact command: %w", e)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DoorResponse{}, fmt.Errorf("canonical publication returned HTTP %d; no local repair was run", response.StatusCode)
	}
	var result DoorResponse
	if e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); e != nil {
		return DoorResponse{}, e
	}
	return result, nil
}

// PlanDocument is the saved reviewable payload. Publication and ordinary edits
// are exclusive: publication never executes its internal receipt identity ops.
type PublicationAction struct {
	Gate    string   `json:"gate"`
	Parent  string   `json:"parent"`
	Waiters []string `json:"waiters,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}
type PlanDocument struct {
	Dir         string             `json:"dir"`
	Basis       PlanBasis          `json:"basis"`
	Ops         [][]string         `json:"ops,omitempty"`
	Intent      *DoorIntent        `json:"intent,omitempty"`
	Publication *PublicationAction `json:"publication,omitempty"`
}

func publicationLeaf(b Bead) (string, error) {
	leaf := ""
	count := 0
	for _, label := range b.Labels {
		if value, ok := strings.CutPrefix(label, "gate-of:"); ok {
			leaf = UnescapeLabel(value)
			count++
		}
	}
	if count != 1 || leaf == "" {
		return "", errors.New("publication requires exactly one guarded leaf")
	}
	return leaf, nil
}

type publicationAuthority struct {
	projectID    int64
	gate, parent string
	waiters      []string
	active, dry  bool
	intent       string
}
type publicationAuthorityKey struct{}

func CanonicalPublication(ctx context.Context) bool {
	_, ok := ctx.Value(publicationAuthorityKey{}).(publicationAuthority)
	return ok
}

// ActivatePublicationRepair is called by the compiled gate validator only
// AFTER its full leaf-coverage/cycle preflight, inside the canonical graph
// transaction. This authority is never a JSON field, argv or comment marker.
func ActivatePublicationRepair(ctx context.Context, gate, parent string, waiters []string, intent string) (context.Context, error) {
	value, ok := ctx.Value(publicationAuthorityKey{}).(publicationAuthority)
	if !ok {
		return ctx, nil
	}
	expected, actual := slices.Clone(value.waiters), slices.Clone(waiters)
	slices.Sort(expected)
	slices.Sort(actual)
	if value.gate != gate || value.parent != parent || !slices.Equal(expected, actual) || intent == "" {
		return ctx, errors.New("publication repair differs from its canonical transaction")
	}
	value.active = true
	value.intent = intent
	return context.WithValue(ctx, publicationAuthorityKey{}, value), nil
}
func publicationEdgeAllowed(ctx context.Context, pid int64, from, to string) bool {
	value, ok := ctx.Value(publicationAuthorityKey{}).(publicationAuthority)
	return ok && value.active && value.projectID == pid && value.gate == to && slices.Contains(value.waiters, from)
}
func checkPublicationOperation(ctx context.Context, pid int64, args []string) error {
	value, ok := ctx.Value(publicationAuthorityKey{}).(publicationAuthority)
	if !ok {
		return nil
	}
	if value.dry {
		return errors.New("dry-run publication cannot write")
	}
	if value.projectID != pid {
		return errors.New("publication authority belongs to another project")
	}
	if reflect.DeepEqual(args, []string{"update", value.gate, "--parent", value.parent}) {
		return nil
	}
	if value.active {
		for _, waiter := range value.waiters {
			for _, verb := range []string{"remove", "rm"} {
				if reflect.DeepEqual(args, []string{"dep", verb, waiter, value.gate}) {
					return nil
				}
			}
		}
		if len(args) == 4 && args[0] == "comments" && args[1] == "add" && args[2] == value.gate && (args[3] == value.intent || args[3] == value.intent+" completed") {
			return nil
		}
	}
	return errors.New("publication authority permits only its exact gate placement, redundant waits and audit")
}

// ApplyPublication runs the validator and every audit/edge/parent mutation in
// the same transaction. The callback is trusted compiled gate logic, not data
// accepted from a caller. Existing planning/human/instance guards still apply.
func ApplyPublication(ctx context.Context, st *store.Store, req PublicationRequest, perform func(context.Context, Client) error) DoorResponse {
	fail := func(e error) DoorResponse { return DoorResponse{Exit: 1, Error: e.Error(), Atomic: true} }
	if e := requirePlanningCaller(ctx); e != nil {
		return fail(e)
	}
	if st == nil || !st.IsPostgres() || !PostgresConfigured() {
		return fail(errors.New("publication requires canonical PostgreSQL state"))
	}
	if req.Gate == "" || req.Parent == "" || len(req.Waiters) > 128 || len(req.Reason) > 8000 || req.Basis.JurisdictionRevision < 1 {
		return fail(errors.New("publication requires a gate, parent and bounded current planning basis"))
	}
	if req.Propose && (req.Dry || req.Basis.CoordinationID != "") {
		return fail(errors.New("publication proposal requires an ungranted non-dry action"))
	}
	if perform == nil {
		return fail(errors.New("publication validator unavailable"))
	}
	// This tagged identity is only a receipt/basis key, never a stored comment.
	identity, _ := json.Marshal(struct {
		Gate, Parent, Reason string
		Waiters              []string
		Dry                  bool
	}{req.Gate, req.Parent, req.Reason, req.Waiters, req.Dry})
	ops := [][]string{{"comments", "add", req.Gate, "canonical-publication@v1 " + string(identity)}, {"update", req.Gate, "--parent", req.Parent}}
	for _, waiter := range req.Waiters {
		ops = append(ops, []string{"dep", "remove", waiter, req.Gate})
	}
	ctx = WithPlanRequest(ctx, &req.Basis, ops, nil)
	envelope, _, e := receiptEnvelope(ctx, ops)
	if e != nil {
		return fail(e)
	}
	// A typed mechanical action is not an ordinary batch that writes its tagged
	// identity comment. Bind the endpoint semantics as well as its exact payload.
	digest := sha256.Sum256([]byte("gate-publication@v1:" + envelope.Hash))
	envelope.Hash = hex.EncodeToString(digest[:])
	if req.Basis.RequestID == "" {
		envelope.Key = envelope.Hash
	}

	var result DoorResponse
	e = st.WithTx(ctx, func(q store.Querier) error {
		pid, e := projectID(ctx, q, req.Dir)
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return e
		}
		if req.Propose {
			prior, e := readPublicationProposal(ctx, q, pid, envelope)
			if e != nil {
				return e
			}
			if prior != "" {
				result = DoorResponse{Atomic: true, ProposalID: prior}
				return nil
			}
		}
		if !req.Dry && !req.Propose {
			prior, e := readPlanReceipt(ctx, q, pid, envelope)
			if e != nil {
				return e
			}
			if prior != nil {
				result = *prior
				return nil
			}
		}
		if e = pgCheckPlanBasis(ctx, q, pid, ops); e != nil {
			return e
		}
		authorized := ctx
		if !req.Propose {
			authorized, e = authorizeCoordinationEnvelope(ctx, q, pid, envelope)
			if e != nil {
				return e
			}
		}
		txCtx := context.WithValue(authorized, postgresReaderKey{}, q)
		client := CompletionClient(q, pid)
		b, e := client.Show(txCtx, req.Dir, req.Gate)
		if e != nil {
			return e
		}
		if b == nil {
			return ErrNotFound
		}
		leaf, e := publicationLeaf(*b)
		if e != nil {
			return e
		}
		if req.Basis.Beads[leaf] < 1 {
			return errors.New("publication basis is missing its protected leaf")
		}
		ids := append([]string{req.Gate, req.Parent, leaf}, req.Waiters...)
		for _, id := range ids {
			if req.Propose {
				continue
			}
			if e = pgCheckPlanningScope(txCtx, q, pid, "update", []string{id}, nil); e != nil {
				return e
			}
		}
		var pending bool
		if e = q.QueryRowContext(ctx, `SELECT commander_pending_human_answer(?,?)`, pid, req.Gate).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return errors.New("apply this gate's saved human answer before changing its publication")
		}
		txCtx = context.WithValue(txCtx, publicationAuthorityKey{}, publicationAuthority{projectID: pid, gate: req.Gate, parent: req.Parent, waiters: slices.Clone(req.Waiters), dry: req.Dry || req.Propose})
		if e = perform(txCtx, client); e != nil {
			return e
		}
		if req.Propose {
			id, e := savePublicationProposal(txCtx, st, q, pid, req, envelope)
			if e != nil {
				return e
			}
			result = DoorResponse{Atomic: true, ProposalID: id}
			return nil
		}
		result = DoorResponse{Atomic: true, PlanReceipt: envelope.Key}
		if req.Dry {
			return nil
		}
		return savePlanReceipt(ctx, q, pid, envelope, result)
	})
	if e != nil {
		return fail(e)
	}
	return result
}
