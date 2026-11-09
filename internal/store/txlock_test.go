package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A transaction that reads, then writes after another connection has written,
// must wait its turn rather than fail with SQLITE_BUSY ("database is locked",
// seen closing an inspector tab while agents wrote).
func TestTransactionWaitsForAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	if _, err := a.db.Exec(`CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := b.db.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatalf("write in the transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the other writer: %v", err)
	}
}
