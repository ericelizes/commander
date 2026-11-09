package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

const tabUsage = `usage:
  cmdr tab add  (--url U | --cmd "..." | --decision STEP) [--title T] [--ws BEAD --project P] [--focus] [--refresh 30s]
                [--renderer browser]   (a picture of the page via headless Chrome)
  cmdr tab rm   <n|#id|url>  [--ws BEAD --project P]
  cmdr tab ls   [--json]     [--ws BEAD --project P]
  cmdr tab focus <n|#id|url> [--ws BEAD --project P]
  cmdr tab move <n|#id|url> <pos> [--ws BEAD --project P]

Without --ws/--project the tab list is the one of the window you run this in.
<n> is the tab's number in that window (the digit on the tab); #id is its row id.`

// tabCallerWS returns the workstream bead id of the window the caller runs in
// ($TMUX_PANE -> @cmdr_ws), or "" outside tmux / in a non-workstream window.
// Tests replace it.
var tabCallerWS = func() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{@cmdr_ws}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tabWorkstream is the workstream that owns ws (a workstream or one of its
// steps), by real parent edges. Without --project, the project is the one of
// the workstream whose id ws extends: bead ids carry their project's prefix.
func tabWorkstream(ctx context.Context, st *store.Store, projectName, ws string) string {
	var projectID int64
	if managers, err := st.ListWorkstreamManagers(ctx); err == nil {
		for _, m := range managers {
			if m.WorkstreamBeadID == ws {
				return ws
			}
			if strings.HasPrefix(ws, m.WorkstreamBeadID+".") {
				projectID = m.ProjectID
			}
		}
	}
	var p *store.Project
	if projectName != "" {
		p, _ = st.GetProjectByName(ctx, projectName)
	} else if projectID != 0 {
		p, _ = st.GetProject(ctx, projectID)
	}
	if p == nil {
		return wsmgr.Owner(ctx, nil, "", ws)
	}
	return wsmgr.Owner(ctx, newBeadsClientFn(), p.Path, ws)
}

// runTab is `cmdr tab add|rm|ls|focus|move`.
func runTab(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, tabUsage)
		return 2
	}
	sub := args[0]
	flags, pos, err := splitTabArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab %s: %v\n", sub, err)
		return 2
	}

	statePath, err := stateDBPath()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab: resolve state dir: %v\n", err)
		return 1
	}
	st, err := store.Open(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab: open store: %v\n", err)
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws, projectName := flags["ws"], flags["project"]
	if ws == "" && projectName == "" {
		ws = tabCallerWS()
	}
	if ws != "" {
		ws = tabWorkstream(ctx, st, projectName, ws)
	}
	projectID, ws, err := st.InspectorWindow(ctx, projectName, ws)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab %s: %v\n", sub, err)
		return 1
	}
	w := tabWindow{st: st, projectID: projectID, ws: ws}

	switch sub {
	case "add":
		return w.add(ctx, flags, stdout, stderr)
	case "ls", "list":
		return w.ls(ctx, flags["json"] == "true", stdout, stderr)
	case "rm", "remove":
		return w.withTab(ctx, pos, 1, "rm", stdout, stderr, func(tab store.InspectorTab) error {
			if err := st.RemoveTab(ctx, tab.ID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "removed tab %s\n", tab.Title)
			return nil
		})
	case "focus":
		return w.withTab(ctx, pos, 1, "focus", stdout, stderr, func(tab store.InspectorTab) error {
			if err := st.SetActiveTab(ctx, projectID, ws, tab.ID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "focused tab %s\n", tab.Title)
			return nil
		})
	case "move":
		return w.withTab(ctx, pos, 2, "move", stdout, stderr, func(tab store.InspectorTab) error {
			to, err := strconv.Atoi(pos[1])
			if err != nil || to < 1 {
				return fmt.Errorf("position must be a number >= 1, got %q", pos[1])
			}
			if err := st.MoveTab(ctx, tab.ID, to); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "moved tab %s to %d\n", tab.Title, to)
			return nil
		})
	}
	fmt.Fprintf(stderr, "cmdr tab: unknown subcommand %q\n%s\n", sub, tabUsage)
	return 2
}

type tabWindow struct {
	st        *store.Store
	projectID int64
	ws        string
}

func (w tabWindow) add(ctx context.Context, flags map[string]string, stdout, stderr io.Writer) int {
	target, kind := flags["url"], inspector.KindURL
	if step := flags["decision"]; step != "" {
		if target != "" || flags["cmd"] != "" {
			fmt.Fprintln(stderr, "cmdr tab add: --decision stands alone (no --url/--cmd)")
			return 2
		}
		// A decision card for a flagged step; the inspector draws it as a form.
		target, kind = step, inspector.KindDecision
	}
	if cmd := flags["cmd"]; cmd != "" && kind != inspector.KindDecision {
		if target != "" {
			fmt.Fprintln(stderr, "cmdr tab add: pass --url or --cmd, not both")
			return 2
		}
		// Commander's own internal commands are plumbing, not something to watch:
		// a decision shows as its card, never as its raw command.
		if strings.HasPrefix(strings.TrimSpace(cmd), "cmdr internal-") {
			fmt.Fprintln(stderr, "cmdr tab add: --cmd cannot pin a cmdr internal command; show a decision with --decision <step>")
			return 2
		}
		target, kind = cmd, inspector.KindCmd
	}
	if target == "" {
		fmt.Fprintf(stderr, "cmdr tab add: --url or --cmd is required\n%s\n", tabUsage)
		return 2
	}
	title := flags["title"]
	if title == "" {
		title = inspector.DefaultTitle(kind, target)
	}
	refresh := inspector.DefaultRefreshSecs(kind, target, flags["renderer"])
	if v := flags["refresh"]; v != "" {
		secs, err := parseRefreshSecs(v)
		if err != nil {
			fmt.Fprintf(stderr, "cmdr tab add: --refresh: %v\n", err)
			return 2
		}
		refresh = secs
	}
	addedBy := flags["by"]
	if addedBy == "" {
		addedBy = "user"
		if w.ws != "" {
			addedBy = wsmgr.Role(w.ws)
		}
	}
	id, created, err := w.st.AddTab(ctx, store.InspectorTab{
		ProjectID: w.projectID, WorkstreamBeadID: w.ws, Title: title, Kind: kind, Target: target,
		Renderer: flags["renderer"], RefreshSecs: refresh, AddedBy: addedBy,
	})
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab add: %v\n", err)
		return 1
	}
	if flags["focus"] == "true" {
		if err := w.st.SetActiveTab(ctx, w.projectID, w.ws, id); err != nil {
			fmt.Fprintf(stderr, "cmdr tab add: focus: %v\n", err)
			return 1
		}
	}
	verb := "added"
	if !created {
		verb = "already there"
	}
	fmt.Fprintf(stdout, "tab #%d %s: %s\n", id, verb, title)
	return 0
}

type tabJSON struct {
	ID          int64  `json:"id"`
	Position    int    `json:"position"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	Renderer    string `json:"renderer"`
	RefreshSecs int    `json:"refresh_secs"`
	AddedBy     string `json:"added_by"`
	Active      bool   `json:"active"`
}

func (w tabWindow) ls(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	tabs, err := w.st.ListTabs(ctx, w.projectID, w.ws)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab ls: %v\n", err)
		return 1
	}
	active, _ := w.st.GetActiveTab(ctx, w.projectID, w.ws)
	if active == 0 && len(tabs) > 0 {
		active = tabs[0].ID
	}
	if asJSON {
		out := make([]tabJSON, 0, len(tabs))
		for _, t := range tabs {
			out = append(out, tabJSON{t.ID, t.Position, t.Title, t.Kind, t.Target, t.Renderer, t.RefreshSecs, t.AddedBy, t.ID == active})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "cmdr tab ls: %v\n", err)
			return 1
		}
		return 0
	}
	for _, t := range tabs {
		mark := " "
		if t.ID == active {
			mark = "*"
		}
		fmt.Fprintf(stdout, "%s%d  #%d  %s  %s\n", mark, t.Position, t.ID, t.Title, t.Target)
	}
	return 0
}

// withTab resolves the first positional (n, #id or url/command) to a tab in
// this window and runs fn on it. want is the number of positionals expected.
func (w tabWindow) withTab(ctx context.Context, pos []string, want int, sub string, _, stderr io.Writer, fn func(store.InspectorTab) error) int {
	if len(pos) != want {
		fmt.Fprintf(stderr, "cmdr tab %s: expected %d argument(s)\n%s\n", sub, want, tabUsage)
		return 2
	}
	tabs, err := w.st.ListTabs(ctx, w.projectID, w.ws)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr tab %s: %v\n", sub, err)
		return 1
	}
	tab, ok := findTab(tabs, pos[0])
	if !ok {
		fmt.Fprintf(stderr, "cmdr tab %s: no tab %q in this window (see `cmdr tab ls`)\n", sub, pos[0])
		return 1
	}
	if err := fn(tab); err != nil {
		fmt.Fprintf(stderr, "cmdr tab %s: %v\n", sub, err)
		return 1
	}
	return 0
}

// findTab matches "#12" by row id, "3" by position and anything else by exact
// target.
func findTab(tabs []store.InspectorTab, ref string) (store.InspectorTab, bool) {
	if strings.HasPrefix(ref, "#") {
		if id, err := strconv.ParseInt(ref[1:], 10, 64); err == nil {
			for _, t := range tabs {
				if t.ID == id {
					return t, true
				}
			}
		}
		return store.InspectorTab{}, false
	}
	if n, err := strconv.Atoi(ref); err == nil {
		for _, t := range tabs {
			if t.Position == n {
				return t, true
			}
		}
		return store.InspectorTab{}, false
	}
	for _, t := range tabs {
		if t.Target == ref {
			return t, true
		}
	}
	return store.InspectorTab{}, false
}

// parseRefreshSecs accepts "30s", "2m" or a bare number of seconds.
func parseRefreshSecs(v string) (int, error) {
	if n, err := strconv.Atoi(v); err == nil {
		if n < 1 {
			return 0, fmt.Errorf("must be at least 1 second")
		}
		return n, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second {
		return 0, fmt.Errorf("want a duration like 30s or 2m, got %q", v)
	}
	return int(d.Seconds()), nil
}

var tabBoolFlags = map[string]bool{"focus": true, "json": true}
var tabValueFlags = map[string]bool{"url": true, "cmd": true, "title": true, "ws": true, "project": true, "refresh": true, "renderer": true, "by": true, "decision": true}

// splitTabArgs separates --flag [value] pairs from positionals, in any order
// (the stdlib flag package stops at the first positional, which would make
// `cmdr tab rm 2 --ws X` ignore --ws).
func splitTabArgs(args []string) (map[string]string, []string, error) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		value, hasValue := "", false
		if eq := strings.Index(name, "="); eq >= 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		switch {
		case tabBoolFlags[name]:
			flags[name] = "true"
			if hasValue {
				flags[name] = value
			}
		case tabValueFlags[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				value = args[i]
			}
			flags[name] = value
		default:
			return nil, nil, fmt.Errorf("unknown flag %s", a)
		}
	}
	return flags, pos, nil
}
