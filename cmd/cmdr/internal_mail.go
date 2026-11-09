package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/rolemail"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wsmgr"
)

// internal-mail / internal-inbox let the Commander/Manager agents send and read
// mail in ROLE terms ("manager-commander") while the am server only knows
// random names ("GoldTiger"). cmdr resolves role->name via state.db and talks to
// the am server over HTTP (the `am` CLI can't run alongside the server). See
// docs/agent-mail-routing.md.

// amAgentStore is the slice of *store.Store these subcommands use.
type amAgentStore = rolemail.Store

// amMailer is the slice of *agentmail.ServerClient these subcommands use.
type amMailer interface {
	EnsureProject(ctx context.Context, humanKey string) (string, error)
	RegisterAgent(ctx context.Context, projectKey, program, model, task string) (string, error)
	SendMessage(ctx context.Context, projectKey, sender string, to []string, subject, body string, opts agentmail.SendOptions) error
	FetchInbox(ctx context.Context, projectKey, agent string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error)
}

func resolveOrRegister(ctx context.Context, st amAgentStore, m amMailer, amProjectKey, role string, autoRegister bool) (string, error) {
	return rolemail.Resolve(ctx, st, m, amProjectKey, role, autoRegister)
}

func sendRoleMail(ctx context.Context, st amAgentStore, m amMailer, amProjectKey, fromRole string, toRoles []string, subject, body string, opts agentmail.SendOptions) error {
	return rolemail.Send(ctx, st, m, amProjectKey, fromRole, toRoles, subject, body, opts)
}

// fetchRoleInbox resolves the agent role to an am name and fetches its inbox.
func fetchRoleInbox(ctx context.Context, st amAgentStore, m amMailer, amProjectKey, agentRole string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error) {
	name, err := resolveOrRegister(ctx, st, m, amProjectKey, agentRole, true)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}
	return m.FetchInbox(ctx, amProjectKey, name, since, opts)
}

// mailDeps wires the real store + am server client and the fleet am project key.
// Overridable in tests.
var mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("open store: %w", err)
	}
	client, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		_ = st.Close()
		return nil, nil, "", nil, fmt.Errorf("agent-mail client: %w", err)
	}
	return st, client, resolveCommanderRepo(), func() { _ = st.Close() }, nil
}

func runInternalMail(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-mail", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "sender role (e.g. commander, manager-commander)")
	to := fs.String("to", "", "recipient role(s), comma-separated")
	existing := fs.String("to-existing-agent", "", "exact existing external AM name in the fleet project (exclusive with --to)")
	subject := fs.String("subject", "", "subject line")
	body := fs.String("body", "", "message body (markdown)")
	importance := fs.String("importance", "", "low|normal|high|urgent")
	ack := fs.Bool("ack", false, "request acknowledgement")
	interrupt := fs.Bool("interrupt", false, "stop each recipient's current turn so it reads this next (a working Codex Operator cannot read mail: its run is ended)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	toRoles := splitRoles(*to)
	if *from == "" || (len(toRoles) == 0 && *existing == "") || *subject == "" {
		emitErr(stderr, "missing required flag(s): --from, --to or --to-existing-agent, --subject")
		return 1
	}
	if *existing != "" && (*to != "" || *interrupt || strings.TrimSpace(*existing) != *existing || strings.ContainsAny(*existing, ",\r\n\t ")) {
		emitErr(stderr, "--to-existing-agent requires one exact name, cannot combine with --to or --interrupt")
		return 1
	}
	if *existing != "" {
		if err := existingAgentCaller(*from); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}

	st, client, amProjectKey, cleanup, err := mailDeps()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *existing != "" {
		recipient, err := sendExistingAgentMail(ctx, st, client, amProjectKey, *from, *existing, *subject, *body, agentmail.SendOptions{Importance: *importance, AckRequired: *ack})
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(map[string]any{"sent": true, "from": *from, "to_existing_agent": recipient.Name, "recipient_id": recipient.ID, "project_id": recipient.ProjectID, "project_key": amProjectKey, "subject": *subject}); err != nil {
			emitErr(stderr, fmt.Sprintf("encode result: %v", err))
			return 1
		}
		return 0
	}

	toRoles, err = canonicalRecipients(ctx, st, amProjectKey, toRoles, stderr)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	toRoles = rerouteOperatorMail(ctx, st, *from, toRoles, *importance, stderr)
	toRoles, ccRole, movedTo, rerouteRun := rerouteStaleManager(ctx, st, *from, toRoles, stderr)
	startMissingManagers(ctx, st, amProjectKey, toRoles, stderr)

	if err := sendRoleMail(ctx, st, client, amProjectKey, *from, toRoles, *subject, *body, agentmail.SendOptions{
		Importance:  *importance,
		AckRequired: *ack,
	}); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if ccRole != "" {
		// The Manager the Operator still names hears once per run that its reports
		// now go elsewhere, after the owner has them; a failed copy is tried again
		// with the run's next mail.
		if err := sendRoleMail(ctx, st, client, amProjectKey, *from, []string{ccRole}, "[cc: moved to "+movedTo+"] "+*subject, *body, agentmail.SendOptions{Importance: *importance}); err != nil {
			fmt.Fprintf(stderr, "warning: copy to %s not sent: %v\n", ccRole, err)
		} else {
			markReroute(rerouteRun, ccRole)
		}
	}
	result := map[string]any{"sent": true, "from": *from, "to": toRoles, "subject": *subject}
	home := appCommanderHome()
	oneShot, rest := splitCodexOperators(ctx, st, home, toRoles)
	if *interrupt {
		stopped, failed := stopCodexOperators(ctx, oneShot, *from, stderr)
		result["stopped"] = stopped
		if len(failed) > 0 {
			result["unreachable"] = failed
		}
		result["interrupted"] = interruptRoles(ctx, home, rest, stderr)
	} else if len(oneShot) > 0 {
		names := make([]string, len(oneShot))
		for i, run := range oneShot {
			names[i] = run.AgentName
		}
		result["unreachable"] = names
		fmt.Fprintf(stderr, "warning: %s is a working Codex Operator and will not read this mail: it has no mail hook and takes no further turns. Use --interrupt to stop it now, then respawn with the new direction.\n", strings.Join(names, ", "))
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// canonicalRecipients maps an Operator recipient written with dots or dashes
// to the role actually registered (rolemail.Registered: as written, else any
// registered operator-* role with the same canonical spelling). One that
// matches nothing fails here, so mail is never reported sent without a receipt;
// the hint names a real registered candidate only.
func canonicalRecipients(ctx context.Context, st amAgentStore, amProjectKey string, toRoles []string, stderr io.Writer) ([]string, error) {
	out := make([]string, len(toRoles))
	for i, r := range toRoles {
		out[i] = r
		if !strings.HasPrefix(r, "operator-") {
			continue
		}
		reg, _, err := rolemail.Registered(ctx, st, amProjectKey, r)
		if err != nil {
			return nil, fmt.Errorf("look up role %q: %w", r, err)
		}
		if reg == "" {
			return nil, fmt.Errorf("no am agent registered for role %q (check mail_role in internal-spawn-operator's output)", r)
		}
		if reg != r {
			out[i] = reg
			fmt.Fprintf(stderr, "note: role %s is registered as %s\n", r, reg)
		}
	}
	return out, nil
}

// agentRunLookup is what the reroute needs from the store (the real one has it).
type agentRunLookup interface {
	GetAgentRunByAgentName(ctx context.Context, agentName string) (*store.AgentRun, error)
}

// rerouteOperatorMail is the safety net for an Operator that reports to
// Commander instead of its Manager: mail from operator-* to "commander" goes to
// the Manager of the task's workstream instead. Only urgent mail still copies
// Commander. Anything it cannot resolve is sent as written.
func rerouteOperatorMail(ctx context.Context, st amAgentStore, from string, toRoles []string, importance string, stderr io.Writer) []string {
	if !strings.HasPrefix(from, "operator-") || !slices.Contains(toRoles, "commander") {
		return toRoles
	}
	runs, ok := st.(agentRunLookup)
	if !ok {
		return toRoles
	}
	run, err := runs.GetAgentRunByAgentName(ctx, from)
	if (err != nil || run == nil) && spawn.CanonicalRole(from) != from {
		run, err = runs.GetAgentRunByAgentName(ctx, spawn.CanonicalRole(from))
	}
	if err != nil || run == nil {
		return toRoles
	}
	dir := ""
	if projects, ok := st.(interface {
		GetProject(ctx context.Context, id int64) (*store.Project, error)
	}); ok {
		if p, err := projects.GetProject(ctx, run.ProjectID); err == nil && p != nil {
			dir = p.Path
		}
	}
	manager := spawn.ManagerRole(ctx, newBeadsClientFn(), dir, run.TaskBeadID)
	if manager == "" {
		return toRoles
	}
	out := []string{}
	for _, r := range toRoles {
		if r == "commander" {
			if importance == "urgent" {
				out = append(out, r)
			}
			r = manager
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	fmt.Fprintf(stderr, "note: %s reports to its Manager; mail to commander rerouted to %s\n", from, manager)
	return out
}

// rerouteStaleManager sends an Operator's mail for a Manager of another bead of
// its task's lineage to the task's current owner (StrictOwner: the nearest owner
// with the project's nested Managers on, else the root). Both ways: a running
// Operator spawned before nesting came on still names the root, and one spawned
// while it was on still names its nested Manager after --undo. Roles match in
// either spelling. The old Manager gets one copy per run (ccRole; the caller
// sends it after the main send and marks it only once sent, markReroute), so it
// learns where the reports went. Anything it cannot read is sent as written.
func rerouteStaleManager(ctx context.Context, st amAgentStore, from string, toRoles []string, stderr io.Writer) (out []string, ccRole, owner string, runID int64) {
	if !strings.HasPrefix(from, "operator-") || !slices.ContainsFunc(toRoles, func(r string) bool { return strings.HasPrefix(r, "manager-") }) {
		return toRoles, "", "", 0
	}
	run, dir := operatorRunAndDir(ctx, st, from)
	if run == nil || dir == "" {
		return toRoles, "", "", 0
	}
	bc := newBeadsClientFn()
	nearest, err := wsmgr.StrictOwner(ctx, bc, dir, run.TaskBeadID)
	if err != nil {
		return toRoles, "", "", 0
	}
	owner = wsmgr.Role(nearest)
	var others []string
	for _, id := range beads.Lineage(ctx, bc, dir, nil, run.TaskBeadID) {
		if id != nearest {
			others = append(others, wsmgr.Role(id))
		}
	}
	out = []string{}
	for _, r := range toRoles {
		if !roleAmong(r, []string{owner}) && roleAmong(r, others) {
			fmt.Fprintf(stderr, "note: %s now reports to %s (the Manager that owns its task); mail to %s rerouted there\n", from, owner, r)
			if ccRole == "" && !rerouteMarked(run.ID, r) {
				ccRole = r
			}
			r = owner
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, ccRole, owner, run.ID
}

// operatorRunAndDir finds an Operator's run and its project path.
func operatorRunAndDir(ctx context.Context, st amAgentStore, from string) (*store.AgentRun, string) {
	runs, ok := st.(agentRunLookup)
	if !ok {
		return nil, ""
	}
	run, err := runs.GetAgentRunByAgentName(ctx, from)
	if (err != nil || run == nil) && spawn.CanonicalRole(from) != from {
		run, err = runs.GetAgentRunByAgentName(ctx, spawn.CanonicalRole(from))
	}
	if err != nil || run == nil {
		return nil, ""
	}
	if projects, ok := st.(interface {
		GetProject(ctx context.Context, id int64) (*store.Project, error)
	}); ok {
		if p, err := projects.GetProject(ctx, run.ProjectID); err == nil && p != nil {
			return run, p.Path
		}
	}
	return run, ""
}

func rerouteMarker(runID int64, role string) string {
	return filepath.Join(appCommanderHome(), "mail-reroutes", fmt.Sprintf("%d-%s", runID, spawn.CanonicalRole(role)))
}

// rerouteMarked: run's mail to role was already moved and its copy sent.
func rerouteMarked(runID int64, role string) bool {
	_, err := os.Stat(rerouteMarker(runID, role))
	return err == nil
}

// markReroute records that run's copy to role went out, so it is sent once.
func markReroute(runID int64, role string) {
	path := rerouteMarker(runID, role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, nil, 0o644)
	}
}

// interruptRoles stops each role's current turn through cmdrd. The turn ends,
// the chat goes idle, and the wake loop hands it the mail as its next message.
// Mail is already sent, so a role that cannot be interrupted (no native chat,
// cmdrd down) is a warning, not a failure.
func interruptRoles(ctx context.Context, home string, roles []string, stderr io.Writer) []string {
	raw, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		fmt.Fprintf(stderr, "warning: not interrupted, cmdrd has no api-url: %v\n", err)
		return []string{}
	}
	base := strings.TrimRight(strings.TrimSpace(string(raw)), "/")
	token, _ := os.ReadFile(filepath.Join(home, "api-token"))
	done := []string{}
	for _, role := range roles {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat/"+url.PathEscape(role)+"/interrupt", nil)
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s not interrupted: %v\n", role, err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s not interrupted: %v\n", role, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(stderr, "warning: %s not interrupted (it reads the mail at its next break): %s\n", role, strings.TrimSpace(string(body)))
			continue
		}
		done = append(done, role)
	}
	return done
}

// stopHostRun ends a host Operator's run. Overridable in tests.
var stopHostRun = func(ctx context.Context, run store.AgentRun, by string) error {
	return spawn.StopHostRun(ctx, tmuxctl.New(), run, by)
}

// splitCodexOperators splits the recipients into the running host Operators on
// Codex, and everyone else. Such a run is one turn with no mail hook (Codex's
// hooks fire only at a turn's start and end, and a one-shot bridge takes no
// message after the prompt), so neither mail nor cmdrd's chat interrupt reaches
// it. A run it cannot look up counts as everyone else.
func splitCodexOperators(ctx context.Context, st amAgentStore, home string, toRoles []string) ([]store.AgentRun, []string) {
	runs, ok := st.(agentRunLookup)
	if !ok {
		return nil, toRoles
	}
	var oneShot []store.AgentRun
	rest := []string{}
	for _, role := range toRoles {
		var run *store.AgentRun
		if strings.HasPrefix(role, "operator-") {
			run, _ = runs.GetAgentRunByAgentName(ctx, role)
		}
		if run == nil || run.Status != "running" || !run.TmuxPaneID.Valid || run.TmuxPaneID.String == "" || chat.ReadRuntime(home, run.AgentName) != chat.RuntimeCodex {
			rest = append(rest, role)
			continue
		}
		oneShot = append(oneShot, *run)
	}
	return oneShot, rest
}

// callerIsOperator judges who is running this command; --from is whatever it
// typed, so an operator sender only adds to it. A host Operator's launcher sets
// its COMMANDER_ROLE (spawn.hostOperatorCommand); one started before it did has
// only COMMANDER_MANAGER_ROLE. A Manager's chat always has COMMANDER_ROLE (chat.Engine), and may still inherit
// COMMANDER_MANAGER_ROLE through a cmdrd started from an Operator's shell, so
// that variable counts only without a role.
func callerIsOperator(from string) bool {
	role := os.Getenv("COMMANDER_ROLE")
	if strings.HasPrefix(role, "operator-") || strings.HasPrefix(from, "operator-") {
		return true
	}
	return role == "" && os.Getenv("COMMANDER_MANAGER_ROLE") != ""
}

// stopCodexOperators ends the runs, and says what became of each: the mail sent
// with the stop stays unread. An Operator may not end another's run.
func stopCodexOperators(ctx context.Context, runs []store.AgentRun, from string, stderr io.Writer) (stopped, failed []string) {
	stopped, failed = []string{}, []string{}
	byOperator := callerIsOperator(from)
	for _, run := range runs {
		var err error
		if byOperator {
			err = errors.New("an Operator cannot stop another Operator")
		} else {
			err = stopHostRun(ctx, run, from)
		}
		if err != nil {
			failed = append(failed, run.AgentName)
			fmt.Fprintf(stderr, "warning: %s NOT stopped and will not read this mail: %v\n", run.AgentName, err)
			continue
		}
		stopped = append(stopped, run.AgentName)
		fmt.Fprintf(stderr, "note: %s stopped: its process has ended and it will not read this mail. cmdrd hands its step back to its Manager within a minute; respawn with the new direction in the brief after cmdrd's 'Operator stopped' mail (a spawn on its branch is refused until then).\n", run.AgentName)
	}
	return stopped, failed
}

func runInternalInbox(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "this agent's role (e.g. commander, manager-commander)")
	sinceStr := fs.String("since", "", "ISO-8601 lower bound (optional)")
	limit := fs.Int("limit", 0, "max messages (0 = server default)")
	bodies := fs.Bool("bodies", true, "include message bodies (--bodies=false for subjects only)")
	unread := fs.Bool("unread", false, "only mail newer than this agent's read cursor")
	markRead := fs.Bool("mark-read", false, "advance the read cursor past the mail printed")
	brief := fs.Bool("brief", false, "one readable line per message: id, from, subject, first line of the body (no JSON)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *agent == "" {
		emitErr(stderr, "missing required flag: --agent")
		return 1
	}
	var since time.Time
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			emitErr(stderr, fmt.Sprintf("parse --since: %v", err))
			return 1
		}
		since = t
	}

	st, client, amProjectKey, cleanup, err := mailDeps()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := agentmail.InboxOptions{Limit: *limit, IncludeBodies: *bodies}
	var mail []agentmail.Mail
	if *unread {
		mail, err = mailstate.Unread(ctx, roleInbox(st, client, amProjectKey, since, opts), *agent)
	} else {
		mail, err = fetchRoleInbox(ctx, st, client, amProjectKey, *agent, since, opts)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if mail == nil {
		mail = []agentmail.Mail{} // print [] rather than null
	}
	if *brief {
		for _, m := range mail {
			fmt.Fprintln(stdout, briefLine(m))
		}
	} else if err := json.NewEncoder(stdout).Encode(mail); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	if *markRead {
		if err := mailstate.MarkAllRead(*agent, mail); err != nil {
			emitErr(stderr, fmt.Sprintf("mark read: %v", err))
			return 1
		}
	}
	return 0
}

// roleInbox adapts fetchRoleInbox to the mailstate.Fetcher interface.
func roleInbox(st amAgentStore, m amMailer, amProjectKey string, since time.Time, opts agentmail.InboxOptions) mailstate.Fetcher {
	return mailstate.FetchFunc(func(ctx context.Context, role string) ([]agentmail.Mail, error) {
		return fetchRoleInbox(ctx, st, m, amProjectKey, role, since, opts)
	})
}

// splitRoles parses a comma-separated role list, trimming blanks.
func splitRoles(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if r := strings.TrimSpace(part); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// workstreamForManagerRole maps manager-<bead id> to its project, via the
// Workstreams numbering. Overridable in tests.
var workstreamForManagerRole = func(ctx context.Context, role string) (project, beadID string, ok bool) {
	bead := strings.TrimPrefix(role, "manager-")
	rows, err := tui.ListNumberedWorkstreams(ctx)
	if err != nil {
		return "", "", false
	}
	for _, r := range rows {
		if r.BeadID == bead {
			return r.Project, r.BeadID, true
		}
	}
	// A nested workstream's Manager, in a project whose nested Managers are on:
	// only when the bead is its own owner (an open kind:workstream bead).
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Project] {
			continue
		}
		seen[r.Project] = true
		p, err := resolveProject(r.Project)
		if err != nil || !beads.NestedManagersOn(p.Path) {
			continue
		}
		if owner, err := wsmgr.StrictOwner(ctx, newBeadsClientFn(), p.Path, bead); err == nil && owner == bead {
			return r.Project, bead, true
		}
	}
	return "", "", false
}

// startMissingManagers starts the Manager of any workstream addressed by role
// that has no mail registration yet, so mail to a not-yet-running Manager
// brings it up instead of failing. Best effort: send reports the real error.
func startMissingManagers(ctx context.Context, st amAgentStore, amProjectKey string, toRoles []string, stderr *os.File) {
	for _, role := range toRoles {
		if !strings.HasPrefix(role, "manager-") {
			continue
		}
		if name, err := st.GetAmAgent(ctx, amProjectKey, role); err != nil || name != "" {
			continue
		}
		project, bead, ok := workstreamForManagerRole(ctx, role)
		if !ok {
			continue
		}
		opener, cleanup, err := newWorkstreamOpener()
		if err != nil {
			fmt.Fprintf(stderr, "warning: could not start Manager %s: %v\n", role, err)
			continue
		}
		if _, err := opener.Open(ctx, project, bead, wsmgr.OpenOptions{}); err != nil {
			fmt.Fprintf(stderr, "warning: could not start Manager %s: %v\n", role, err)
		}
		cleanup()
	}
}

// briefLine is "id  from  subject — first body line" for --brief.
func briefLine(m agentmail.Mail) string {
	first := ""
	for _, l := range strings.Split(m.Body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			first = l
			break
		}
	}
	if len(first) > 160 {
		first = first[:160] + "…"
	}
	line := fmt.Sprintf("%s  %s  %s", m.ID, m.From, strings.Join(strings.Fields(m.Subject), " "))
	if first != "" {
		line += " — " + first
	}
	return line
}
