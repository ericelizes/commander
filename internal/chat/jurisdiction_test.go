package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/pgtest"
)

func TestScopedCommanderResolvesIndependentDurableConversation(t *testing.T) {
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	r := StoreResolver{Store: st, Home: home}
	for _, invalid := range []string{"commander-", "commander--urgent", "commander-../escape"} {
		if e := st.RegisterCommander(ctx, invalid, "Invalid"); e == nil {
			t.Fatalf("unstartable Commander id registered: %s", invalid)
		}
	}
	if _, e := r.Resolve(ctx, "commander-unregistered"); e == nil {
		t.Fatal("unregistered conversation resolved")
	}
	var ids []string
	for _, role := range []string{"commander-urgent", "commander-background"} {
		if e := st.RegisterCommander(ctx, role, role); e != nil {
			t.Fatal(e)
		}
		first, e := r.Resolve(ctx, role)
		if e != nil {
			t.Fatal(e)
		}
		if first.Role != role || first.Agent != role || first.Tier != agentrt.TierCommander || first.SessionID == "" {
			t.Fatalf("wrong spec: %+v", first)
		}
		if !strings.Contains(first.Settings, role) {
			t.Fatalf("hooks point outside conversation: %s", first.Settings)
		}
		second, e := r.Resolve(ctx, role)
		if e != nil || second.SessionID != first.SessionID {
			t.Fatalf("session not durable: %+v %v", second, e)
		}
		if _, e = os.Stat(filepath.Join(home, agentrt.SessionFileName(role, agentrt.Claude))); e != nil {
			t.Fatal(e)
		}
		if !AutoSwitch(home, role) || !isManagedChatRole(role) {
			t.Fatal("scoped Commander lost recovery/account policy")
		}
		ids = append(ids, first.SessionID)
	}
	if ids[0] == ids[1] {
		t.Fatal("focused threads share a runtime session")
	}
	if e := r.NewSession(ctx, "commander-urgent"); e != nil {
		t.Fatal(e)
	}
	renewed, e := r.Resolve(ctx, "commander-urgent")
	if e != nil || renewed.SessionID == ids[0] {
		t.Fatal("fresh thread did not rotate its session", e)
	}
	retained, e := r.Resolve(ctx, "commander-background")
	if e != nil || retained.SessionID != ids[1] {
		t.Fatal("fresh thread changed background session", e)
	}
}

func TestCommanderPromptMatchesActiveJurisdictionModel(t *testing.T) {
	var scenario struct{ Planners []string }
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-executor-runtime.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Planners) != 2 || scenario.Planners[0] != "commander" {
		t.Fatal("planning role scenario missing")
	}
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	if e := st.RegisterCommander(ctx, scenario.Planners[1], "Urgent"); e != nil {
		t.Fatal(e)
	}
	r := StoreResolver{Store: st, Home: home}
	if e := r.NewSession(ctx, "commander"); e != nil {
		t.Fatal(e)
	}
	check := func(role string, wantPlanning bool) {
		t.Helper()
		spec, e := r.Resolve(ctx, role)
		if e != nil {
			t.Fatal(e)
		}
		body, e := os.ReadFile(spec.PromptPath)
		if e != nil {
			t.Fatal(e)
		}
		if wantPlanning {
			for _, want := range []string{"planning owner", "do not delegate tree management", "cmdr plan apply", "cmdr question inbox", "cmdr execution ready", "--resume-only"} {
				if !strings.Contains(string(body), want) {
					t.Errorf("%s lost planning instruction %q", role, want)
				}
			}
			for _, bad := range []string{"bias toward delegation", "route work to the right Manager", "Commander reviews every owned tree", "Manager to start"} {
				if strings.Contains(string(body), bad) {
					t.Errorf("%s retained obsolete delegation instruction %q", role, bad)
				}
			}
		} else if !strings.Contains(string(body), "bias toward delegation") {
			t.Fatal("legacy Commander contract changed before activation")
		}
	}
	check(scenario.Planners[0], false)
	check(scenario.Planners[1], true)
	if _, e := st.SetOrchestrationControl(ctx, 1, "active", "planning prompt fixture"); e != nil {
		t.Fatal(e)
	}
	for _, role := range scenario.Planners {
		check(role, true)
	}
}
