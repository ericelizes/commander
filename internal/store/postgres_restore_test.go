package store

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresCustomDumpRestoreShape(t *testing.T) {
	var scenario struct {
		WrongOperator string `json:"wrong_operator"`
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/postgres-backup-shape.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.WrongOperator != " AND " {
		t.Fatal("invalid predicate mutation scenario")
	}
	s := postgresTestStore(t)
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("private PostgreSQL restore test requires %s: %v", tool, err)
		}
	}
	u, err := url.Parse(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	schema := q.Get("search_path")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	dump := filepath.Join(t.TempDir(), "schema.dump")
	run := func(tool string, args ...string) {
		t.Helper()
		if out, e := exec.Command(tool, args...).CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", tool, e, out)
		}
	}
	run("pg_dump", "--dbname", u.String(), "--format=custom", "--schema", schema, "--file", dump)
	if _, err = s.DB().Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
		t.Fatal(err)
	}
	run("pg_restore", "--exit-on-error", "--dbname", u.String(), dump)
	opened, err := OpenPostgres(s.postgresDSN)
	if err != nil {
		t.Fatal("restored schema refused:", err)
	}
	opened.Close()
	// Alter a real restored predicate while retaining all the other trigger fields.
	var definition string
	if err = s.DB().QueryRow(`SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid='execution_slots'::regclass AND tgname='execution_slots_update_events'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, " OR ") {
		t.Fatal("fixture lacks OR predicate")
	}
	definition = strings.Replace(definition, " OR ", scenario.WrongOperator, 1)
	if _, err = s.DB().Exec(`DROP TRIGGER execution_slots_update_events ON execution_slots; ` + definition); err != nil {
		t.Fatal(err)
	}
	opened, err = OpenPostgres(s.postgresDSN)
	if err == nil {
		opened.Close()
		t.Fatal("wrong restored predicate accepted")
	}
	if !strings.Contains(err.Error(), "shape mismatch") {
		t.Fatal(err)
	}
}

func TestCanonicalPostgresPredicate(t *testing.T) {
	for _, pair := range [][2]string{{"((a OR b) OR c)", "(a OR b OR c)"}, {"((a AND b) AND c)", "a AND b AND c"}, {"(a OR ('x OR (y)' = z))", "a OR ('x OR (y)' = z)"}} {
		if canonicalPredicate(pair[0]) != canonicalPredicate(pair[1]) {
			t.Fatal(pair)
		}
	}
	for _, pair := range [][2]string{{"(a OR b) AND c", "a OR (b AND c)"}, {"a OR b", "a AND b"}, {"a OR b", "b OR a"}, {"a = '(x)'", "a = 'x'"}} {
		if canonicalPredicate(pair[0]) == canonicalPredicate(pair[1]) {
			t.Fatal("wrong expression equated", pair)
		}
	}
}

func TestPostgresUnknownPredicateRemainsExact(t *testing.T) {
	for _, predicate := range []string{
		"NOT (old.state IS DISTINCT FROM new.state)",
		"old.state BETWEEN 'a' AND 'z'",
		"old.state = ' OR (quoted)'",
	} {
		definition := "CREATE TRIGGER fixture WHEN (" + predicate + ") EXECUTE FUNCTION f()"
		if got := normalizeTriggerPredicate(definition); got != definition {
			t.Fatalf("unknown grammar normalized: %s", got)
		}
	}
}
