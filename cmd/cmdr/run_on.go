package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
)

// placeLookup reads a place by name; tests swap it.
var placeLookup = func(ctx context.Context, name string) (*store.Place, error) {
	path, err := stateDBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.GetPlace(ctx, name)
}

// checkRunOn judges --run-on before anything is written: "none", or a paired,
// not revoked place; never from an Operator.
func checkRunOn(value string) error {
	if value == "" {
		return nil
	}
	if strings.HasPrefix(os.Getenv("COMMANDER_ROLE"), "operator-") {
		return fmt.Errorf("an Operator cannot choose where a step runs: ask your Manager")
	}
	if value == "none" {
		return nil
	}
	if !place.ValidName(value) {
		return fmt.Errorf("--run-on must be mac:<name> (see cmdr place list) or none")
	}
	p, err := placeLookup(context.Background(), value)
	if err != nil {
		return fmt.Errorf("--run-on: read places: %w", err)
	}
	if p == nil {
		return fmt.Errorf("--run-on %s: no such place; the user pairs one with cmdr place pair (cmdr place list shows them)", value)
	}
	if !p.Paired() {
		return fmt.Errorf("--run-on %s: the place was revoked", value)
	}
	return nil
}
