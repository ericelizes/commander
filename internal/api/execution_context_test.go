package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresPlanRegisteredExecutionContextCannotUsePlanningEndpoints(t *testing.T) {
	ctx := context.Background()
	var scenario struct {
		Role    string `json:"registered_role"`
		Session string `json:"session"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-execution-context.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || scenario.Role == "" || scenario.Session == "" {
		t.Fatal("missing execution context scenario", e)
	}
	// This protected leaf already has a running imported execution when the
	// installation activates. The gate blocks fresh admission, not custody or
	// current-run reporting by a process that was already running.
	var rid int64
	f := newPublicationFixture(t, func(st *store.Store, pid int64) {
		var err error
		rid, err = st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: scenario.Role, OpencodeSessionID: scenario.Session, SpawnTime: time.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
	})
	env := []string{"COMMANDER_ROLE=" + scenario.Role, "COMMANDER_RUN_ID=" + strconv.FormatInt(rid, 10), "COMMANDER_RUN_SESSION=" + scenario.Session}
	for _, args := range [][]string{{"set-state", "leaf", "cmdr=executing"}, {"comments", "add", "leaf", "Verified evidence from this current run"}} {
		status, result := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: args, NoWait: true, Env: env})
		if status.StatusCode != http.StatusOK || result.Exit != 0 {
			t.Fatal("current claimed report rejected", args, status, result)
		}
	}
	_, forbidden := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"update", "leaf", "--title", "Execution changed its own goal"}, NoWait: true, Env: env})
	if forbidden.Exit == 0 {
		t.Fatal("planning registration overrode execution context")
	}
	// Blank role with execution attribution must not enter the interactive-user lane.
	_, anonymous := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"update", "leaf", "--title", "Pretend user"}, NoWait: true, Env: env[1:]})
	if anonymous.Exit == 0 {
		t.Fatal("execution context became an interactive user")
	}
	f.refresh(t)
	request := f.req
	request.Env = env
	for _, propose := range []bool{false, true} {
		request.Propose = propose
		if result := postPublication(t, f.base, request); result.Exit == 0 {
			t.Fatal("execution session published/proposed a human gate", propose)
		}
	}
	f.assertOriginal(t)
	// Logical planning for the same registered identity remains available from
	// its planning conversation, with no execution attribution to borrow.
	_, planned := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"update", "leaf", "--title", "Commander revised its owned goal"}, NoWait: true, Env: []string{"COMMANDER_ROLE=" + scenario.Role}})
	if planned.Exit != 0 {
		t.Fatal("logical planning session lost scope", planned)
	}
}
