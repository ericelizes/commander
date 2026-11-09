package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/example-user/commander/internal/linearsync"
)

func runLinear(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "status" && args[0] != "set-key") {
		fmt.Fprintln(stderr, "usage: cmdr linear status | cmdr linear set-key (key via stdin or hidden prompt)")
		return 2
	}
	home := commanderHomeDir()
	if args[0] == "status" {
		key, err := linearsync.ReadKey(home)
		if err != nil {
			fmt.Fprintln(stdout, "Linear: key unavailable; run cmdr linear set-key")
			return 1
		}
		if key == "" {
			fmt.Fprintf(stdout, "Linear: no key; run cmdr linear set-key to save it in %s\n", linearsync.KeyPath(home))
		} else {
			fmt.Fprintln(stdout, "Linear: key configured (read-only; sprint fetched by cmdrd)")
		}
		return 0
	}
	var raw []byte
	var err error
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		fmt.Fprint(stderr, "Linear API key: ")
		raw, err = term.ReadPassword(f.Fd())
		fmt.Fprintln(stderr)
	} else {
		raw, err = io.ReadAll(io.LimitReader(stdin, 4098))
	}
	if err != nil || linearsync.WriteKey(home, strings.TrimSpace(string(raw))) != nil {
		fmt.Fprintln(stderr, "Linear: could not save key; supply one nonempty key via stdin")
		return 1
	}
	fmt.Fprintf(stdout, "Linear key saved in %s (0600); cmdrd reads it on its next refresh\n", linearsync.KeyPath(home))
	return 0
}
