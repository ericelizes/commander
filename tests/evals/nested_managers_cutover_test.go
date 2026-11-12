package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

type cutoverMail struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// The real cmdr internal-nest-managers and internal-mail, against a real store, a
// scripted bd holding the tree, a fake cmdrd and a fake am server.
func TestNestedManagersCutoverScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/nested-managers-cutover.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Project      string                    `json:"project"`
		Operator     string                    `json:"operator"`
		Task         string                    `json:"task"`
		Beads        map[string]staleBoardBead `json:"beads"`
		Names        map[string]string         `json:"names"`
		RootCursor   string                    `json:"root_cursor"`
		RootInbox    []cutoverMail             `json:"root_inbox"`
		NestedOwners []string                  `json:"nested_owners"`
		WantUnread   map[string][]string       `json:"want_unread"`
		WantOperator map[string]string         `json:"want_operator"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 120*time.Second))
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	fixture := filepath.Join(dir, "beads")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	writeBeadsFixture(t, fixture, sc.Beads)
	// list --label L answers from label-<L>.json.
	byLabel := map[string][]json.RawMessage{}
	for id, b := range sc.Beads {
		rawBead, _ := os.ReadFile(filepath.Join(fixture, id+".json"))
		for _, l := range b.Labels {
			byLabel[l] = append(byLabel[l], rawBead)
		}
	}
	for l, list := range byLabel {
		out, _ := json.Marshal(list)
		if err := os.WriteFile(filepath.Join(fixture, "label-"+strings.ReplaceAll(l, ":", "_")+".json"), out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "bd")
	writeExecutable(t, script, `#!/bin/sh
cmd="$1"; shift
ids=''; label=''
case "$cmd" in
show) ids="$*" ;;
list)
  while [ $# -gt 0 ]; do
    case "$1" in
      --id) ids=$(echo "$2" | tr ',' ' '); shift ;;
      --label) label="$2"; shift ;;
    esac
    shift
  done
  if [ -z "$ids" ]; then
    f="$CUTOVER_BEADS/label-$(echo "$label" | tr ':' '_').json"
    if [ -n "$label" ] && [ -f "$f" ]; then cat "$f"; else echo '[]'; fi
    exit 0
  fi ;;
*) echo '[]'; exit 0 ;;
esac
out=''
for a in $ids; do
  if [ -f "$CUTOVER_BEADS/$a.json" ]; then out="$out${out:+,}$(cat "$CUTOVER_BEADS/$a.json")"; fi
done
echo "[$out]"
`)
	t.Setenv("CUTOVER_BEADS", fixture)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	const key = "/example/repo"
	t.Setenv("COMMANDER_REPO", key)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fixture")
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "project")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COMMANDER_HOME", home)

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pid, err := st.InsertProject(ctx, store.Project{Name: sc.Project, Type: "code", Path: project, Status: "active", ActivatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: sc.Task, AgentName: sc.Operator, SpawnTime: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "demo-r", AmRole: "manager-demo-r", SessionFile: filepath.Join(home, "root-session")}); err != nil {
		t.Fatal(err)
	}
	nameToRole := map[string]string{}
	for role, name := range sc.Names {
		nameToRole[name] = role
		if err := st.UpsertAmAgent(ctx, key, role, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := mailstate.MarkRead("manager-demo-r", agentmail.Mail{ID: sc.RootCursor}); err != nil {
		t.Fatal(err)
	}
	rootCursor := func() []byte {
		b, _ := os.ReadFile(filepath.Join(home, "mail-cursors", "manager-demo-r.json"))
		return b
	}
	cursorBefore := rootCursor()

	cmdrd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": map[string]any{"running": true}})
			return
		}
		http.NotFound(w, r)
	}))
	defer cmdrd.Close()
	for f, v := range map[string]string{"api-url": cmdrd.URL, "api-token": "eval"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	type sent struct{ to, subject, body string }
	var mu sync.Mutex
	var mails []sent
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		text := "{}"
		switch req.Params.Name {
		case "send_message":
			mu.Lock()
			to, _ := req.Params.Arguments["to"].([]any)
			for _, n := range to {
				mails = append(mails, sent{nameToRole[fmt.Sprint(n)], fmt.Sprint(req.Params.Arguments["subject"]), fmt.Sprint(req.Params.Arguments["body_md"])})
			}
			mu.Unlock()
		case "fetch_inbox":
			if req.Params.Arguments["mark_read"] != false {
				t.Errorf("fetch_inbox wrote read state: %v", req.Params.Arguments)
			}
			var inbox []map[string]any
			if req.Params.Arguments["agent_name"] == sc.Names["manager-demo-r"] {
				for _, m := range sc.RootInbox {
					inbox = append(inbox, map[string]any{"id": m.ID, "subject": m.Subject, "from": "SomeSender", "body_md": m.Body, "importance": "normal", "created_ts": time.Now().UTC().Format(time.RFC3339)})
				}
			}
			b, _ := json.Marshal(append([]map[string]any{}, inbox...))
			text = string(b)
		default:
			t.Errorf("mail server asked for %s", req.Params.Name)
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	}))
	defer am.Close()
	t.Setenv("AM_HTTP_URL", am.URL)
	take := func() []sent {
		mu.Lock()
		defer mu.Unlock()
		out := mails
		mails = nil
		return out
	}
	run := func(args ...string) (map[string]any, string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out.String(), errOut.String())
		}
		var res map[string]any
		_ = json.Unmarshal(out.Bytes(), &res)
		return res, out.String()
	}
	type planOwner struct {
		BeadID     string `json:"bead_id"`
		Registered bool   `json:"registered"`
		Operators  []struct {
			Agent      string `json:"agent"`
			OldManager string `json:"old_manager"`
		} `json:"operators"`
		RootUnread []struct {
			ID string `json:"id"`
		} `json:"root_unread"`
	}
	owners := func(out string) []planOwner {
		var res struct {
			Owners []planOwner `json:"owners"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return res.Owners
	}
	checkPlan := func(out string) {
		t.Helper()
		var ids []string
		for _, o := range owners(out) {
			ids = append(ids, o.BeadID)
			var unread []string
			for _, m := range o.RootUnread {
				unread = append(unread, m.ID)
			}
			if !slices.Equal(unread, sc.WantUnread[o.BeadID]) {
				t.Errorf("%s: root unread %v, want %v", o.BeadID, unread, sc.WantUnread[o.BeadID])
			}
			if want := sc.WantOperator[o.BeadID]; want != "" && (len(o.Operators) != 1 || o.Operators[0].Agent != want || o.Operators[0].OldManager != "manager-demo-r") {
				t.Errorf("%s: operators %+v", o.BeadID, o.Operators)
			}
		}
		if !slices.Equal(ids, sc.NestedOwners) {
			t.Errorf("owners %v, want %v", ids, sc.NestedOwners)
		}
	}

	// Dry run: the plan, nothing written, nobody mailed.
	res, out := run("internal-nest-managers", "--project", sc.Project)
	checkPlan(out)
	if beads.NestedManagersOn(project) || res["mode"] != "dry-run" || len(take()) != 0 {
		t.Fatalf("dry run changed something: %s", out)
	}
	if row, _ := st.GetWorkstreamManager(ctx, pid, "demo-a1"); row != nil {
		t.Fatal("dry run registered a nested Manager")
	}

	// Apply.
	_, out = run("internal-nest-managers", "--project", sc.Project, "--apply")
	t.Logf("apply: %s", out)
	checkPlan(out)
	if !beads.NestedManagersOn(project) {
		t.Fatalf("switch still off: %s", out)
	}
	for _, id := range sc.NestedOwners {
		if row, _ := st.GetWorkstreamManager(ctx, pid, id); row == nil {
			t.Errorf("%s not registered", id)
		}
		if !mailstate.HasCursor("manager-" + id) {
			t.Errorf("manager-%s has no mail cursor: its first mail would read as history", id)
		}
	}
	got := take()
	byTo := map[string]sent{}
	for _, m := range got {
		byTo[m.to] = m
	}
	if h := byTo["manager-demo-a1"]; !strings.Contains(h.body, sc.Operator) || !strings.Contains(h.body, "7") || !strings.Contains(h.body, "cmdr internal-inbox --agent manager-demo-r") {
		t.Errorf("nested handover lacks its Operator or the root's unread mail: %+v", h)
	}
	if n := byTo["manager-demo-r"]; !strings.Contains(n.body, "manager-demo-a1") || !strings.Contains(n.body, "Forward, do not act on, your unread mail 7") || !strings.Contains(n.body, "unread mail 8") {
		t.Errorf("root notice: %+v", n)
	}
	if !bytes.Equal(rootCursor(), cursorBefore) {
		t.Fatalf("the root's mail cursor moved: %s -> %s", cursorBefore, rootCursor())
	}

	// A rerun of --apply changes and mails nothing.
	if res, out := run("internal-nest-managers", "--project", sc.Project, "--apply"); len(take()) != 0 || res["switch_after"] != true || !strings.Contains(out, "already on") {
		t.Fatalf("a rerun of --apply: %s", out)
	}

	// A running Operator's report to its old Manager lands at its nested Manager.
	t.Setenv("COMMANDER_ROLE", sc.Operator)
	run("internal-mail", "--from", sc.Operator, "--to", "manager-demo-r", "--subject", "done", "--body", "report")
	t.Setenv("COMMANDER_ROLE", "")
	got = take()
	var to []string
	for _, m := range got {
		to = append(to, m.to+"|"+m.subject)
	}
	slices.Sort(to)
	if want := []string{"manager-demo-a1|done", "manager-demo-r|[cc: moved to manager-demo-a1] done"}; !slices.Equal(to, want) {
		t.Fatalf("report went to %v, want %v", to, want)
	}

	// Undo.
	_, out = run("internal-nest-managers", "--project", sc.Project, "--apply", "--undo")
	if beads.NestedManagersOn(project) {
		t.Fatalf("switch still on: %s", out)
	}
	got = take()
	byTo = map[string]sent{}
	for _, m := range got {
		byTo[m.to] = m
	}
	// The root learns what returns to it; every nested Manager is told to stop.
	if n := byTo["manager-demo-r"]; !strings.Contains(n.body, "manager-demo-a1") || !strings.Contains(n.body, sc.Operator+" on "+sc.Task+" reports to you again") {
		t.Fatalf("undo root notice %+v", got)
	}
	for _, id := range sc.NestedOwners {
		if n, ok := byTo["manager-"+id]; !ok || !strings.Contains(n.body, "Stop dispatching") {
			t.Errorf("manager-%s was not told to stop: %+v", id, got)
		}
	}
	if len(got) != 1+len(sc.NestedOwners) {
		t.Errorf("undo mail %+v", got)
	}
	if !bytes.Equal(rootCursor(), cursorBefore) {
		t.Fatal("the root's mail cursor moved on undo")
	}

	// A rerun of --undo mails nobody again.
	if _, out := run("internal-nest-managers", "--project", sc.Project, "--apply", "--undo"); len(take()) != 0 || !strings.Contains(out, "already off") {
		t.Fatalf("a rerun of --undo: %s", out)
	}

	// The Operator still names its nested Manager: its report goes back to the
	// root. The obsolete nested Manager owns no work after undo, so no stale
	// copy may wake it (the cutover notice above already told it to stop).
	t.Setenv("COMMANDER_ROLE", sc.Operator)
	run("internal-mail", "--from", sc.Operator, "--to", "manager-demo-a1", "--subject", "done again", "--body", "report")
	t.Setenv("COMMANDER_ROLE", "")
	to = nil
	for _, m := range take() {
		to = append(to, m.to+"|"+m.subject)
	}
	slices.Sort(to)
	if want := []string{"manager-demo-r|done again"}; !slices.Equal(to, want) {
		t.Fatalf("after undo the report went to %v, want %v", to, want)
	}
}
