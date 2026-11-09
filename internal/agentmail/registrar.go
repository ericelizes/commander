package agentmail

import "context"

// AgentRegistrar is the slice of the am server that commander's activation flow
// depends on: ensure the fleet am project exists, and register an agent (the
// server assigns the name). *ServerClient satisfies this.
type AgentRegistrar interface {
	EnsureProject(ctx context.Context, humanKey string) (string, error)
	RegisterAgent(ctx context.Context, projectKey, program, model, taskDescription string) (string, error)
}

// MockRegistrar is a test double for AgentRegistrar.
type MockRegistrar struct {
	// NextName is returned from RegisterAgent (defaults to "MockAgent").
	NextName string
	// EnsureErr / RegisterErr force errors when non-nil.
	EnsureErr   error
	RegisterErr error

	EnsureCalls   []string       // human_keys passed to EnsureProject
	RegisterCalls []RegisterCall // each RegisterAgent invocation
}

// RegisterCall records one RegisterAgent invocation.
type RegisterCall struct {
	ProjectKey      string
	Program         string
	Model           string
	TaskDescription string
}

func (m *MockRegistrar) EnsureProject(_ context.Context, humanKey string) (string, error) {
	m.EnsureCalls = append(m.EnsureCalls, humanKey)
	if m.EnsureErr != nil {
		return "", m.EnsureErr
	}
	return "slug-" + humanKey, nil
}

func (m *MockRegistrar) RegisterAgent(_ context.Context, projectKey, program, model, task string) (string, error) {
	m.RegisterCalls = append(m.RegisterCalls, RegisterCall{ProjectKey: projectKey, Program: program, Model: model, TaskDescription: task})
	if m.RegisterErr != nil {
		return "", m.RegisterErr
	}
	if m.NextName != "" {
		return m.NextName, nil
	}
	return "MockAgent", nil
}
