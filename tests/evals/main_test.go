package evals

import (
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/testguard"
)

func TestMain(m *testing.M) {
	// Scenarios build throwaway projects with the br-format db; bd tests opt in.
	os.Setenv("COMMANDER_BEADS_BIN", "br")
	flag.Parse()
	selected, err := regexp.MatchString(strings.SplitN(flag.Lookup("test.run").Value.String(), "/", 2)[0], "TestEvalHungScenario")
	if err == nil && selected && flag.Lookup("test.list").Value.String() == "" && os.Getenv("COMMANDER_EVAL_HANG_CHILD") == "" && os.Getenv("COMMANDER_TESTGUARD_PID") == "" {
		prepareHungScenarios()
	}
	if os.Getenv("COMMANDER_EVAL_HANG_CHILD") != "" {
		// This intentional child runs only the negative scenario. Keep normal
		// home/tmux/process isolation, but allow this one bounded re-execution.
		if flag.Lookup("test.run").Value.String() != "^TestEvalHungScenario$" {
			os.Exit(2)
		}
	}
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
