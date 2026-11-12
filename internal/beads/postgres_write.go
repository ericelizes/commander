package beads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

type pgFlags map[string][]string

func (f pgFlags) one(k string) string {
	v := f[k]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}
func (f pgFlags) has(k string) bool { _, ok := f[k]; return ok }

func pgCallerValue(ctx context.Context, key string) string {
	if env, ok := ctx.Value(doorEnvKey{}).([]string); ok {
		for _, kv := range env {
			if v, yes := strings.CutPrefix(kv, key+"="); yes {
				return v
			}
		}
		return ""
	}
	return os.Getenv(key)
}

// A planning conversation cannot edit another Commander's jurisdiction.
// Active runtime executors may publish only current-run execution evidence;
// tree edits require planning ownership or an exact coordinated plan grant.
func pgCheckPlanningScope(ctx context.Context, q store.Querier, pid int64, verb string, pos []string, f pgFlags) error {
	if ExecutionAttributed(ctx) {
		active, e := pgActiveExecution(ctx, q)
		if e != nil {
			return e
		}
		if active {
			return pgCheckExecutorWrite(ctx, q, pid, verb, pos, f)
		}
	}
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	if actor == "" {
		return nil
	}
	var planner bool
	if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, actor).Scan(&planner); e != nil {
		return e
	}
	if !planner {
		if actor == "commander" || strings.HasPrefix(actor, "commander-") {
			return errors.New("planning conversation is not registered")
		}
		active, e := pgActiveExecution(ctx, q)
		if e != nil {
			return e
		}
		if active {
			return pgCheckExecutorWrite(ctx, q, pid, verb, pos, f)
		}
		return nil
	}
	check := func(id string) error {
		var owner string
		if e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(?,NULLIF(?,''))`, pid, id).Scan(&owner); e != nil {
			return e
		}
		if owner != actor {
			if grant, _ := ctx.Value(coordinationGrantKey{}).(bool); grant {
				return nil
			}
			return fmt.Errorf("planning change on %s belongs to %s; coordinate the change or transfer jurisdiction", id, owner)
		}
		return nil
	}
	if verb == "create" {
		return check(f.one("--parent"))
	}
	if verb == "dep" {
		if len(pos) == 3 {
			if e := check(pos[1]); e != nil {
				return e
			}
			// A qualified edge mutates the source record only. Its foreign
			// target is resolved and validated in pgQualifiedDep, not here
			// against an unrelated local bead with the same spelling.
			if f.has("--source-project") || f.has("--target-project") {
				return nil
			}
			return check(pos[2])
		}
		return nil
	}
	if verb == "link" {
		for _, id := range pos {
			if e := check(id); e != nil {
				return e
			}
		}
		return nil
	}
	if len(pos) > 0 {
		if e := check(pos[0]); e != nil {
			return e
		}
	}
	if f.has("--parent") {
		return check(f.one("--parent"))
	}
	return nil
}
func parsePGFlags(args []string, allowed string) ([]string, pgFlags, error) {
	pos := []string{}
	f := pgFlags{}
	names := strings.Fields(allowed)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		k, v, equal := strings.Cut(a, "=")
		if !slices.Contains(names, k) {
			return nil, nil, fmt.Errorf("unsupported Postgres bead option %s", k)
		}
		if !equal && slices.Contains([]string{"--force", "--silent", "--no-inherit-labels", "--json", "--all", "--include-comments", "--claim"}, k) {
			v = "true"
		} else if !equal {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("missing value for %s", k)
			}
			i++
			v = args[i]
		}
		f[k] = append(f[k], v)
	}
	return pos, f, nil
}

// postgresRun is the implementation behind the existing write door. Nothing
// executes the legacy Beads binary once Postgres is selected.
func postgresRun(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("missing bead operation")
	}
	if len(args) == 2 && args[0] == "create" && args[1] == "--help" {
		return []byte("--no-inherit-labels"), nil, nil
	}
	if args[0] == "show" {
		pos, _, e := parsePGFlags(args[1:], "--json")
		if e != nil {
			return nil, nil, e
		}
		records := []map[string]any{}
		c := &postgresClient{}
		for _, id := range pos {
			b, e := c.Show(ctx, dir, id)
			if e != nil {
				return nil, nil, e
			}
			details, e := c.Details(ctx, dir, []string{id})
			if e != nil {
				return nil, nil, e
			}
			raw, _ := json.Marshal(b)
			record := map[string]any{}
			if e = json.Unmarshal(raw, &record); e != nil {
				return nil, nil, e
			}
			record["description"] = b.Description
			record["parent"] = b.ParentID
			record["issue_type"] = b.Type
			record["assignee"] = b.AssignedAgent
			record["dependencies"] = details[id].Deps
			record["comments"] = details[id].Comments
			records = append(records, record)
		}
		out, e := json.Marshal(records)
		return out, nil, e
	}
	s, e := postgresStore()
	if e != nil {
		return nil, nil, e
	}
	if args[0] == "init" {
		if e = os.MkdirAll(filepath.Join(dir, ".beads"), 0700); e != nil {
			return nil, nil, e
		}
		return nil, nil, nil
	}
	var out []byte
	if e = validateAutomaticGateOperations(ctx, [][]string{args}); e != nil {
		return nil, nil, e
	}
	e = s.WithTx(ctx, func(q store.Querier) error {
		return pgWithCustody(ctx, q, dir, func() error {
			pid, e := projectID(ctx, q, dir)
			if e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
				return e
			}
			if e = pgCheckPlanBasis(ctx, q, pid, [][]string{args}); e != nil {
				return e
			}
			if e = pgCheckAutomaticGateResult(ctx, q, pid, dir); e != nil {
				return e
			}
			out, e = pgWrite(ctx, q, pid, args)
			return e
		})
	})
	return out, nil, e
}

// Every operation, including its event trigger, runs in the caller's transaction.
func pgWrite(ctx context.Context, q store.Querier, pid int64, args []string) ([]byte, error) {
	if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031823)`); e != nil {
		return nil, e
	}
	if e := checkPublicationOperation(ctx, pid, args); e != nil {
		return nil, e
	}
	if len(args) < 2 {
		return nil, errors.New("missing bead arguments")
	}
	if args[0] == "comments" {
		// The place write door terminates flags before arbitrary report text.
		if len(args) == 5 && args[1] == "add" && args[3] == "--" {
			args = []string{args[0], args[1], args[2], args[4]}
		}
		if len(args) != 4 || args[1] != "add" {
			return nil, errors.New("comments requires add, bead and text")
		}
		var entropy [8]byte
		if _, e := rand.Read(entropy[:]); e != nil {
			return nil, e
		}
		factKind, factDefinition, factValue, factErr := pgAutomaticGateComment(ctx, q, pid, args[2], args[3])
		if factErr != nil {
			return nil, factErr
		}
		if factKind == "" {
			factKind, factDefinition, factValue, factErr = pgReviewAddendumComment(ctx, q, pid, args[2], args[3])
			if factErr != nil {
				return nil, factErr
			}
		}
		if e := pgCheckRuntimeComment(ctx, q, pid, args[2]); e != nil {
			return nil, e
		}
		author := pgCallerValue(ctx, "COMMANDER_ROLE")
		if factKind == "automatic-gate" {
			author = "auto"
		}
		if strings.TrimSpace(args[3]) == "" {
			return nil, errors.New("comment text cannot be empty")
		}
		if author == "" {
			author = "user"
		}
		runText, session := pgCallerValue(ctx, "COMMANDER_RUN_ID"), pgCallerValue(ctx, "COMMANDER_RUN_SESSION")
		rid, _ := strconv.ParseInt(runText, 10, 64)
		var runID any
		if rid > 0 {
			var bound bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=? AND project_id=? AND task_bead_id=? AND opencode_session_id=? AND replace(agent_name,'.','-')=replace(?::text,'.','-'))`, rid, pid, args[2], session, author).Scan(&bound); e != nil {
				return nil, e
			}
			if !bound {
				return nil, errors.New("comment run identity does not match this actor, session and bead")
			}
			runID = rid
		}
		_, e := q.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at,run_id,fact_kind,fact_definition,fact_value) VALUES(?,?,?,?,?,?,?,?,?,?::jsonb)`, pid, args[2], hex.EncodeToString(entropy[:]), author, args[3], time.Now().UTC().Format(time.RFC3339Nano), runID, factKind, factDefinition, factValue)
		if e == nil && rid > 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(args[3])), "final report") {
			var ok bool
			// A place still needs its run token for report retry, heartbeat and
			// physical exit. EndPlaceRun publishes its result atomically with exit.
			e = q.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM place_runs WHERE run_id=? AND state IN('offered','running')) THEN TRUE ELSE commander_report_result(?,?,?,?,?) END`, rid, pid, args[2], rid, session, args[3]).Scan(&ok)
			if e == nil && !ok {
				return nil, errors.New("final report belongs to a replaced run")
			}
		}
		return nil, e
	}
	verb := args[0]
	pos, f, e := parsePGFlags(args[1:], "--title --description --acceptance --notes --status --priority --type --labels --add-label --remove-label --parent --assignee --reason --force --silent --no-inherit-labels --claim --source-project --target-project")
	if e != nil {
		return nil, e
	}
	if len(pos) == 0 {
		return nil, errors.New("missing bead identity")
	}
	if e = pgCheckPlanningScope(ctx, q, pid, verb, pos, f); e != nil {
		return nil, e
	}
	if f.has("--claim") {
		return nil, errors.New("claims are created by Commander dispatch with a run identity")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if verb == "create" {
		if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
			return nil, errors.New("create requires one nonempty title")
		}
		// ID allocation is scoped to this project's transaction, never a global lock.
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return nil, e
		}
		var entropy [6]byte
		if _, e = rand.Read(entropy[:]); e != nil {
			return nil, e
		}
		id := "bead-" + hex.EncodeToString(entropy[:])
		parent := f.one("--parent")
		if parent != "" {
			if e := pgCheckCompletedStructure(ctx, q, pid, parent); e != nil {
				return nil, e
			}
			var exists int
			if e = q.QueryRowContext(ctx, `SELECT 1 FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, pid, parent).Scan(&exists); e != nil {
				return nil, fmt.Errorf("parent unavailable: %w", e)
			}
			for n := 1; ; n++ {
				id = parent + "." + strconv.Itoa(n)
				e = q.QueryRowContext(ctx, `SELECT 1 FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&exists)
				if errors.Is(e, sql.ErrNoRows) {
					break
				}
				if e != nil {
					return nil, e
				}
			}
		}
		labels := []string{}
		if f.has("--labels") {
			labels = strings.Split(f.one("--labels"), ",")
		}
		if pgCallerValue(ctx, "COMMANDER_ROLE") != "" && slices.Contains(labels, DecisionAnsweredLabel) {
			active, err := pgActiveExecution(ctx, q)
			if err != nil {
				return nil, err
			}
			if active {
				return nil, errors.New("a new bead cannot invent a recorded human answer")
			}
		}
		raw, _ := json.Marshal(labels)
		typ := f.one("--type")
		if typ == "" {
			typ = "task"
		}
		priority := 2
		if f.has("--priority") {
			priority, e = strconv.Atoi(f.one("--priority"))
			if e != nil {
				return nil, e
			}
		}
		_, e = q.ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,notes,status,issue_type,priority,labels,parent_id,created_at,updated_at) VALUES(?,?,?,?,?,?,'open',?,?,?::jsonb,NULLIF(?,''),?,?)`, pid, id, pos[0], f.one("--description"), f.one("--acceptance"), f.one("--notes"), typ, priority, string(raw), parent, now, now)
		if e != nil {
			return nil, e
		}
		if parent != "" {
			if _, e = q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,'parent-child')`, pid, id, parent); e != nil {
				return nil, e
			}
		}
		if f.has("--silent") {
			return []byte(id), nil
		}
		return []byte("Created issue: " + id + " — " + pos[0]), nil
	}
	if verb == "dep" || verb == "link" {
		sub := "add"
		from, to := "", ""
		kind := f.one("--type")
		if kind == "" {
			kind = "blocks"
		}
		if verb == "dep" {
			if len(pos) != 3 {
				return nil, errors.New("dependency requires operation and two identities")
			}
			sub, from, to = pos[0], pos[1], pos[2]
		} else {
			if len(pos) != 2 {
				return nil, errors.New("link requires two identities")
			}
			from, to = pos[0], pos[1]
		}
		if kind == "parent-child" {
			return nil, errors.New("parent links must use the guarded parent operation")
		}
		if e := pgCheckCompletedStructure(ctx, q, pid, from); e != nil {
			return nil, e
		}
		if f.has("--source-project") || f.has("--target-project") {
			return pgQualifiedDep(ctx, q, pid, from, to, sub, kind, f)
		}
		var foreign bool
		if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id=? AND depends_on_id=? AND target_project_id<>project_id)`, pid, from, to).Scan(&foreign); e != nil {
			return nil, e
		}
		if foreign {
			return nil, qualifiedFailure("endpoint", from, errors.New("qualified source requires explicit endpoint projects"))
		}
		if sub == "remove" || sub == "rm" {
			if kind == "blocks" {
				if e := pgCheckHumanUnlink(ctx, q, pid, from, to); e != nil {
					return nil, e
				}
				if e := pgCheckWorkflowUnlink(ctx, q, pid, from, to); e != nil {
					return nil, e
				}
			}
			_, e = q.ExecContext(ctx, `DELETE FROM bead_dependencies WHERE project_id=? AND issue_id=? AND depends_on_id=? AND type=?`, pid, from, to, kind)
			return nil, e
		}
		if sub != "add" {
			return nil, errors.New("unsupported dependency operation")
		}
		if kind == "parent-child" {
			return nil, errors.New("parent links must use the guarded parent operation")
		}
		if from == to {
			return nil, errors.New("a bead cannot depend on itself")
		}
		// Concurrent graph edits serialize per project so two individually safe
		// links cannot jointly introduce a cycle.
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return nil, e
		}
		v, err := loadQualifiedView(ctx, q, pid, false)
		if err != nil {
			return nil, err
		}
		if v != nil {
			if err = qualifiedCycle(v.edges, qualifiedNode{pid, from}, qualifiedNode{pid, to}, kind, qualifiedGraphLimit); err != nil {
				return nil, err
			}
		}
		var cycle bool
		e = q.QueryRowContext(ctx, `WITH RECURSIVE reachable(id) AS (SELECT ?::text UNION SELECT d.depends_on_id FROM bead_dependencies d JOIN reachable r ON d.issue_id=r.id WHERE d.project_id=? AND d.type=?) SELECT EXISTS(SELECT 1 FROM reachable WHERE id=?)`, to, pid, kind, from).Scan(&cycle)
		if e != nil {
			return nil, e
		}
		if cycle {
			return nil, errors.New("dependency would introduce a cycle")
		}
		_, e = q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, pid, from, to, kind)
		return nil, e
	}
	if verb == "update" && f.has("--parent") {
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return nil, e
		}
	}
	id := pos[0]
	var labelsRaw []byte
	var parent, oldStatus, oldTitle, oldDescription, oldAcceptance, oldType string
	if e = q.QueryRowContext(ctx, `SELECT labels,COALESCE(parent_id,''),status,title,description,acceptance_criteria,issue_type FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, pid, id).Scan(&labelsRaw, &parent, &oldStatus, &oldTitle, &oldDescription, &oldAcceptance, &oldType); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, e
	}
	labels := []string{}
	if e = json.Unmarshal(labelsRaw, &labels); e != nil {
		return nil, e
	}
	if e = pgCheckWorkflowMutation(ctx, q, pid, Bead{ID: id, Title: oldTitle, Description: oldDescription, AcceptanceCriteria: oldAcceptance, Type: oldType, Status: oldStatus, ParentID: parent, Labels: labels}, verb, pos, f); e != nil {
		return nil, e
	}
	beforeLabels := slices.Clone(labels)
	newStatus := oldStatus
	sets := []string{"updated_at=?"}
	values := []any{now}
	set := func(col string, value any) { sets = append(sets, col+"=?"); values = append(values, value) }
	switch verb {
	case "update":
		if len(pos) != 1 {
			return nil, errors.New("update requires one bead")
		}
		for _, pair := range [][2]string{{"--title", "title"}, {"--description", "description"}, {"--acceptance", "acceptance_criteria"}, {"--notes", "notes"}, {"--status", "status"}, {"--assignee", "assigned_agent"}, {"--type", "issue_type"}} {
			if f.has(pair[0]) {
				v := f.one(pair[0])
				if pair[0] == "--title" && strings.TrimSpace(v) == "" {
					return nil, errors.New("title cannot be empty")
				}
				if pair[0] == "--status" && !slices.Contains([]string{"open", "in_progress", "blocked", "deferred", "closed"}, v) {
					return nil, errors.New("invalid bead status")
				}
				if pair[0] == "--status" {
					newStatus = v
				}
				set(pair[1], v)
			}
		}
		if f.has("--priority") {
			n, e := strconv.Atoi(f.one("--priority"))
			if e != nil || n < 0 || n > 4 {
				return nil, errors.New("invalid priority")
			}
			set("priority", n)
		}
		if f.has("--labels") {
			labels = strings.Split(f.one("--labels"), ",")
		}
		for _, l := range f["--remove-label"] {
			labels = slices.DeleteFunc(labels, func(v string) bool { return v == l })
		}
		for _, l := range f["--add-label"] {
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
		if f.has("--parent") {
			newParent := f.one("--parent")
			if newParent == id {
				return nil, errors.New("bead cannot parent itself")
			}
			if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
				return nil, e
			}
			if newParent != "" {
				var cycle bool
				e = q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (SELECT ?::text UNION SELECT b.parent_id FROM bead_tasks b JOIN ancestors a ON b.id=a.id WHERE b.project_id=? AND b.parent_id IS NOT NULL) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=?)`, newParent, pid, id).Scan(&cycle)
				if e != nil {
					return nil, e
				}
				if cycle {
					return nil, errors.New("parent change would introduce a cycle")
				}
			}
			sets = append(sets, "parent_id=NULLIF(?,'')")
			values = append(values, newParent)
			if _, e = q.ExecContext(ctx, `DELETE FROM bead_dependencies WHERE project_id=? AND issue_id=? AND type='parent-child'`, pid, id); e != nil {
				return nil, e
			}
			if newParent != "" {
				if _, e = q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,'parent-child')`, pid, id, newParent); e != nil {
					return nil, e
				}
			}
		}
	case "close":
		newStatus = "closed"
		set("status", "closed")
		set("close_reason", f.one("--reason"))
	case "delete":
		set("deleted_at", time.Now().Unix())
		set("close_reason", f.one("--reason"))
	case "set-state":
		if len(pos) != 2 {
			return nil, errors.New("set-state requires dimension=value")
		}
		dim, value, ok := strings.Cut(pos[1], "=")
		if !ok || dim == "" || value == "" {
			return nil, errors.New("invalid state")
		}
		labels = slices.DeleteFunc(labels, func(l string) bool { return strings.HasPrefix(l, dim+":") })
		labels = append(labels, dim+":"+value)
	default:
		return nil, fmt.Errorf("unsupported Postgres bead operation %s", verb)
	}
	if e := pgCheckHumanMutation(ctx, q, pid, id, verb, oldStatus, newStatus, beforeLabels, labels, f.has("--parent") && f.one("--parent") != parent, f); e != nil {
		return nil, e
	}
	if e := pgCheckCompletion(ctx, q, pid, id, oldStatus, newStatus, slices.Contains(labels, "retired") || slices.Contains(labels, "superseded")); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(labels)
	if e != nil {
		return nil, e
	}
	sets = append(sets, "labels=?::jsonb")
	values = append(values, string(raw), pid, id)
	_, e = q.ExecContext(ctx, `UPDATE bead_tasks SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, values...)
	return nil, e
}

func postgresApplyOps(ctx context.Context, dir string, ops [][]string) DoorResponse {
	if planBasisOf(ctx) != nil {
		if e := requirePlanningCaller(ctx); e != nil {
			return DoorResponse{Exit: 1, Error: e.Error(), Atomic: true}
		}
	}
	if e := validateAutomaticGateOperations(ctx, ops); e != nil {
		return DoorResponse{Exit: 1, Error: e.Error(), Atomic: true}
	}
	s, e := postgresStore()
	if e != nil {
		return DoorResponse{Exit: 1, Error: e.Error()}
	}
	var outputs []string
	var response DoorResponse
	envelope, record, err := receiptEnvelope(ctx, ops)
	if err != nil {
		return DoorResponse{Exit: 1, Error: err.Error(), Atomic: true}
	}
	e = s.WithTx(ctx, func(q store.Querier) error {
		return pgWithCustody(ctx, q, dir, func() error {
			pid, e := projectID(ctx, q, dir)
			if e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
				return e
			}
			if record {
				prior, err := readPlanReceipt(ctx, q, pid, envelope)
				if err != nil {
					return err
				}
				if prior != nil {
					response = *prior
					return nil
				}
			}
			if e = pgCheckPlanBasis(ctx, q, pid, ops); e != nil {
				return e
			}
			if e = pgCheckAutomaticGateResult(ctx, q, pid, dir); e != nil {
				return e
			}
			if AutomaticGateUnchanged(ctx) {
				response = DoorResponse{Atomic: true}
				return nil
			}
			var err error
			ctx, err = authorizeCoordinatedPlan(ctx, q, pid, ops)
			if err != nil {
				return err
			}
			aliases := map[string]string{}
			writeCtx := context.WithValue(ctx, postgresReaderKey{}, q)
			for _, op := range ops {
				ref := ""
				if record {
					var err error
					op, ref, err = resolvePlanOperation(op, aliases)
					if err != nil {
						return err
					}
					if validator, ok := ctx.Value(planValidatorKey{}).(PlanValidator); ok {
						if err = validator(writeCtx, [][]string{op}); err != nil {
							return err
						}
					}
				}
				out, e := pgWrite(writeCtx, q, pid, op)
				if e != nil {
					return e
				}
				if ref != "" {
					aliases[ref] = strings.TrimSpace(string(out))
				}
				if len(out) > 0 {
					outputs = append(outputs, string(out))
				}
			}
			response = DoorResponse{Stdout: strings.Join(outputs, "\n"), Applied: len(ops), Atomic: true}
			if len(aliases) > 0 {
				response.Created = aliases
			}
			if record {
				response.PlanReceipt = envelope.Key
				return savePlanReceipt(ctx, q, pid, envelope, response)
			}
			return nil
		})
	})
	if e != nil {
		return DoorResponse{Exit: 1, Error: e.Error(), Atomic: true}
	}
	return response
}
