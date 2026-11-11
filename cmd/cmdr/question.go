package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func runQuestion(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr question ask|inbox|get|history|answer [--project-id ID --bead ID --plan-revision N --request KEY --text TEXT --checkpoint TEXT --related JSON]")
		return 2
	}
	fs := flag.NewFlagSet("question", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "")
	bead := fs.String("bead", "", "")
	revision := fs.Int64("plan-revision", 0, "")
	key := fs.String("request", "", "")
	text := fs.String("text", "", "")
	recommend := fs.String("recommendation", "", "")
	checkpoint := fs.String("checkpoint", "", "")
	blocking := fs.Bool("blocking", true, "")
	related := fs.String("related", "[]", "")
	id := fs.String("id", "", "")
	approve := fs.Bool("approve", true, "")
	commander := fs.String("commander", "", "")
	runID := fs.Int64("run-id", 0, "history run filter")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	switch args[0] {
	case "ask", "history":
		if *pid <= 0 || *bead == "" {
			return 2
		}
	case "get", "answer":
		if *id == "" {
			return 2
		}
	case "inbox":
	default:
		return 2
	}
	if args[0] == "ask" && beads.HasExecutionAttribution(os.Environ()) {
		rid, err := strconv.ParseInt(os.Getenv("COMMANDER_RUN_ID"), 10, 64)
		if err != nil || rid <= 0 || os.Getenv("COMMANDER_RUN_SESSION") == "" {
			fmt.Fprintln(stderr, "execution questions require their immutable run and session")
			return 1
		}
	}
	if args[0] == "answer" && beads.HasExecutionAttribution(os.Environ()) {
		fmt.Fprintln(stderr, "execution sessions cannot answer planning questions")
		return 1
	}
	path, e := stateDBPath()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	st, e := store.Open(path)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	defer st.Close()
	if !st.IsPostgres() {
		fmt.Fprintln(stderr, "durable questions require PostgreSQL")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	actor := os.Getenv("COMMANDER_ROLE")
	var result any
	switch args[0] {
	case "ask":
		var scopes []store.QuestionScope
		if e = json.Unmarshal([]byte(*related), &scopes); e != nil {
			break
		}
		rid, _ := strconv.ParseInt(os.Getenv("COMMANDER_RUN_ID"), 10, 64)
		runtime, thread, checkpointErr := questionCheckpoint(ctx, st, agentrt.CommanderHome(), actor, rid, os.Getenv("COMMANDER_RUN_SESSION"), *blocking)
		if checkpointErr != nil {
			e = checkpointErr
			break
		}
		var question string
		question, e = st.AskQuestion(ctx, store.QuestionRequest{ProjectID: *pid, BeadID: *bead, PlanRevision: *revision, Actor: actor, ClientKey: *key, RunID: rid, Session: os.Getenv("COMMANDER_RUN_SESSION"), Question: *text, Recommendation: *recommend, Checkpoint: *checkpoint, CheckpointRuntime: runtime, CheckpointThread: thread, Blocking: *blocking, Related: scopes})
		result = map[string]string{"question_id": question}
	case "get":
		result, e = st.Question(ctx, *id)
	case "history":
		result, e = st.QuestionHistory(ctx, *pid, *bead, *runID)
	case "inbox":
		if *commander != "" {
			actor = *commander
		}
		if actor == "" {
			actor = "commander"
		}
		result, e = st.QuestionInbox(ctx, actor)
	case "answer":
		var complete bool
		complete, e = st.AnswerQuestion(ctx, *id, actor, *text, *approve)
		result = map[string]bool{"complete": complete}
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(result); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return 0
}

// Native execution questions use the same immutable source binding as launch
// and resume. Role preferences and derived metadata cannot change the saved
// provider/thread while the agent is still executing its original run.
func questionCheckpoint(ctx context.Context, st *store.Store, home, actor string, rid int64, session string, blocking bool) (runtime, thread string, err error) {
	runtime = chat.ProviderOf(home, actor).Runtime
	path := chat.CodexThreadFileForRun(home, actor, rid, session)
	if rid > 0 {
		run, e := st.GetAgentRun(ctx, rid)
		if e != nil {
			return "", "", e
		}
		if run == nil || run.Status != "running" || run.OpencodeSessionID != session || strings.ReplaceAll(run.AgentName, ".", "-") != strings.ReplaceAll(actor, ".", "-") {
			return "", "", fmt.Errorf("checkpoint does not belong to the current execution session")
		}
		if run.ExecutionDriver == "native" {
			runtime = run.ExecutionRuntime
			path = chat.NativeCodexThreadFile(home, run.AgentName, session)
		}
	}
	if runtime == chat.RuntimeCodex {
		raw, e := os.ReadFile(path)
		if e != nil && blocking && rid > 0 {
			return "", "", fmt.Errorf("read current Codex checkpoint thread: %w", e)
		}
		thread = strings.TrimSpace(string(raw))
		if thread == "" && blocking && rid > 0 {
			return "", "", fmt.Errorf("current Codex checkpoint has no native thread")
		}
	}
	return runtime, thread, nil
}
