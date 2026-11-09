package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/decisions"
)

// runDecisions is `cmdr decisions list|add|retire`: the user's standing
// decisions, which every Commander and Manager turn carries (decisions.Deliver).
func runDecisions(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		emitErr(stderr, "decisions: want list, add or retire")
		return 1
	}
	verb, args := args[0], args[1:]
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:] // retire <id> [flags]
	}
	fs := flag.NewFlagSet("decisions "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	words := fs.String("words", "", "add: the user's own words for the call")
	rule := fs.String("rule", "", "add: a rule key no-<words> (e.g. no-deploy-window): agent gates, waits, parks, review steps and asks that name those words are refused")
	scope := fs.String("scope", "", "add: global or a workstream/track bead id (default: global; a Manager's own workstream for a Manager); list: only what holds there")
	date := fs.String("date", "", "add: the day the user said it, YYYY-MM-DD (default today)")
	reason := fs.String("reason", "", "retire: what the user said instead")
	all := fs.Bool("all", false, "list: include retired decisions")
	asJSON := fs.Bool("json", false, "list: JSON output")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	home := appCommanderHome()
	role := os.Getenv("COMMANDER_ROLE")
	who := role
	if who == "" {
		who = "user"
	}
	switch verb {
	case "list":
		ds, err := decisions.Load(home)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		if !*all {
			if *scope != "" {
				ds = decisions.ActiveIn(ds, *scope)
			} else {
				ds = decisions.ActiveIn(ds)
			}
		}
		if *asJSON {
			if ds == nil {
				ds = []decisions.Decision{}
			}
			_ = json.NewEncoder(stdout).Encode(ds)
			return 0
		}
		if len(ds) == 0 {
			fmt.Fprintln(stdout, "no standing decisions")
		}
		for _, d := range ds {
			line := d.Line()
			if !d.Active() {
				line += fmt.Sprintf("  [retired %s by %s: %s]", time.Unix(d.RetiredAt, 0).Format("2006-01-02"), d.RetiredBy, d.RetireReason)
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	case "add":
		if strings.HasPrefix(role, "operator-") {
			emitErr(stderr, "an Operator does not record the user's decisions: tell your Manager what the user said")
			return 1
		}
		if *scope == "" && strings.HasPrefix(role, "manager-") {
			*scope = strings.TrimPrefix(role, "manager-")
		}
		d, err := decisions.Add(home, decisions.Decision{Words: *words, Rule: *rule, Scope: *scope, Date: *date, AddedBy: who}, time.Now())
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(d)
		return 0
	case "retire":
		if role != "" && role != "commander" {
			emitErr(stderr, "only Commander (or the user) retires a standing decision: the user changed the call, so tell Commander")
			return 1
		}
		if id == "" {
			id = fs.Arg(0)
		}
		if id == "" {
			emitErr(stderr, "usage: cmdr decisions retire <id> [--reason '<what the user said instead>']")
			return 1
		}
		d, err := decisions.Retire(home, id, who, *reason, time.Now())
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(d)
		return 0
	}
	emitErr(stderr, fmt.Sprintf("decisions: unknown verb %q (want list, add or retire)", verb))
	return 1
}
