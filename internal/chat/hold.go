package chat

import (
	"os"
	"path/filepath"
	"time"
)

// MailHold is how long a role's mail waits after the user last sent it a message
// or typed in its chat: mail landing mid-conversation derails it. When the
// hold lapses (or the user releases it) the waiting mail goes in as one batch.
const MailHold = 2 * time.Minute

// talkingPath is a file whose mtime is the last time the user talked to role; a
// file so the mail hook (a separate cmdr process) can read it too.
func talkingPath(home, role string) string {
	return filepath.Join(home, "chat", role+".talking")
}

// releasePath marks the user's "send them now": it beats the hold until the mail
// goes in, so typing his next message does not take the click back.
func releasePath(home, role string) string {
	return filepath.Join(home, "chat", role+".release")
}

// ReleaseTTL caps a release whose mail never went in, so it cannot end the hold for good.
const ReleaseTTL = 10 * time.Minute

// Talking reports whether the user talked to role within MailHold and has not released its mail.
func Talking(home, role string, now time.Time) bool {
	if fi, err := os.Stat(releasePath(home, role)); err == nil && now.Sub(fi.ModTime()) < ReleaseTTL {
		return false
	}
	fi, err := os.Stat(talkingPath(home, role))
	return err == nil && now.Sub(fi.ModTime()) < MailHold
}

// MailDelivered ends a release once role's waiting mail went in.
func MailDelivered(home, role string) {
	_ = os.Remove(releasePath(home, role))
}

// MarkTalking records that the user is talking to role now.
func (e *Engine) MarkTalking(role string) error {
	p := talkingPath(e.opts.Home, role)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o600)
}

// ReleaseMail ends the hold until the waiting mail goes in (next wake poll,
// or the end of the current turn), even if the user keeps typing.
func (e *Engine) ReleaseMail(role string) error {
	p := releasePath(e.opts.Home, role)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o600)
}

// Talking reports whether the user talked to role within MailHold.
func (e *Engine) Talking(role string) bool {
	return Talking(e.opts.Home, role, time.Now())
}

// MailDelivered ends role's release: its waiting mail went in.
func (e *Engine) MailDelivered(role string) {
	MailDelivered(e.opts.Home, role)
}
