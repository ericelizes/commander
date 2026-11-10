package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Ordered row digests compare complete common columns without publishing any
// source data. Source/destination row order and SQL driver byte forms may differ.
func migrationDigest(rows *sql.Rows) (string, error) {
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return "", e
	}
	digests := [][32]byte{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range values {
			ptr[i] = &values[i]
		}
		if e = rows.Scan(ptr...); e != nil {
			return "", e
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		raw, e := json.Marshal(values)
		if e != nil {
			return "", e
		}
		digests = append(digests, sha256.Sum256(raw))
	}
	if e = rows.Err(); e != nil {
		return "", e
	}
	sort.Slice(digests, func(i, j int) bool { return string(digests[i][:]) < string(digests[j][:]) })
	h := sha256.New()
	for _, d := range digests {
		h.Write(d[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func verifyMigrationTable(ctx context.Context, src, dst *sql.Tx, table, selection string) error {
	a, e := src.QueryContext(ctx, `SELECT `+selection+` FROM `+table)
	if e != nil {
		return e
	}
	want, e := migrationDigest(a)
	if e != nil {
		return e
	}
	b, e := dst.QueryContext(ctx, `SELECT `+selection+` FROM `+table)
	if e != nil {
		return e
	}
	got, e := migrationDigest(b)
	if e != nil {
		return e
	}
	if got != want {
		return fmt.Errorf("migration content digest differs for %s", table)
	}
	return nil
}
