// Package migrations bundles the SQL migration files into the cmdr binary
// via embed.FS so the executable is self-contained.
package migrations

import "embed"

// FS holds every *.sql file in this directory, ordered lexically by filename.
// Files are named NNNN_description.sql; NNNN is the schema version they bring
// the database up to. Migration N requires PRAGMA user_version == N-1 to run.
//
//go:embed *.sql
var FS embed.FS

// LatestVersion is the highest schema version this binary knows how to apply.
// Bump this whenever a new migration file lands. Open() refuses to run against
// a database with PRAGMA user_version > LatestVersion (a loud schema drift error).
const LatestVersion = 22
