package agentmail

import (
	"context"
	"testing"
)

type roleMap map[string]string

func (m roleMap) GetAmAgent(_ context.Context, key, role string) (string, error) {
	return m[key+"|"+role], nil
}
func (m roleMap) UpsertAmAgent(_ context.Context, key, role, name string) error {
	m[key+"|"+role] = name
	return nil
}

func TestServerRegistrar_ReusesAMappedRole(t *testing.T) {
	cli := &MockClient{}
	srv := &MockRegistrar{}
	names := roleMap{"/repo|operator-op-x": "BlueFox"}
	r := &ServerRegistrar{Client: cli, Server: srv, Store: names, Key: "/repo"}
	if err := r.Register(context.Background(), "commander", "claude-code", "opus", "operator-op-x"); err != nil {
		t.Fatal(err)
	}
	if len(srv.EnsureCalls)+len(srv.RegisterCalls)+len(cli.Calls) != 0 {
		t.Fatalf("mapped role must not register again: server %v %v, cli %v", srv.EnsureCalls, srv.RegisterCalls, cli.Calls)
	}
}

func TestServerRegistrar_RegistersUnderThePathKeyNeverTheName(t *testing.T) {
	cli := &MockClient{}
	srv := &MockRegistrar{NextName: "RedOwl"}
	names := roleMap{}
	r := &ServerRegistrar{Client: cli, Server: srv, Store: names, Key: "/repo"}
	if err := r.Register(context.Background(), "commander", "claude-code", "opus", "operator-op-y"); err != nil {
		t.Fatal(err)
	}
	if len(cli.Calls) != 0 {
		t.Fatalf("the am CLI must not be called: %v", cli.Calls)
	}
	if len(srv.EnsureCalls) != 1 || srv.EnsureCalls[0] != "/repo" || len(srv.RegisterCalls) != 1 || srv.RegisterCalls[0].ProjectKey != "/repo" {
		t.Fatalf("want ensure+register under /repo, got %v %v", srv.EnsureCalls, srv.RegisterCalls)
	}
	if names["/repo|operator-op-y"] != "RedOwl" {
		t.Fatalf("role not mapped: %v", names)
	}
}
