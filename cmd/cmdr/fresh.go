package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

const freshUsage = `usage: cmdr fresh <role>...   (commander, manager-<workstream bead>)
       cmdr fresh --all          every running Commander and Manager chat

A clean restart: each chat becomes a new session (new context, today's prompt)
whose first turn reads its predecessor's conversation and carries on. A chat
mid-turn is retried until it is idle (up to 10 minutes each).
`

func runFresh(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, freshUsage)
		return 2
	}
	c := cmdrdChats{home: agentrt.CommanderHome()}
	roles := args
	if slices.Contains(args, "--all") {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		list, err := c.list(ctx)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "cmdr fresh: %v\n", err)
			return 1
		}
		roles = nil
		for _, s := range list {
			if s.Running && (s.Role == "commander" || strings.HasPrefix(s.Role, "manager-")) {
				roles = append(roles, s.Role)
			}
		}
		// Commander last: it is the one the user is likely talking to.
		slices.SortStableFunc(roles, func(a, b string) int {
			return map[bool]int{true: 1}[a == "commander"] - map[bool]int{true: 1}[b == "commander"]
		})
	}
	failed := 0
	for _, role := range roles {
		if err := freshOne(c, role); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", role, err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "%s: fresh session started; it reads its predecessor's conversation first\n", role)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// freshOne restarts role fresh, waiting out a turn in flight.
func freshOne(c cmdrdChats, role string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := c.post(ctx, role, "fresh", nil)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "wait for the turn to end") || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Second)
	}
}
