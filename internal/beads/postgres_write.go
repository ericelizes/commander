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
	e = s.WithTx(ctx, func(q store.Querier) error {
		pid, e := projectID(ctx, q, dir)
		if e != nil {
			return e
		}
		out, e = pgWrite(ctx, q, pid, args)
		return e
	})
	return out, nil, e
}

// Every operation, including its event trigger, runs in the caller's transaction.
func pgWrite(ctx context.Context, q store.Querier, pid int64, args []string) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("missing bead arguments")
	}
	verb := args[0]
	pos, f, e := parsePGFlags(args[1:], "--title --description --acceptance --notes --status --priority --type --labels --add-label --remove-label --parent --assignee --reason --force --silent --no-inherit-labels --claim")
	if e != nil {
		return nil, e
	}
	if len(pos) == 0 {
		return nil, errors.New("missing bead identity")
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
		if sub == "remove" || sub == "rm" {
			_, e = q.ExecContext(ctx, `DELETE FROM bead_dependencies WHERE project_id=? AND issue_id=? AND depends_on_id=? AND type <> 'parent-child'`, pid, from, to)
			return nil, e
		}
		if sub != "add" {
			return nil, errors.New("unsupported dependency operation")
		}
		if from == to {
			return nil, errors.New("a bead cannot depend on itself")
		}
		// Concurrent graph edits serialize per project so two individually safe
		// links cannot jointly introduce a cycle.
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return nil, e
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
	if verb == "comments" {
		if len(pos) != 3 || pos[0] != "add" || strings.TrimSpace(pos[2]) == "" {
			return nil, errors.New("comments add requires a bead and text")
		}
		var entropy [12]byte
		if _, e = rand.Read(entropy[:]); e != nil {
			return nil, e
		}
		author := os.Getenv("COMMANDER_ROLE")
		if env, _ := ctx.Value(doorEnvKey{}).([]string); len(env) > 0 {
			for _, kv := range env {
				if strings.HasPrefix(kv, "COMMANDER_ROLE=") {
					author = strings.TrimPrefix(kv, "COMMANDER_ROLE=")
				}
			}
		}
		if author == "" {
			author = "user"
		}
		_, e = q.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,?,?,?,?)`, pid, pos[1], hex.EncodeToString(entropy[:]), author, pos[2], now)
		return nil, e
	}
	id := pos[0]
	var labelsRaw []byte
	var parent string
	if e = q.QueryRowContext(ctx, `SELECT labels,COALESCE(parent_id,'') FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, pid, id).Scan(&labelsRaw, &parent); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, e
	}
	labels := []string{}
	if e = json.Unmarshal(labelsRaw, &labels); e != nil {
		return nil, e
	}
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
	s, e := postgresStore()
	if e != nil {
		return DoorResponse{Exit: 1, Error: e.Error()}
	}
	var outputs []string
	e = s.WithTx(ctx, func(q store.Querier) error {
		pid, e := projectID(ctx, q, dir)
		if e != nil {
			return e
		}
		for _, op := range ops {
			out, e := pgWrite(ctx, q, pid, op)
			if e != nil {
				return e
			}
			if len(out) > 0 {
				outputs = append(outputs, string(out))
			}
		}
		return nil
	})
	if e != nil {
		return DoorResponse{Exit: 1, Error: e.Error(), Atomic: true}
	}
	return DoorResponse{Stdout: strings.Join(outputs, "\n"), Applied: len(ops), Atomic: true}
}
