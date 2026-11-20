package brief

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/linearapi"
	"github.com/example-user/commander/internal/linearsync"
)

// The writer's read helpers, behind `cmdr internal-brief-read`.

const (
	maxRows = 200
	maxCell = 1500
)

var (
	writeWords = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|create|alter|drop|truncate|grant|revoke|copy|call|lock|vacuum|refresh|reindex|cluster|notify|listen|prepare|execute|into|nextval|setval|pg_sleep|lo_import|lo_export|dblink|pg_read_file|pg_read_binary_file|pg_ls_dir|pg_terminate_backend|pg_cancel_backend|set_config)\b`)
	literals   = regexp.MustCompile(`'(?:[^']|'')*'`)
)

// ReadOnlySQL refuses anything but one SELECT (or WITH ... SELECT) statement
// that names no writing statement or function outside its string literals.
// The query also runs in a read-only transaction on a read-only connection.
func ReadOnlySQL(q string) error {
	t := strings.TrimSpace(q)
	t = strings.TrimSpace(strings.TrimSuffix(t, ";"))
	if t == "" {
		return errors.New("empty query")
	}
	bare := literals.ReplaceAllString(t, "''")
	if strings.Contains(bare, ";") {
		return errors.New("one statement only")
	}
	if strings.Contains(bare, "--") || strings.Contains(bare, "/*") || strings.Contains(bare, "$") {
		return errors.New("no SQL comments or dollar quoting")
	}
	first := strings.ToLower(strings.Fields(t)[0])
	if first != "select" && first != "with" {
		return errors.New("only SELECT queries")
	}
	if m := writeWords.FindString(bare); m != "" {
		return fmt.Errorf("only reads: %q is not allowed", m)
	}
	return nil
}

// QueryRows runs a checked read query and writes its rows as JSON lines: at
// most maxRows rows, each value cut to maxCell characters.
func QueryRows(ctx context.Context, db *sql.DB, q string, w io.Writer) error {
	if err := ReadOnlySQL(q); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, strings.TrimSuffix(strings.TrimSpace(q), ";"))
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		if n == maxRows {
			fmt.Fprintf(w, "(more rows: only the first %d are shown; narrow the query)\n", maxRows)
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := map[string]any{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if s, ok := v.(string); ok {
				v = trim(s, maxCell)
			}
			row[c] = v
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		fmt.Fprintln(w, "(no rows)")
	}
	return rows.Err()
}

var ticketID = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`)

const issueQuery = `query($id: String!) { issue(id: $id) { identifier title description url state { name type } assignee { name }
 startedAt completedAt updatedAt comments(first: 15) { nodes { body createdAt user { name } } } } }`

// ReadIssue is one Linear ticket as text, read with the saved key.
func ReadIssue(ctx context.Context, home, id string, do linearapi.HTTPDo, endpoint string) (string, error) {
	if !ticketID.MatchString(id) {
		return "", errors.New("a ticket id looks like ABC-123")
	}
	key, err := linearsync.ReadKey(home)
	if err != nil || key == "" {
		return "", errors.New("Linear is not connected")
	}
	if endpoint == "" {
		endpoint = linearapi.Endpoint
	}
	payload, _ := json.Marshal(map[string]any{"query": issueQuery, "variables": map[string]string{"id": id}})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, status := linearapi.Query(ctx, do, endpoint, key, payload)
	if status/100 != 2 {
		return "", errors.New("Linear could not be read")
	}
	text := strings.ReplaceAll(string(body), key, "[redacted]")
	var data struct {
		Data struct {
			Issue *struct {
				Identifier, Title, Description, URL string
				StartedAt, CompletedAt, UpdatedAt   string
				State                               struct{ Name, Type string }
				Assignee                            *struct{ Name string }
				Comments                            struct {
					Nodes []struct {
						Body, CreatedAt string
						User            *struct{ Name string }
					}
				}
			}
		}
	}
	if json.Unmarshal([]byte(text), &data) != nil || data.Data.Issue == nil {
		return "", errors.New("no such ticket")
	}
	is := data.Data.Issue
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): %s\n", is.Identifier, is.State.Name, is.Title)
	if is.Assignee != nil {
		fmt.Fprintf(&b, "Assignee: %s\n", is.Assignee.Name)
	}
	fmt.Fprintf(&b, "Started: %s  Completed: %s  Updated: %s\n\n%s\n", is.StartedAt, is.CompletedAt, is.UpdatedAt, trim(is.Description, 4000))
	for _, c := range is.Comments.Nodes {
		who := "someone"
		if c.User != nil {
			who = c.User.Name
		}
		fmt.Fprintf(&b, "\n— %s, %s: %s", who, c.CreatedAt, trim(c.Body, 800))
	}
	return b.String(), nil
}
