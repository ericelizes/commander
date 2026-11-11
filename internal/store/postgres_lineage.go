package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

// These catalog fingerprints describe the immutable runtime lineage (1-3),
// then additive places (4) and qualified custody (5). They attest definitions,
// not just object names or the unqualified version marker.
//
//go:embed schema_shapes/*.json
var postgresShapes embed.FS

const postgresCatalogSQL = `SELECT kind,identity,definition FROM (
 SELECT 'column' kind,table_name||'.'||column_name identity,data_type||':'||is_nullable||':'||COALESCE(column_default,'') definition FROM information_schema.columns WHERE table_schema=current_schema() AND table_name<>'commander_schema'
 UNION ALL SELECT 'generation',table_name||'.'||column_name,COALESCE(generation_expression,'')||':'||COALESCE(identity_generation,'') FROM information_schema.columns WHERE table_schema=current_schema() AND (is_generated='ALWAYS' OR is_identity='YES') AND table_name<>'commander_schema'
 UNION ALL SELECT 'constraint',c.relname||'.'||x.conname,pg_get_constraintdef(x.oid) FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname<>'commander_schema'
 UNION ALL SELECT 'index',indexname,indexdef FROM pg_indexes WHERE schemaname=current_schema() AND tablename<>'commander_schema'
 UNION ALL SELECT 'trigger',c.relname||'.'||tgname,pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
 UNION ALL SELECT 'trigger-state',c.relname||'.'||tgname,t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
 UNION ALL SELECT 'index-state',c.relname,i.indisvalid::text||':'||i.indisready::text FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_class tbl ON tbl.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND tbl.relname<>'commander_schema'
 UNION ALL SELECT 'function',p.proname||'('||pg_get_function_identity_arguments(p.oid)||')',pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema()
 ) x ORDER BY kind,identity`

func postgresCatalog(ctx context.Context, q Querier) (map[string]string, error) {
	var schema string
	if err := q.QueryRowContext(ctx, `SELECT quote_ident(current_schema())`).Scan(&schema); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, postgresCatalogSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, id, definition string
		if err = rows.Scan(&kind, &id, &definition); err != nil {
			return nil, err
		}
		definition = normalizePostgresDefinition(kind, definition, schema)
		out[kind+":"+id] = fmt.Sprintf("%x", sha256.Sum256([]byte(definition)))
	}
	return out, rows.Err()
}

// Normalize only catalog-rendered namespace positions, never SQL body aliases
// or literals that happen to contain a short schema name (such as "b").
func normalizePostgresDefinition(kind, definition, quotedSchema string) string {
	switch kind {
	case "function":
		return strings.Replace(definition, "FUNCTION "+quotedSchema+".", "FUNCTION fixture.", 1)
	case "trigger":
		definition = normalizeTriggerPredicate(definition)
		fallthrough
	case "index":
		return strings.Replace(definition, " ON "+quotedSchema+".", " ON fixture.", 1)
	}
	return definition
}

func validatePostgresLineage(ctx context.Context, q Querier, version int) error {
	actual, err := postgresCatalog(ctx, q)
	if err != nil {
		return fmt.Errorf("store: inspect PostgreSQL schema %d: %w", version, err)
	}
	// The old reviewed candidate b6a345cb reserved 2 for places and 3 for
	// qualified custody. Refuse before any DDL or version update. Recovery must
	// explicitly preserve its data into the canonical runtime lineage.
	if version == 2 || version == 3 {
		if _, ok := actual["column:places.name"]; ok {
			return fmt.Errorf("store: incompatible PostgreSQL historical b6a345cb places/custody lineage recorded as schema %d; canonical runtime schemas 2/3 are required; restore to a private recovery database and explicitly reconcile before retrying", version)
		}
	}
	raw, err := postgresShapes.ReadFile(fmt.Sprintf("schema_shapes/%d.json", version))
	if err != nil {
		return err
	}
	var expected map[string]string
	if err = json.Unmarshal(raw, &expected); err != nil {
		return err
	}
	for key, want := range expected {
		if actual[key] != want {
			return fmt.Errorf("store: PostgreSQL schema %d shape mismatch at %s; recorded version alone is insufficient; explicit recovery required", version, key)
		}
	}
	// Derive managed tables from the attested columns. Extra objects on other
	// tables may coexist, but additions to a managed table change its shape.
	managed := map[string]bool{}
	for key := range expected {
		if strings.HasPrefix(key, "column:") {
			table, _, _ := strings.Cut(strings.TrimPrefix(key, "column:"), ".")
			managed[table] = true
		}
	}
	for key := range actual {
		if _, ok := expected[key]; ok {
			continue
		}
		kind, identity, _ := strings.Cut(key, ":")
		table, _, _ := strings.Cut(identity, ".")
		switch kind {
		case "column", "generation", "constraint", "trigger", "trigger-state":
			if managed[table] {
				return fmt.Errorf("store: PostgreSQL schema %d shape mismatch at unexpected %s; explicit recovery required", version, key)
			}
		}
	}
	return nil
}
