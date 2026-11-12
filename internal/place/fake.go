package place

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"sync"

	"github.com/example-user/commander/internal/agentmail"
)

// Device is a place's key pair as the app holds it. Tests and the eval's fake
// Mac use it; the app keeps its private key in the Keychain.
type Device struct {
	Place string
	Pub   string
	Priv  ed25519.PrivateKey
}

// NewDevice makes a fresh key pair for place.
func NewDevice(place string) Device {
	pub, priv, _ := ed25519.GenerateKey(nil)
	return Device{Place: place, Pub: base64.StdEncoding.EncodeToString(pub), Priv: priv}
}

// Hello is the device's hello for nonce.
func (d Device) Hello(nonce, cmdrVersion string) Message {
	return Message{Type: "hello", PublicKey: d.Pub, Signature: SignChallenge(d.Priv, nonce, d.Pub), CmdrVersion: cmdrVersion, AppVersion: "test"}
}

// Recorder is in-memory Deps for tests: beads, comments, mail, usage, settles.
type Recorder struct {
	mu       sync.Mutex
	Beads    map[string]Bead
	Comments []RecordedComment
	Mails    []agentmail.Mail
	Inboxes  map[string][]agentmail.Mail
	Usage    []string
	Settled  []Settlement
}

// RecordedComment is one comment written through Deps.Comment.
type RecordedComment struct{ Bead, Role, Text string }

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder {
	return &Recorder{Beads: map[string]Bead{}, Inboxes: map[string][]agentmail.Mail{}}
}

// Wire fills d's outside calls with the recorder's.
func (r *Recorder) Wire(d Deps) Deps {
	d.ReadBead = func(_ context.Context, _, id string) (Bead, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		b, ok := r.Beads[id]
		if !ok {
			return Bead{}, ErrNotFound
		}
		return b, nil
	}
	d.Comment = func(_ context.Context, _, bead, role, text string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.Comments = append(r.Comments, RecordedComment{bead, role, text})
		return nil
	}
	d.Mail = func(_ context.Context, from, to, subject, body string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.Mails = append(r.Mails, agentmail.Mail{From: from, To: to, Subject: subject, Body: body})
		return nil
	}
	d.Inbox = func(_ context.Context, role string) ([]agentmail.Mail, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		m := r.Inboxes[role]
		delete(r.Inboxes, role)
		return m, nil
	}
	d.RecordUsage = func(place string, ev json.RawMessage) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.Usage = append(r.Usage, place+" "+string(ev))
	}
	d.Settle = func(_ context.Context, s Settlement) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.Settled = append(r.Settled, s)
		return nil
	}
	return d
}

// Snapshot copies what was recorded.
func (r *Recorder) Snapshot() (comments []RecordedComment, mails []agentmail.Mail, settled []Settlement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecordedComment(nil), r.Comments...), append([]agentmail.Mail(nil), r.Mails...), append([]Settlement(nil), r.Settled...)
}
