// Package place is a governed Operator on a paired device (the user's Mac):
// the place registry's rules, pairing codes, the device challenge, run tokens,
// liveness and the run service cmdrd serves under /api/places.
//
// The server never runs a command on a place. It offers runs by id; the app on
// the Mac fetches what it needs, asks the user in a native dialog and launches
// claude from a template compiled into the app. Design:
// docs/plans/2026-11-09-mac-operator.md.
package place

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// LabelPrefix is the step label that names a place: run-on:mac:<name>.
const LabelPrefix = "run-on:"

var nameRE = regexp.MustCompile(`^mac:[a-z0-9][a-z0-9-]{0,30}$`)

// ValidName reports whether name is a place name: "mac:" and a short slug.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Label is the step label for a place.
func Label(name string) string { return LabelPrefix + name }

// FromLabels returns the place a step's labels name, "" when none. More than
// one run-on label is an error: a step runs in one place.
func FromLabels(labels []string) (string, error) {
	var found string
	for _, l := range labels {
		name, ok := strings.CutPrefix(l, LabelPrefix)
		if !ok {
			continue
		}
		if found != "" && found != name {
			return "", fmt.Errorf("step names two places (%s, %s)", found, name)
		}
		found = name
	}
	return found, nil
}

// RefuseAgent is the user-only rule of the pairing verbs: any caller with an
// agent role is refused (a speed bump, as every role check: the same OS user
// can unset it).
func RefuseAgent(getenv func(string) string) error {
	for _, k := range []string{"COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"} {
		if v := getenv(k); v != "" {
			return fmt.Errorf("refused: pairing a place is the user's act in their own terminal (%s=%s is set)", k, v)
		}
	}
	return nil
}

// --- pairing codes -------------------------------------------------------

// PairingTTL bounds how long a code from the app waits for `cmdr place pair`.
const PairingTTL = 10 * time.Minute

type pending struct {
	PublicKey string `json:"public_key"`
	Created   int64  `json:"created"`
}

const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

func pairingDir(home string) string { return filepath.Join(home, "place-pairing") }

// RequestPairing records a device's public key under a fresh one-time code
// (the app shows it; the user types it into `cmdr place pair`).
func RequestPairing(home, publicKey string, now time.Time) (string, error) {
	if _, err := ParsePublicKey(publicKey); err != nil {
		return "", err
	}
	dir := pairingDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := make([]byte, len(buf))
	for i, b := range buf {
		code[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	b, _ := json.Marshal(pending{PublicKey: publicKey, Created: now.Unix()})
	path := filepath.Join(dir, string(code))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return string(code), nil
}

// ErrNoPairing is a code that is unknown, used or expired.
var ErrNoPairing = errors.New("no such pairing code (unknown, used or expired): ask the app for a new one")

// TakePairing returns the public key a code holds and removes the code: a code
// pairs once.
func TakePairing(home, code string, now time.Time) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || strings.Trim(code, codeAlphabet) != "" {
		return "", ErrNoPairing
	}
	path := filepath.Join(pairingDir(home), code)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ErrNoPairing
	}
	_ = os.Remove(path)
	var p pending
	if json.Unmarshal(b, &p) != nil || now.Sub(time.Unix(p.Created, 0)) > PairingTTL {
		return "", ErrNoPairing
	}
	return p.PublicKey, nil
}

// --- device challenge ----------------------------------------------------

// challengeContext binds a signature to this use, so a device signature made
// for anything else cannot pose as a hello.
const challengeContext = "commander-place-hello-v1\n"

// NewNonce is a fresh server nonce for one connection.
func NewNonce() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("not an Ed25519 public key (base64, 32 bytes)")
	}
	return ed25519.PublicKey(raw), nil
}

// ChallengeMessage is what the device signs: the nonce and its own public key
// (base64), under a fixed context. The app knows its key, not the name the
// user typed at pairing; cmdrd finds the place by the key.
func ChallengeMessage(nonce, publicKey string) []byte {
	return []byte(challengeContext + publicKey + "\n" + nonce)
}

// SignChallenge is the device side (the app does the same in main; tests and
// the fake place use this).
func SignChallenge(priv ed25519.PrivateKey, nonce, publicKey string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ChallengeMessage(nonce, publicKey)))
}

// VerifyChallenge checks a hello's signature with the key it names.
func VerifyChallenge(publicKey, nonce, sigB64 string) bool {
	pub, err := ParsePublicKey(publicKey)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, ChallengeMessage(nonce, publicKey), sig)
}

// --- run tokens ----------------------------------------------------------

// TokenTTL bounds a run token: at most this long after the run was accepted.
const TokenTTL = 12 * time.Hour

// NewToken returns a run token and the hash cmdrd keeps (never the token).
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// BriefSHA is the hash an offer carries over the brief the dialog shows.
func BriefSHA(brief string) string {
	sum := sha256.Sum256([]byte(brief))
	return hex.EncodeToString(sum[:])
}

// BuildRevision is this binary's VCS revision ("-dirty" when modified), the
// value a Mac's cmdr and cmdrd must share. "" when the build carries none.
func BuildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}
