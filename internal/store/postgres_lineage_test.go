package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestPostgresHistoricalNumberingRefused(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, schema := composedFixture(t, 1)
			// Exact b6a345cb SQL, independently guarded by the reserved SQL hash test.
			if _, err := s.DB().Exec(postgresPlacesSchema); err != nil {
				t.Fatal(err)
			}
			if version == 3 {
				if _, err := s.DB().Exec(func() string {
					raw, err := os.ReadFile("testdata/historical_b6a345cb/qualified_3.sql")
					if err != nil {
						t.Fatal(err)
					}
					return string(raw)
				}()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.DB().Exec(fmt.Sprintf("UPDATE commander_schema SET version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if err := s.AddPlace(context.Background(), Place{Name: "mac:historical", Kind: "mac", PublicKey: "retained", PairedBy: "user", PairedAt: 1}); err != nil {
				t.Fatal(err)
			}
			before := composedShape(t, s, schema)
			var eventsBefore int
			if err := s.DB().QueryRow(`SELECT count(*) FROM state_events`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenPostgres(s.postgresDSN)
			if err == nil {
				opened.Close()
				t.Fatal("historical numbering trusted")
			}
			if !strings.Contains(err.Error(), "historical b6a345cb") {
				t.Fatal(err)
			}
			var got, eventsAfter int
			var key string
			if err = s.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&got); err != nil || got != version {
				t.Fatal(got, err)
			}
			if err = s.DB().QueryRow(`SELECT public_key FROM places WHERE name='mac:historical'`).Scan(&key); err != nil || key != "retained" {
				t.Fatal(key, err)
			}
			if err = s.DB().QueryRow(`SELECT count(*) FROM state_events`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
				t.Fatal(eventsAfter, eventsBefore, err)
			}
			if after := composedShape(t, s, schema); after != before {
				t.Fatal("refusal mutated catalog")
			}
		})
	}
}

func TestPostgresRecordedVersionNeedsShape(t *testing.T) {
	cases := []struct {
		name    string
		version int
		damage  string
	}{
		{"runtime-column", 2, `ALTER TABLE bead_tasks DROP COLUMN execution_ready`},
		{"runtime-function", 3, `CREATE OR REPLACE FUNCTION commander_identity_owns(role TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS 'SELECT true'`},
		{"places-index", 4, `DROP INDEX place_runs_one_active`},
		{"qualified-fk", 5, `ALTER TABLE bead_dependencies DROP CONSTRAINT bead_dependencies_target_fkey`},
		{"qualified-trigger", 5, `DROP TRIGGER qualified_tasks_lock ON bead_tasks`},
		{"disabled-trigger", 5, `ALTER TABLE bead_tasks DISABLE TRIGGER qualified_tasks_lock`},
		{"generated-expression", 3, `ALTER TABLE bead_questions ALTER COLUMN canonical_bead_id DROP EXPRESSION`},
		{"identity", 2, `ALTER TABLE jurisdiction_history ALTER COLUMN id DROP IDENTITY`},
		{"qualified-column", 5, `ALTER TABLE bead_dependencies ALTER COLUMN target_project_id DROP NOT NULL`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, schema := composedFixture(t, c.version)
			if _, err := s.DB().Exec(c.damage); err != nil {
				t.Fatal(err)
			}
			before := composedShape(t, s, schema)
			err := s.migratePostgres(context.Background())
			if err == nil || !strings.Contains(err.Error(), "shape mismatch") {
				t.Fatal("wrong shape accepted", err)
			}
			var got int
			if err = s.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&got); err != nil || got != c.version {
				t.Fatal(got, err)
			}
			if composedShape(t, s, schema) != before {
				t.Fatal("refusal changed catalog")
			}
		})
	}
}

func TestPostgresCatalogNamespaceNormalization(t *testing.T) {
	got := normalizePostgresDefinition("function", "CREATE OR REPLACE FUNCTION b.fn() RETURNS bigint AS 'SELECT b.id FROM bead_tasks b'", "b")
	if got != "CREATE OR REPLACE FUNCTION fixture.fn() RETURNS bigint AS 'SELECT b.id FROM bead_tasks b'" {
		t.Fatal(got)
	}
	got = normalizePostgresDefinition("index", `CREATE INDEX idx ON "odd schema".bead_tasks USING btree (id)`, `"odd schema"`)
	if got != "CREATE INDEX idx ON fixture.bead_tasks USING btree (id)" {
		t.Fatal(got)
	}
}
