package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// signedOutAccounts keeps a red health problem for every Claude account a
// Commander or Manager turn found signed out, naming the command that signs it
// in again, until its credentials are rewritten (a new login).
type signedOutAccounts struct {
	mu   sync.Mutex
	home string
	at   map[string]time.Time
	why  map[string]string
}

func (s *signedOutAccounts) failed(role, account, movedTo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at == nil {
		s.at, s.why = map[string]time.Time{}, map[string]string{}
	}
	s.at[account] = time.Now()
	s.why[account] = authCardText(s.home, role, account, movedTo)
}

// authCardText is the card: which account, what happened, the exact login command.
func authCardText(home, role, account, movedTo string) string {
	login := claudeacct.LoginCommand(home, account)
	if movedTo == "" {
		return fmt.Sprintf("Claude account %s is signed out and no other Claude account is logged in, so %s cannot run. Sign it in again in a terminal: %s", account, role, login)
	}
	return fmt.Sprintf("Claude account %s is signed out; %s moved to %s. Sign it in again in a terminal: %s", account, role, movedTo, login)
}

// refresh writes each still signed-out account to the health board, and
// clears the ones whose login was renewed since.
func (s *signedOutAccounts) refresh(h *healthBoard) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for account, at := range s.at {
		key := "claude-signed-out:" + account
		if renewedSince(claudeacct.Dir(s.home, account), at) {
			h.ok(key)
			delete(s.at, account)
			delete(s.why, account)
			continue
		}
		h.fail(key, s.why[account])
	}
}

// renewedSince: the account's credentials file was rewritten after t.
func renewedSince(dir string, t time.Time) bool {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".claude")
	}
	fi, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && fi.ModTime().After(t)
}
