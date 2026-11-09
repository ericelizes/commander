package beads

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemClient is a stateful in-memory Client: beads created, labelled, closed and
// commented are what later calls see. Tests use it where MockClient (scripted,
// stateless) cannot follow a flow. One bead set serves every project dir.
// validLabel is the label rule br enforces on create.
var validLabel = regexp.MustCompile(`^[A-Za-z0-9_:-]+$`)

type MemClient struct {
	mu       sync.Mutex
	next     int
	Beads    []*Bead
	Edges    []Dep
	comments map[string][]Comment
	Prefix   string // id prefix, default "mem"
	// EdgeParentsOnly makes Details report a parent only from a parent-child
	// edge, as br does; by default a bead without one reports its dotted parent.
	EdgeParentsOnly bool
}

var _ Client = (*MemClient)(nil)

func (m *MemClient) find(id string) *Bead {
	for _, b := range m.Beads {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// Seed adds a bead as-is (a step, a stage).
func (m *MemClient) Seed(b Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.Status == "" {
		b.Status = "open"
	}
	m.Beads = append(m.Beads, &b)
}

func (m *MemClient) Init(context.Context, string) error { return nil }

func (m *MemClient) SetState(context.Context, string, string, string, string, string) error {
	return nil
}

func (m *MemClient) List(_ context.Context, _ string, opts ListOptions) ([]Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Bead{}
	for _, b := range m.Beads {
		if b.Status == "tombstone" {
			continue
		}
		if opts.Status != "" && opts.Status != "all" && b.Status != opts.Status {
			continue
		}
		if opts.Status == "" && b.Status == "closed" {
			continue
		}
		ok := len(opts.IDs) == 0 || slices.Contains(opts.IDs, b.ID)
		for _, l := range opts.Labels {
			ok = ok && slices.Contains(b.Labels, l)
		}
		if len(opts.LabelAny) > 0 {
			any := false
			for _, l := range opts.LabelAny {
				any = any || slices.Contains(b.Labels, l)
			}
			ok = ok && any
		}
		if ok {
			cp := *b
			cp.Labels = slices.Clone(b.Labels)
			out = append(out, cp)
		}
	}
	return out, nil
}

func (m *MemClient) Create(_ context.Context, _, title string, opts CreateOptions) (string, error) {
	for _, l := range opts.Labels {
		if !validLabel.MatchString(l) {
			return "", fmt.Errorf("br create: Validation failed: label %q: invalid characters (only alphanumeric, hyphen, underscore, colon allowed)", l)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := m.Prefix
	if prefix == "" {
		prefix = "mem"
	}
	var id string
	for {
		m.next++
		id = fmt.Sprintf("%s-%d", prefix, m.next)
		if opts.Parent != "" {
			id = fmt.Sprintf("%s.%d", opts.Parent, m.next)
		}
		if m.find(id) == nil {
			break
		}
	}
	if opts.Parent != "" {
		m.Edges = append(m.Edges, Dep{IssueID: id, DependsOnID: opts.Parent, Type: "parent-child"})
	}
	m.Beads = append(m.Beads, &Bead{ID: id, Title: title, Description: opts.Description, AcceptanceCriteria: opts.AcceptanceCriteria, Status: "open",
		Labels: slices.Clone(opts.Labels), ParentID: opts.Parent, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return id, nil
}

func (m *MemClient) Link(_ context.Context, _, fromID, toID, linkType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if linkType == "" {
		linkType = "blocks"
	}
	m.Edges = append(m.Edges, Dep{IssueID: fromID, DependsOnID: toID, Type: linkType})
	return nil
}

func (m *MemClient) DependOn(ctx context.Context, dir, issueID, dependsOn string) error {
	return m.Link(ctx, dir, issueID, dependsOn, "blocks")
}

func (m *MemClient) RemoveDep(_ context.Context, _, issueID, dependsOn string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Edges = slices.DeleteFunc(m.Edges, func(e Dep) bool {
		return e.IssueID == issueID && e.DependsOnID == dependsOn && e.Type != "parent-child"
	})
	return nil
}

func (m *MemClient) UpdateFields(_ context.Context, _, issueID string, f Fields) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.find(issueID)
	if b == nil {
		return fmt.Errorf("no issue %s", issueID)
	}
	if f.Title != nil {
		b.Title = *f.Title
	}
	if f.Description != nil {
		b.Description = *f.Description
	}
	return nil
}

func (m *MemClient) CloseWithReason(ctx context.Context, dir, issueID, _ string) error {
	return m.Close(ctx, dir, issueID)
}

func (m *MemClient) Deps(_ context.Context, _, issueID string) ([]Dep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Dep
	for _, e := range m.Edges {
		if e.IssueID == issueID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemClient) Dependents(_ context.Context, _, issueID string) ([]Dep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Dep
	for _, e := range m.Edges {
		if e.DependsOnID == issueID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemClient) Details(_ context.Context, _ string, ids []string) (map[string]Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Detail, len(ids))
	for _, id := range ids {
		if m.find(id) == nil {
			continue
		}
		d := Detail{Comments: slices.Clone(m.comments[id])}
		if !m.EdgeParentsOnly {
			d.Parent = ParentOf(id)
		}
		for _, e := range m.Edges {
			if e.IssueID == id {
				d.Deps = append(d.Deps, e)
				if e.Type == "parent-child" {
					d.Parent = e.DependsOnID
				}
			}
		}
		out[id] = d
	}
	return out, nil
}

func (m *MemClient) Show(_ context.Context, _, issueID string) (*Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.find(issueID)
	if b == nil || b.Status == "tombstone" {
		return nil, fmt.Errorf("br show: no issue %s: %w", issueID, ErrNotFound)
	}
	cp := *b
	cp.Labels = slices.Clone(b.Labels)
	return &cp, nil
}

func (m *MemClient) Rename(_ context.Context, _, issueID, title string) error {
	return m.edit(issueID, func(b *Bead) { b.Title = title })
}

func (m *MemClient) Close(_ context.Context, _, issueID string) error {
	return m.edit(issueID, func(b *Bead) { b.Status = "closed" })
}

func (m *MemClient) Delete(_ context.Context, _, issueID, _ string) error {
	return m.edit(issueID, func(b *Bead) { b.Status = "tombstone" })
}

func (m *MemClient) SetStatus(_ context.Context, _, issueID, status string) error {
	return m.edit(issueID, func(b *Bead) { b.Status = status })
}

func (m *MemClient) SetLabel(_ context.Context, _, issueID, label string, on bool) error {
	return m.edit(issueID, func(b *Bead) {
		has := slices.Contains(b.Labels, label)
		switch {
		case on && !has:
			b.Labels = append(b.Labels, label)
		case !on && has:
			b.Labels = slices.DeleteFunc(b.Labels, func(l string) bool { return l == label })
		}
	})
}

func (m *MemClient) SetParent(_ context.Context, _, issueID, parent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.find(issueID)
	if b == nil {
		return fmt.Errorf("no issue %s", issueID)
	}
	m.Edges = slices.DeleteFunc(m.Edges, func(e Dep) bool { return e.IssueID == issueID && e.Type == "parent-child" })
	b.ParentID = parent
	if parent != "" {
		m.Edges = append(m.Edges, Dep{IssueID: issueID, DependsOnID: parent, Type: "parent-child"})
	}
	return nil
}

func (m *MemClient) edit(id string, f func(*Bead)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.find(id)
	if b == nil {
		return fmt.Errorf("no issue %s", id)
	}
	f(b)
	return nil
}

func (m *MemClient) AddComment(_ context.Context, _, issueID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.find(issueID) == nil {
		return fmt.Errorf("no issue %s", issueID)
	}
	if m.comments == nil {
		m.comments = map[string][]Comment{}
	}
	m.comments[issueID] = append(m.comments[issueID], Comment{ID: fmt.Sprintf("comment-%d", len(m.comments[issueID])+1), Author: "test", Text: strings.TrimSpace(text), CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return nil
}

func (m *MemClient) Comments(_ context.Context, _, issueID string) ([]Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.comments[issueID]), nil
}
