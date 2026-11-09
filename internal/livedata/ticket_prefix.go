package livedata

import (
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/example-user/commander/internal/config"
)

// ticketID matches "(KEY-123)" for the configured ticket prefix only, so a
// title's own "(UTF-8)" or "(SHA-256)" is never taken for a ticket id. nil
// (no prefix configured) strips nothing.
var ticketID atomic.Pointer[regexp.Regexp]

// teamKey is the shape of a ticket prefix (a Linear team key).
var teamKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,5}$`)

// SetTicketPrefix sets the ticket prefix board text drops from titles (a
// Linear team key such as "ENG"; case-insensitive). "" or an invalid key
// drops nothing.
func SetTicketPrefix(key string) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !teamKey.MatchString(key) {
		ticketID.Store(nil)
		return
	}
	ticketID.Store(regexp.MustCompile(`\s*\(` + key + `-\d{1,6}\)\s*`))
}

// LoadTicketPrefix reads [integrations.linear] team from <home>/local.toml.
// A missing or unreadable file, or an unset team, drops nothing.
func LoadTicketPrefix(home string) {
	local, err := config.LoadLocal(filepath.Join(home, "config.toml"))
	if err != nil || local.Integrations.Linear.Team == nil {
		SetTicketPrefix("")
		return
	}
	SetTicketPrefix(*local.Integrations.Linear.Team)
}

// withoutTicketIDs drops the configured ticket ids from t.
func withoutTicketIDs(t string) string {
	if re := ticketID.Load(); re != nil {
		return re.ReplaceAllString(t, " ")
	}
	return t
}
