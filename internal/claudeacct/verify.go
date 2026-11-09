package claudeacct

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// Verification contains only public identity fields, never raw CLI output.
type Verification struct {
	LoggedIn    bool   `json:"logged_in"`
	Email       string `json:"email,omitempty"`
	AccountUUID string `json:"account_uuid,omitempty"`
	Matches     bool   `json:"matches"`
}

// Verify asks Claude for this profile's identity without changing authentication.
func Verify(ctx context.Context, home, name string) (Verification, error) {
	if !Exists(home, name) {
		return Verification{}, errors.New("no such Claude account")
	}
	cmd := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") && !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, Env(home, name)...)
	raw, err := cmd.Output()
	if err != nil {
		return Verification{}, errors.New("Claude identity check failed")
	}
	var status struct {
		LoggedIn    bool   `json:"loggedIn"`
		Email       string `json:"email"`
		AccountUUID string `json:"accountUuid"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return Verification{}, errors.New("Claude identity check returned invalid JSON")
	}
	a := Get(home, name)
	matches := status.LoggedIn && a.LoggedIn && identityAgrees(status.Email, status.AccountUUID, a)
	return Verification{LoggedIn: status.LoggedIn, Email: status.Email, AccountUUID: status.AccountUUID, Matches: matches}, nil
}

// identityAgrees holds when every field both sides know is equal and at least
// one is known: a missing email does not undo matching UUIDs.
func identityAgrees(email, uuid string, a Account) bool {
	email, profile := strings.TrimSpace(email), strings.TrimSpace(a.Email)
	uuidKnown, emailKnown := uuid != "" && a.AccountUUID != "", email != "" && profile != ""
	if uuidKnown && uuid != a.AccountUUID {
		return false
	}
	if emailKnown && !strings.EqualFold(email, profile) {
		return false
	}
	return uuidKnown || emailKnown
}
