package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Every pooled connection waits on a busy db and enforces foreign keys, not
// only the one Open happened to use.
func TestEveryConnectionGetsBusyTimeoutAndForeignKeys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var conns []interface{ Close() error }
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		c, err := s.DB().Conn(ctx) // held open, so each is a distinct connection
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var busy, fk int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if busy < 5000 || fk != 1 {
			t.Fatalf("connection %d: busy_timeout=%d foreign_keys=%d", i, busy, fk)
		}
	}
}
