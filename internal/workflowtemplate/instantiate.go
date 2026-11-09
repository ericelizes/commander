package workflowtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Params say where an instance is plugged in.
type Params struct {
	Project string
	Parent  string // the bead the block lives under
	Before  string // the bead that waits on the block
	PR      string // canonical PR URL
}

// InstStep is one step of an instance: what to create and what it waits on.
type InstStep struct {
	ID     string
	Title  string
	Labels []string
	After  []string // step ids it waits on
}

// Instance is a template bound to a place, with the definition snapshotted.
type Instance struct {
	Key      string
	Template string
	Version  int
	Hash     string
	Params   Params
	Title    string
	Labels   []string // container labels
	Snapshot string   // the definition as loaded, embedded in the container's description
	Steps    []InstStep
}

// Key identifies an instance by what it guards: project, parent, merge target
// and PR. The same four always give the same key, so a retry finds its beads.
func Key(p Params) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{p.Project, p.Parent, p.Before, p.PR}, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// Instantiate binds t to p. The snapshot and hash are taken now, so editing the
// template file later never changes an instance that already exists.
func Instantiate(t *Template, p Params) (*Instance, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if p.Project == "" || p.Parent == "" || p.Before == "" {
		return nil, fmt.Errorf("instantiate %s: project, parent and before are required", t.ID)
	}
	snap, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	in := &Instance{
		Key: Key(p), Template: t.ID, Version: t.Version, Hash: t.Hash(), Params: p,
		Title: t.Title, Snapshot: string(snap),
	}
	base := []string{
		LabelTemplate + t.ID,
		fmt.Sprintf("%s%d", LabelVersion, t.Version),
		LabelInstance + in.Key,
		LabelHash + in.Hash,
	}
	in.Labels = append([]string{"kind:objective"}, base...)
	waits := t.Waits()
	for _, s := range t.Steps {
		in.Steps = append(in.Steps, InstStep{
			ID: s.ID, Title: s.Title, After: waits[s.ID],
			Labels: append([]string{"kind:objective", LabelStep + s.ID}, base...),
		})
	}
	return in, nil
}
