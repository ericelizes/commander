package agentmail

import (
	"context"
	"fmt"
	"time"
)

// MockClient is a scriptable Client used by downstream packages' tests.
//
// Recorded calls land in Calls in the form "<method>:<key-args>" so tests can
// assert sequencing without coupling to argv shape (which the *_test.go in
// this package already cover).
//
// Result fields are returned verbatim; *Err fields override the zero-value
// returns when non-nil.
type MockClient struct {
	Calls []string

	RegisterErr error

	SendErr error

	InboxResult []Mail
	InboxErr    error

	CheckInboxResult []Mail
	CheckInboxErr    error

	ListAgentsResult []Agent
	ListAgentsErr    error
}

// Compile-time guard: MockClient must satisfy Client.
var _ Client = (*MockClient)(nil)

func (m *MockClient) Register(_ context.Context, project, program, model, name string) error {
	m.Calls = append(m.Calls, fmt.Sprintf("Register:%s/%s/%s/%s", project, program, model, name))
	return m.RegisterErr
}

func (m *MockClient) Send(_ context.Context, project, from, to, subject, _ string, opts SendOptions) error {
	m.Calls = append(m.Calls, fmt.Sprintf("Send:%s/%s->%s/%s/imp=%s/ack=%v",
		project, from, to, subject, opts.Importance, opts.AckRequired))
	return m.SendErr
}

func (m *MockClient) Inbox(_ context.Context, project, agent string, since time.Time, opts InboxOptions) ([]Mail, error) {
	m.Calls = append(m.Calls, fmt.Sprintf("Inbox:%s/%s/since=%s/limit=%d/bodies=%v",
		project, agent, iso8601(since), opts.Limit, opts.IncludeBodies))
	return m.InboxResult, m.InboxErr
}

func (m *MockClient) CheckInbox(_ context.Context, project, agent string) ([]Mail, error) {
	m.Calls = append(m.Calls, fmt.Sprintf("CheckInbox:%s/%s", project, agent))
	return m.CheckInboxResult, m.CheckInboxErr
}

func (m *MockClient) ListAgents(_ context.Context, project string) ([]Agent, error) {
	m.Calls = append(m.Calls, fmt.Sprintf("ListAgents:%s", project))
	return m.ListAgentsResult, m.ListAgentsErr
}
