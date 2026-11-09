package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// nestPlanOwner is one nested workstream in the cutover plan.
type nestPlanOwner struct {
	BeadID     string         `json:"bead_id"`
	Title      string         `json:"title"`
	Role       string         `json:"manager_role"`
	Parent     string         `json:"parent_role"`
	Root       string         `json:"root_role"`
	StandIn    bool           `json:"stand_in,omitempty"` // one nested child stands in for it: registered, started only by mail
	Registered bool           `json:"registered"`
	Operators  []nestPlanRun  `json:"operators"`   // running Operators whose Manager changes
	Cards      []string       `json:"cards"`       // open NEEDS YOU cards it takes over
	Waits      []string       `json:"waits"`       // recorded waits it takes over
	RootUnread []nestPlanMail `json:"root_unread"` // the root's unread mail naming its beads: listed, never moved or marked read
}

type nestPlanRun struct {
	Agent      string `json:"agent"`
	Task       string `json:"task"`
	OldManager string `json:"old_manager"`
}

type nestPlanMail struct {
	Inbox   string `json:"inbox,omitempty"` // --undo: the nested Manager whose inbox holds it
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
}

type nestResult struct {
	Project      string          `json:"project"`
	Mode         string          `json:"mode"` // dry-run | apply | undo | undo-dry-run
	SwitchBefore bool            `json:"switch_before"`
	SwitchAfter  bool            `json:"switch_after"`
	Owners       []nestPlanOwner `json:"owners"`
	NestedUnread []nestPlanMail  `json:"nested_unread,omitempty"` // --undo: mail left in nested inboxes, for the roots
	Mailed       []string        `json:"mailed,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
}

// runInternalNestManagers turns a project's nested Managers on (--apply) or off
// (--undo), printing the plan; with neither it is a dry run. Commander and the
// user only. Apply registers every nested Manager and writes its mail cursor
// BEFORE the switch flips, so no mail is ever routed to a Manager that does not
// exist and none reads as history; then flips, starts each nested Manager with a
// handover mail and tells each root Manager. Nothing in any inbox is moved or
// marked read. Undo flips the switch back, tells each nested Manager that its
// work is its root's again (do not spawn: the spawn door now refuses it) and
// each root what returns to it, with only its own nested Managers' unread mail;
// an Operator still naming a nested Manager has its mail rerouted to the root
// (internal-mail). Registrations and sessions stay for a later --apply.
func runInternalNestManagers(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-nest-managers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectName := fs.String("project", "", "project slug")
	apply := fs.Bool("apply", false, "turn nested Managers on (default: dry run)")
	undo := fs.Bool("undo", false, "turn nested Managers off again (with --apply; alone: what --undo --apply would do)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" {
		emitErr(stderr, "missing required flag: --project")
		return 1
	}
	if role := os.Getenv("COMMANDER_ROLE"); role != "" && role != "commander" {
		emitErr(stderr, "only Commander or the user turns a project's nested Managers on or off: ask Commander")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := nestManagers(ctx, project, *apply, *undo)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

func nestManagers(ctx context.Context, project *store.Project, apply, undo bool) (*nestResult, error) {
	res := &nestResult{Project: project.Name, Mode: "dry-run", SwitchBefore: beads.NestedManagersOn(project.Path)}
	switch {
	case apply && undo:
		res.Mode = "undo"
	case undo:
		res.Mode = "undo-dry-run"
	case apply:
		res.Mode = "apply"
	}
	warn := func(s string) { res.Warnings = append(res.Warnings, s) }
	assume := beads.AssumeNestedManagers(ctx)
	bc := freshReader(newBeadsClientFn())
	owners, err := wsmgr.NestedOwners(ctx, bc, *project)
	if err != nil {
		return nil, err
	}
	plan, err := nestPlan(assume, bc, project, owners, warn)
	if err != nil {
		return nil, err
	}
	res.Owners = plan
	if len(plan) == 0 {
		warn("no open nested kind:workstream bead: nothing would get a Manager of its own (a legacy kind:track bead never does; migrate it first)")
	}
	switch res.Mode {
	case "dry-run":
		res.SwitchAfter = res.SwitchBefore
		return res, nil
	case "undo-dry-run", "undo":
		res.NestedUnread = nestedUnread(ctx, plan, warn)
		if res.Mode == "undo-dry-run" {
			res.SwitchAfter = res.SwitchBefore
			return res, nil
		}
		if !res.SwitchBefore {
			// A rerun after a finished undo: the notices went out the first time.
			warn("nested Managers were already off: nothing changed and nobody was mailed again")
			return res, nil
		}
		if err := beads.SetNestedManagers(project.Path, false); err != nil {
			return nil, fmt.Errorf("switch off: %w", err)
		}
		res.SwitchAfter = false
		res.Mailed = tellRootsUndo(ctx, plan, res.NestedUnread, warn)
		return res, nil
	}

	// 1. Register before routing: every nested Manager exists, with a mail
	// cursor, before anything can be routed to it.
	opener, cleanup, err := newWorkstreamOpener()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	for _, o := range plan {
		t := wsmgr.Target{Project: *project, BeadID: o.BeadID, Title: o.Title}
		if _, err := opener.Prepare(assume, t); err != nil {
			return nil, fmt.Errorf("registering %s failed: %w; the switch was not flipped and nothing was routed", o.Role, err)
		}
	}
	if res.SwitchBefore {
		// A rerun after a finished apply registers anything new (idempotent) but
		// does not hand every Manager its handover and every root its notice again.
		res.SwitchAfter = true
		warn("nested Managers were already on: registrations checked, nobody was mailed again (a nested workstream added since gets its Manager from cmdrd)")
		return res, nil
	}
	// 2. Flip: from here every resolver returns the nested owner.
	if err := beads.SetNestedManagers(project.Path, true); err != nil {
		return nil, fmt.Errorf("switch on: %w (every nested Manager is registered; rerun --apply)", err)
	}
	res.SwitchAfter = true
	// 3. Start each nested Manager (stand-ins wait for mail) with a handover.
	st, client, key, done, mailErr := mailDeps()
	if mailErr == nil {
		defer done()
	}
	send := func(to, subject, body, importance string) {
		if mailErr != nil {
			warn(fmt.Sprintf("%s was not told: %v", to, mailErr))
			return
		}
		if err := sendRoleMail(ctx, st, client, key, "commander", []string{to}, subject, body, agentmail.SendOptions{Importance: importance}); err != nil {
			warn(fmt.Sprintf("%s was not told: %v", to, err))
			return
		}
		res.Mailed = append(res.Mailed, to)
	}
	for _, o := range plan {
		if !o.StandIn {
			if _, err := opener.Open(ctx, project.Name, o.BeadID, wsmgr.OpenOptions{}); err != nil {
				warn(fmt.Sprintf("%s did not start (mail starts it): %v", o.Role, err))
			}
		}
		send(o.Role, "You are the Manager of "+o.BeadID, handoverBody(o), "high")
	}
	// 4. Tell each root Manager what moved.
	for root, list := range byRoot(plan) {
		send(root, "Nested Managers are on in "+project.Name, rootNoticeBody(root, list), "high")
	}
	return res, nil
}

// nestPlan fills each owner's facts: who would own each bead with the switch on.
func nestPlan(assume context.Context, bc beads.Client, project *store.Project, owners []wsmgr.Target, warn func(string)) ([]nestPlanOwner, error) {
	if len(owners) == 0 {
		return nil, nil
	}
	kids := map[string]int{}
	for _, o := range owners {
		kids[o.Parent]++
	}
	all, err := bc.List(assume, project.Path, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("read the beads of %s: %w", project.Name, err)
	}
	known := beads.Parents{}
	byID := map[string]beads.Bead{}
	for _, b := range all {
		byID[b.ID] = b
		if p := listedBeadParent(b); p != "" {
			known[b.ID] = p
		}
	}
	labels := func(id string) (beads.Bead, bool) { b, ok := byID[id]; return b, ok }
	type ownership struct{ owner, root string }
	seen := map[string]ownership{}
	ownerOf := func(id string) (owner, root string) {
		if o, ok := seen[id]; ok {
			return o.owner, o.root
		}
		chain := beads.Lineage(assume, bc, project.Path, known, id)
		for i := 0; i+1 < len(chain); i++ {
			known[chain[i]] = chain[i+1] // what a read learned serves the next bead
		}
		if len(chain) == 0 {
			return "", ""
		}
		known[chain[len(chain)-1]] = ""
		o := ownership{beads.Owner(assume, bc, project.Path, known, labels, id), chain[len(chain)-1]}
		seen[id] = o
		return o.owner, o.root
	}
	st, done, err := needsYouStoreFn()
	if err != nil {
		return nil, err
	}
	defer done()
	idx := map[string]int{}
	out := make([]nestPlanOwner, len(owners))
	for i, o := range owners {
		_, root := ownerOf(o.BeadID)
		row, err := st.GetWorkstreamManager(assume, project.ID, o.BeadID)
		if err != nil {
			return nil, err
		}
		out[i] = nestPlanOwner{BeadID: o.BeadID, Title: o.Title, Role: wsmgr.Role(o.BeadID), Parent: wsmgr.Role(o.Parent), Root: wsmgr.Role(root),
			StandIn: kids[o.BeadID] == 1, Registered: row != nil, Operators: []nestPlanRun{}, Cards: []string{}, Waits: []string{}, RootUnread: []nestPlanMail{}}
		idx[o.BeadID] = i
	}
	for _, b := range all {
		owner, _ := ownerOf(b.ID)
		i, ok := idx[owner]
		if !ok || b.ID == owner || b.Status == "closed" {
			continue
		}
		if state, _ := beads.StepState(b.Status, b.Labels); state == beads.StateNeedsUser {
			out[i].Cards = append(out[i].Cards, b.ID)
		}
		if len(beads.WaitsOf(b.Labels)) > 0 {
			out[i].Waits = append(out[i].Waits, b.ID)
		}
	}
	runs, err := st.ListRunningRuns(assume)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.ProjectID != project.ID || r.TaskBeadID == "" {
			continue
		}
		owner, root := ownerOf(r.TaskBeadID)
		if i, ok := idx[owner]; ok {
			out[i].Operators = append(out[i].Operators, nestPlanRun{Agent: r.AgentName, Task: r.TaskBeadID, OldManager: wsmgr.Role(root)})
		}
	}
	// The root's unread mail that names a bead now owned by a nested Manager.
	subtree := map[string][]string{}
	for _, b := range all {
		if owner, _ := ownerOf(b.ID); owner != "" {
			if _, ok := idx[owner]; ok {
				subtree[owner] = append(subtree[owner], b.ID)
			}
		}
	}
	roots := map[string][]int{}
	for i, o := range out {
		roots[o.Root] = append(roots[o.Root], i)
	}
	for root, members := range roots {
		mails, err := unreadOf(assume, root)
		if err != nil {
			warn(fmt.Sprintf("could not read %s's unread mail: %v", root, err))
			continue
		}
		for _, m := range mails {
			text := m.Subject + "\n" + m.Body
			for _, i := range members {
				if slices.ContainsFunc(subtree[out[i].BeadID], func(id string) bool { return mentionsBead(text, id) }) {
					out[i].RootUnread = append(out[i].RootUnread, nestPlanMail{ID: m.ID, From: m.From, Subject: m.Subject})
				}
			}
		}
	}
	return out, nil
}

func listedBeadParent(b beads.Bead) string {
	if b.Parent != "" {
		return b.Parent
	}
	return b.ParentID
}

// mentionsBead: id appears in text as a whole id, not inside a longer one.
func mentionsBead(text, id string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], id)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(id)
		before := start == 0 || !beadIDRune(text[start-1])
		after := end == len(text) || !(beadIDRune(text[end]) || (text[end] == '.' && end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9'))
		if before && after {
			return true
		}
		i = end
	}
}

func beadIDRune(c byte) bool {
	return c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// unreadOf reads a role's unread mail without registering it or moving its cursor.
var unreadOf = func(ctx context.Context, role string) ([]agentmail.Mail, error) {
	st, client, key, done, err := mailDeps()
	if err != nil {
		return nil, err
	}
	defer done()
	name, err := resolveOrRegister(ctx, st, client, key, role, false)
	if err != nil || name == "" {
		return nil, err
	}
	mails, err := client.FetchInbox(ctx, key, name, time.Time{}, agentmail.InboxOptions{IncludeBodies: true})
	if err != nil {
		return nil, err
	}
	var out []agentmail.Mail
	for _, m := range mails {
		if mailstate.IsUnread(role, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

func nestedUnread(ctx context.Context, plan []nestPlanOwner, warn func(string)) []nestPlanMail {
	var out []nestPlanMail
	for _, o := range plan {
		mails, err := unreadOf(ctx, o.Role)
		if err != nil {
			warn(fmt.Sprintf("could not read %s's unread mail: %v", o.Role, err))
			continue
		}
		for _, m := range mails {
			out = append(out, nestPlanMail{Inbox: o.Role, ID: m.ID, From: m.From, Subject: "[" + o.Role + "] " + m.Subject})
		}
	}
	return out
}

func byRoot(plan []nestPlanOwner) map[string][]nestPlanOwner {
	out := map[string][]nestPlanOwner{}
	for _, o := range plan {
		out[o.Root] = append(out[o.Root], o)
	}
	return out
}

func handoverBody(o nestPlanOwner) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Nested Managers are on: you are the Manager of %s (%s). Your parent is %s. You own the beads whose nearest workstream is %s; until now %s owned them.\n", o.BeadID, o.Title, o.Parent, o.BeadID, o.Root)
	if o.StandIn {
		b.WriteString("Your only open child is a nested workstream with its own Manager, which stands in for you on the board: coordinate it.\n")
	}
	if len(o.Operators) > 0 {
		b.WriteString("\nRunning Operators that now report to you (their prompt still names the old Manager; their mail is rerouted to you):\n")
		for _, r := range o.Operators {
			fmt.Fprintf(&b, "- %s on %s (was %s)\n", r.Agent, r.Task, r.OldManager)
		}
	}
	if len(o.Cards) > 0 {
		fmt.Fprintf(&b, "\nOpen NEEDS YOU cards now yours: %s\n", strings.Join(o.Cards, ", "))
	}
	if len(o.Waits) > 0 {
		fmt.Fprintf(&b, "\nRecorded waits now yours (they fire to you): %s\n", strings.Join(o.Waits, ", "))
	}
	if len(o.RootUnread) > 0 {
		fmt.Fprintf(&b, "\nUnread mail in %s's inbox about your beads (a copy for context; %s still owns it and forwards it). Read with cmdr internal-inbox --agent %s:\n", o.Root, o.Root, o.Root)
		for _, m := range o.RootUnread {
			fmt.Fprintf(&b, "- %s from %s: %s\n", m.ID, m.From, m.Subject)
		}
	}
	b.WriteString("\nRead your workstream with cmdr board --role " + o.Role + " and carry it on.")
	return b.String()
}

func rootNoticeBody(root string, list []nestPlanOwner) string {
	var b strings.Builder
	b.WriteString("Nested Managers are on in this project. These workstreams under yours now have Managers of their own; their beads and Operators are theirs, and you coordinate them (cmdr board --role " + root + "):\n")
	for _, o := range list {
		fmt.Fprintf(&b, "- %s (%s): %s\n", o.BeadID, o.Title, o.Role)
		for _, r := range o.Operators {
			fmt.Fprintf(&b, "  Operator %s on %s now reports to %s\n", r.Agent, r.Task, o.Role)
		}
		for _, m := range o.RootUnread {
			fmt.Fprintf(&b, "  Forward, do not act on, your unread mail %s (%s) to %s, then mark it read yourself\n", m.ID, m.Subject, o.Role)
		}
	}
	return b.String()
}

func tellRootsUndo(ctx context.Context, plan []nestPlanOwner, unread []nestPlanMail, warn func(string)) []string {
	st, client, key, done, err := mailDeps()
	if err != nil {
		warn(fmt.Sprintf("roots and nested Managers were not told: %v", err))
		return nil
	}
	defer done()
	var mailed []string
	send := func(to, subject, body string) {
		if err := sendRoleMail(ctx, st, client, key, "commander", []string{to}, subject, body, agentmail.SendOptions{Importance: "high"}); err != nil {
			warn(fmt.Sprintf("%s was not told: %v", to, err))
			return
		}
		mailed = append(mailed, to)
	}
	roots := byRoot(plan)
	rootNames := make([]string, 0, len(roots))
	for root := range roots {
		rootNames = append(rootNames, root)
	}
	slices.Sort(rootNames)
	for _, root := range rootNames {
		list := roots[root]
		var b strings.Builder
		b.WriteString("Nested Managers are off again in this project: you own everything under your workstream once more, and these Managers' work returns to you (their sessions stay for a later switch-on; they were told to stop dispatching):\n")
		var mine []string
		for _, o := range list {
			fmt.Fprintf(&b, "- %s (%s): %s\n", o.BeadID, o.Title, o.Role)
			for _, r := range o.Operators {
				fmt.Fprintf(&b, "  Operator %s on %s reports to you again (its mail to %s is rerouted to you)\n", r.Agent, r.Task, o.Role)
			}
			mine = append(mine, o.Role)
		}
		var left []nestPlanMail
		for _, m := range unread {
			if slices.Contains(mine, m.Inbox) {
				left = append(left, m)
			}
		}
		if len(left) > 0 {
			b.WriteString("\nUnread mail left in their inboxes (read with cmdr internal-inbox --agent <role>; mark it read there once handled):\n")
			for _, m := range left {
				fmt.Fprintf(&b, "- %s from %s: %s\n", m.ID, m.From, m.Subject)
			}
		}
		send(root, "Nested Managers are off", b.String())
	}
	for _, o := range plan {
		body := fmt.Sprintf("Nested Managers are off again in this project: %s owns %s and everything under it once more. "+
			"Stop dispatching: do not spawn Operators (the spawn door refuses you now), raise cards or set waits on these beads. "+
			"Hand anything in flight to %s with cmdr internal-mail --to %s, then stop.", o.Root, o.BeadID, o.Root, o.Root)
		send(o.Role, "Nested Managers are off: "+o.BeadID+" returns to "+o.Root, body)
	}
	return mailed
}
