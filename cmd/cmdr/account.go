package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

const accountUsage = `usage: cmdr account list [--json]
       cmdr account verify <name>
       cmdr account add <name>
       cmdr account use <role> <name>
       cmdr account auto <role> [on|off]`

// runAccount is `cmdr account`: the Claude accounts chats and Operators run
// on. add only creates the config dir and prints the login line: the user
// logs in themselves.
func runAccount(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, accountUsage)
		return 2
	}
	home := commanderHomeDir()
	switch args[0] {
	case "list", "ls":
		asJSON := len(args) > 1 && args[1] == "--json"
		return printAccounts(stdout, chat.Accounts(home, chat.ReadUsage(home), time.Now()), asJSON, time.Now())
	case "verify":
		if len(args) != 2 {
			fmt.Fprintln(stderr, accountUsage)
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		v, err := claudeacct.Verify(ctx, home, args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: connected as %s; profile identity matches: %t\n", args[1], v.Email, v.Matches)
		if !v.Matches {
			return 1
		}
		return 0
	case "add":
		if len(args) != 2 {
			fmt.Fprintln(stderr, accountUsage)
			return 2
		}
		a, err := claudeacct.Create(home, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "cmdr account add: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Created Claude account %s (%s): settings, CLAUDE.md, skills, plugins and MCP servers are shared with ~/.claude; the login is its own.\n", a.Name, a.Dir)
		fmt.Fprintf(stdout, "Log it in (in a terminal):\n  %s\n", claudeacct.LoginCommand(home, a.Name))
		return 0
	case "use":
		if len(args) != 3 {
			fmt.Fprintln(stderr, accountUsage)
			return 2
		}
		return useAccount(home, args[1], args[2], stdout, stderr)
	case "auto":
		if len(args) < 2 || len(args) > 3 || !chat.ValidRole(args[1]) {
			fmt.Fprintln(stderr, accountUsage)
			return 2
		}
		role := args[1]
		if len(args) == 3 {
			if args[2] != "on" && args[2] != "off" {
				fmt.Fprintln(stderr, accountUsage)
				return 2
			}
			if err := chat.SetAutoSwitch(home, role, args[2] == "on"); err != nil {
				fmt.Fprintf(stderr, "cmdr account auto: %v\n", err)
				return 1
			}
		}
		state := "off"
		if chat.AutoSwitch(home, role) {
			state = "on"
		}
		if role == "commander" || strings.HasPrefix(role, "manager-") {
			state = "on (automatically moves to an account with usage left)"
		}
		fmt.Fprintf(stdout, "%s auto-switch at a usage limit: %s\n", role, state)
		return 0
	}
	fmt.Fprintln(stderr, accountUsage)
	return 2
}

// useAccount switches role through cmdrd (a running chat restarts on the
// account and resumes its session; refused mid-turn), or with cmdrd down
// records the choice for the chat's next start. Only the engine honours it, so
// a chat a terminal pane holds is reported as not moved.
func useAccount(home, role, name string, stdout, stderr io.Writer) int {
	if !chat.ValidRole(role) {
		fmt.Fprintf(stderr, "cmdr account use: invalid role %q (commander, manager-<bead>, operator-<name>)\n", role)
		return 2
	}
	if name == chat.RuntimeCodex {
		connected := false
		for _, a := range chat.Accounts(home, nil, time.Now()) {
			if a.Runtime == chat.RuntimeCodex {
				connected = a.LoggedIn
			}
		}
		if !connected {
			fmt.Fprintln(stderr, "cmdr account use: Codex is not connected (cmdr account list)")
			return 1
		}
		return runRuntime([]string{role, chat.RuntimeCodex}, stdout, stderr)
	}
	if !claudeacct.Exists(home, name) {
		fmt.Fprintf(stderr, "cmdr account use: no Claude account %q (cmdr account list)\n", name)
		return 1
	}
	if !claudeacct.Get(home, name).LoggedIn {
		fmt.Fprintf(stderr, "cmdr account use: %s is not logged in: run %s\n", name, claudeacct.LoginCommand(home, name))
		return 1
	}
	if strings.HasPrefix(role, "operator-") {
		fmt.Fprintf(stderr, "cmdr account use: an Operator switches by respawning: cmdr internal-operator-provider %s --account %s\n", role, name)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := cmdrdChats{home: home}.postStatus(ctx, role, "account", map[string]any{"account": name})
	if err == nil {
		switch {
		case st.Running:
			fmt.Fprintf(stdout, "%s runs on Claude account %s\n", role, name)
		case st.OwnedBy != "":
			fmt.Fprintf(stdout, "%s set to Claude account %s, but it did not move: a %s holds the chat, and terminal panes run on default. It takes effect when the chat next starts in the app.\n", role, name, st.OwnedBy)
		default:
			fmt.Fprintf(stdout, "%s set to Claude account %s: takes effect when the chat next starts in the app\n", role, name)
		}
		return 0
	}
	if !cmdrdDown(err) {
		fmt.Fprintf(stderr, "cmdr account use: %v\n", err)
		return 1
	}
	if err := chat.WriteAccount(home, role, name); err != nil {
		fmt.Fprintf(stderr, "cmdr account use: %v\n", err)
		return 1
	}
	if err := chat.WriteAccountRuntime(home, role); err != nil {
		fmt.Fprintf(stderr, "cmdr account use: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s set to Claude account %s (cmdrd is not running: takes effect when the chat next starts in the app; terminal panes run on default)\n", role, name)
	return 0
}

func printAccounts(w io.Writer, accounts []chat.AccountView, asJSON bool, now time.Time) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(accounts)
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tLOGIN\tSTATUS\t5H\t7D\tAS OF\tEMAIL")
	for _, a := range accounts {
		label := "claude:" + a.Name
		if a.Runtime == chat.RuntimeCodex {
			label = "codex"
		}
		login := "not logged in"
		if a.LoggedIn {
			login = "logged in"
		}
		status, five, seven, asOf := "-", "-", "-", "no report yet"
		if u := a.Usage; u != nil {
			status = u.Status
			if u.Status == chat.UsageOut && u.OutUntil > 0 {
				status += " until " + clock(time.Unix(u.OutUntil, 0), now)
			}
			five, seven = windowText(u.FiveHour, now), windowText(u.SevenDay, now)
			asOf = clock(u.At, now)
		}
		if a.DuplicateOf != "" {
			status = "same login as " + a.DuplicateOf
			five, seven, asOf = "-", "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", label, login, status, five, seven, asOf, a.Email)
	}
	_ = tw.Flush()
	return 0
}

// windowText is "42% (resets 4:00pm)".
func windowText(w *chat.Window, now time.Time) string {
	if w == nil {
		return "-"
	}
	s := fmt.Sprintf("%d%%", int(math.Round(w.Utilization*100)))
	if w.ResetsAt > 0 {
		s += " (resets " + clock(time.Unix(w.ResetsAt, 0), now) + ")"
	}
	return s
}

// clock is "4:00pm" today, else "Mon 4:00pm".
func clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	s := strings.ToLower(t.Format("3:04PM"))
	if t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		s = t.Format("Mon ") + s
	}
	return s
}
