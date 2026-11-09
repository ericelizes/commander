package beads

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MockClient is a scriptable Client for downstream package tests.
//
// Tests set the *Result/*Err fields, then inspect Calls after exercise.
// Each method appends one entry to Calls in a stable, parseable form
// (e.g. "SetState:bd-1:cmdr=brainstorming").
type MockClient struct {
	mu    sync.Mutex
	Calls []string

	InitErr     error
	SetStateErr error

	ListResult []Bead
	ListErr    error

	CreateID  string
	CreateErr error

	LinkErr error

	DepsResult map[string][]Dep // by issue ID
	DepsErr    error
	// DependentsResult is the reverse edges by issue ID.
	DependentsResult map[string][]Dep
	// ParentsResult is the Parent Details reports (nil: callers fall back to
	// dotted ids); its edges come from DepsResult, comments from CommentsResult.
	ParentsResult  map[string]string
	ParentsErr     error
	CommentsResult map[string][]Comment

	ShowResult *Bead
	// ShowResults answers Show for the ids it holds before ShowResult.
	ShowResults map[string]*Bead
	ShowErr     error

	RenameErr error
	CloseErr  error
	SetErr    error
}

// Compile-time guarantee that MockClient implements Client.
var _ Client = (*MockClient)(nil)

func (m *MockClient) record(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, s)
}

func (m *MockClient) Init(_ context.Context, projectDir string) error {
	m.record(fmt.Sprintf("Init:dir=%s", projectDir))
	return m.InitErr
}

func (m *MockClient) SetState(_ context.Context, projectDir, issueID, dimension, value, reason string) error {
	m.record(fmt.Sprintf("SetState:%s:%s=%s|reason=%s|dir=%s", issueID, dimension, value, reason, projectDir))
	return m.SetStateErr
}

func (m *MockClient) List(_ context.Context, projectDir string, opts ListOptions) ([]Bead, error) {
	m.record(fmt.Sprintf(
		"List:dir=%s|labels=%s|labelAny=%s|status=%s|type=%s|limit=%d",
		projectDir,
		strings.Join(opts.Labels, ","),
		strings.Join(opts.LabelAny, ","),
		opts.Status,
		opts.Type,
		opts.Limit,
	))
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	if m.ListResult == nil {
		return []Bead{}, nil
	}
	return m.ListResult, nil
}

func (m *MockClient) Create(_ context.Context, projectDir, title string, opts CreateOptions) (string, error) {
	call := fmt.Sprintf(
		"Create:title=%s|parent=%s|labels=%s|priority=%d|type=%s|silent=%t|dir=%s",
		title, opts.Parent, strings.Join(opts.Labels, ","), opts.Priority, opts.Type, opts.Silent, projectDir,
	)
	if opts.AcceptanceCriteria != "" {
		call += "|acceptance=" + opts.AcceptanceCriteria
	}
	m.record(call)
	return m.CreateID, m.CreateErr
}

func (m *MockClient) Link(_ context.Context, projectDir, fromID, toID, linkType string) error {
	m.record(fmt.Sprintf("Link:%s->%s:type=%s|dir=%s", fromID, toID, linkType, projectDir))
	return m.LinkErr
}

func (m *MockClient) DependOn(_ context.Context, projectDir, issueID, dependsOn string) error {
	m.record(fmt.Sprintf("DependOn:%s->%s|dir=%s", issueID, dependsOn, projectDir))
	return nil
}

func (m *MockClient) RemoveDep(_ context.Context, projectDir, issueID, dependsOn string) error {
	m.record(fmt.Sprintf("RemoveDep:%s->%s|dir=%s", issueID, dependsOn, projectDir))
	return m.SetErr
}

func (m *MockClient) UpdateFields(_ context.Context, projectDir, issueID string, f Fields) error {
	args, err := f.args()
	if err != nil {
		return err
	}
	// One call; its label and status parts read as SetLabel/SetStatus do, so a test can find them.
	line := fmt.Sprintf("UpdateFields:%s|%s", issueID, strings.Join(args, " "))
	for _, l := range f.AddLabels {
		line += fmt.Sprintf("|label=%s|on=true", l)
	}
	for _, l := range f.RemoveLabels {
		line += fmt.Sprintf("|label=%s|on=false", l)
	}
	if f.Status != nil {
		line += "|status=" + *f.Status
	}
	m.record(line + "|dir=" + projectDir)
	return m.SetErr
}

func (m *MockClient) CloseWithReason(_ context.Context, projectDir, issueID, reason string) error {
	m.record(fmt.Sprintf("CloseWithReason:%s|reason=%s|dir=%s", issueID, reason, projectDir))
	return m.SetErr
}

func (m *MockClient) Delete(_ context.Context, projectDir, issueID, reason string) error {
	m.record(fmt.Sprintf("Delete:%s|reason=%s|dir=%s", issueID, reason, projectDir))
	return nil
}

func (m *MockClient) Deps(_ context.Context, projectDir, issueID string) ([]Dep, error) {
	m.record(fmt.Sprintf("Deps:%s|dir=%s", issueID, projectDir))
	return m.DepsResult[issueID], m.DepsErr
}

func (m *MockClient) Dependents(_ context.Context, projectDir, issueID string) ([]Dep, error) {
	m.record(fmt.Sprintf("Dependents:%s|dir=%s", issueID, projectDir))
	return m.DependentsResult[issueID], m.DepsErr
}

func (m *MockClient) Details(_ context.Context, projectDir string, ids []string) (map[string]Detail, error) {
	m.record(fmt.Sprintf("Details:%d|dir=%s", len(ids), projectDir))
	if m.ParentsErr != nil {
		return nil, m.ParentsErr
	}
	out := map[string]Detail{}
	for _, id := range ids {
		p, ok := m.ParentsResult[id]
		if !ok {
			p = ParentOf(id)
		}
		out[id] = Detail{Parent: p, Deps: m.DepsResult[id], Comments: m.CommentsResult[id]}
	}
	return out, nil
}

func (m *MockClient) Show(_ context.Context, projectDir, issueID string) (*Bead, error) {
	m.record(fmt.Sprintf("Show:%s|dir=%s", issueID, projectDir))
	if b, ok := m.ShowResults[issueID]; ok {
		return b, m.ShowErr
	}
	return m.ShowResult, m.ShowErr
}

func (m *MockClient) Rename(_ context.Context, projectDir, issueID, title string) error {
	m.record(fmt.Sprintf("Rename:%s|title=%s|dir=%s", issueID, title, projectDir))
	return m.RenameErr
}

func (m *MockClient) Close(_ context.Context, projectDir, issueID string) error {
	m.record(fmt.Sprintf("Close:%s|dir=%s", issueID, projectDir))
	return m.CloseErr
}

func (m *MockClient) SetStatus(_ context.Context, projectDir, issueID, status string) error {
	m.record(fmt.Sprintf("SetStatus:%s|status=%s|dir=%s", issueID, status, projectDir))
	return m.SetErr
}

func (m *MockClient) SetLabel(_ context.Context, projectDir, issueID, label string, on bool) error {
	m.record(fmt.Sprintf("SetLabel:%s|label=%s|on=%t|dir=%s", issueID, label, on, projectDir))
	return m.SetErr
}

func (m *MockClient) SetParent(_ context.Context, projectDir, issueID, parent string) error {
	m.record(fmt.Sprintf("SetParent:%s|parent=%s|dir=%s", issueID, parent, projectDir))
	return m.SetErr
}

func (m *MockClient) AddComment(_ context.Context, projectDir, issueID, text string) error {
	m.record(fmt.Sprintf("AddComment:%s|text=%s|dir=%s", issueID, text, projectDir))
	return nil
}

func (m *MockClient) Comments(_ context.Context, projectDir, issueID string) ([]Comment, error) {
	m.record(fmt.Sprintf("Comments:%s|dir=%s", issueID, projectDir))
	return m.CommentsResult[issueID], nil
}
