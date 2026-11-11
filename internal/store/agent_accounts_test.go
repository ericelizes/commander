package store

import (
	"context"
	"reflect"
	"testing"
)

func TestAgentAccountsDefaultAndSavedOrder(t *testing.T) {
	st := postgresTestStore(t)
	ctx := context.Background()
	got, err := st.AgentAccounts(ctx)
	if err != nil || !reflect.DeepEqual(got, DefaultAgentAccounts) {
		t.Fatalf("defaults %v %v", got, err)
	}
	if err = st.SetAgentAccounts(ctx, AgentKindAgent, []string{" claude ", "codex", "claude", ""}); err != nil {
		t.Fatal(err)
	}
	got, err = st.AgentAccounts(ctx)
	if err != nil || !reflect.DeepEqual(got[AgentKindAgent], []string{"claude", "codex"}) || !reflect.DeepEqual(got[AgentKindCommander], DefaultAgentAccounts[AgentKindCommander]) {
		t.Fatalf("saved %v %v", got, err)
	}
	if err = st.SetAgentAccounts(ctx, AgentKindCommander, nil); err == nil {
		t.Fatal("an empty order was saved")
	}
	if err = st.SetAgentAccounts(ctx, "manager", []string{"claude"}); err == nil {
		t.Fatal("an unknown kind was saved")
	}
}
