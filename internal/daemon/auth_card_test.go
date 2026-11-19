package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The card names the account and the exact login command; with no account
// left it says so; it clears once the account is signed in again.
func TestSignedOutAccountCard(t *testing.T) {
	home := t.TempDir()
	if s := authCardText(home, "commander", "personal", "default"); !strings.Contains(s, "Claude account personal is signed out; commander moved to default") || !strings.Contains(s, "claude auth login") {
		t.Fatalf("card: %s", s)
	}
	if s := authCardText(home, "commander", "personal", ""); !strings.Contains(s, "no other Claude account is logged in") {
		t.Fatalf("card with none left: %s", s)
	}
	dir := t.TempDir()
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(creds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now()
	old := failedAt.Add(-time.Hour)
	if err := os.Chtimes(creds, old, old); err != nil {
		t.Fatal(err)
	}
	if renewedSince(dir, failedAt) {
		t.Fatal("an old login read as renewed")
	}
	newer := failedAt.Add(time.Minute)
	if err := os.Chtimes(creds, newer, newer); err != nil {
		t.Fatal(err)
	}
	if !renewedSince(dir, failedAt) {
		t.Fatal("a new login was not seen")
	}
}
