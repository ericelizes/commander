package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// cmdTimeout bounds one cmdr invocation.
const cmdTimeout = 3 * time.Minute

// allowedVerbs are the cmdr commands the API may run. A non-nil set lists the
// sub-commands allowed for that verb (argv[1]).
var allowedVerbs = map[string]map[string]bool{
	"internal-set-objective":    nil,
	"internal-close-objective":  nil,
	"internal-create-objective": nil,
	"internal-rename-objective": nil,
	"internal-resolve-needs":    nil,
	"internal-open-workstream":  nil,
	"internal-place":            nil,
	"internal-mail":             nil, // its only mode is send
	"internal-ship":             nil,
	"internal-gate": {
		"add": true, "apply": true, "check": true, "pass": true, "list": true, "rm": true, "link": true,
	},
	"graph": {"check": true}, // read-only: the app Graph against the beads
	"board": nil,             // read-only: the board as the sidebar shows it
	"app":   {"shot": true},  // read-only: asks the open app window for a picture
	"tab": {
		"add": true, "rm": true, "remove": true, "focus": true,
		"move": true, "ls": true, "list": true,
	},
}

// Allowed reports whether argv may be run through the API.
func Allowed(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	subs, ok := allowedVerbs[argv[0]]
	if !ok {
		return false
	}
	if subs == nil {
		return true
	}
	return len(argv) > 1 && subs[argv[1]]
}

// CmdResult is the response of POST /api/cmd.
type CmdResult struct {
	OK     bool   `json:"ok"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
	Error  string `json:"error,omitempty"`
}

// Runner runs cmdr with argv (no shell) and reports its output.
type Runner func(ctx context.Context, argv []string) CmdResult

// ExecRunner runs the cmdr binary at path directly with argv.
func ExecRunner(path string) Runner {
	return func(ctx context.Context, argv []string) CmdResult {
		ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
		defer cancel()
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, path, argv...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		// The app's clicks are the user's: a COMMANDER_ROLE or
		// COMMANDER_MANAGER_ROLE inherited from the shell that started cmdrd
		// must not make them checked as an agent.
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "COMMANDER_ROLE=") && !strings.HasPrefix(kv, "COMMANDER_MANAGER_ROLE=") && !strings.HasPrefix(kv, "COMMANDER_RUN_ID=") && !strings.HasPrefix(kv, "COMMANDER_RUN_SESSION=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		err := cmd.Run()
		res := CmdResult{Stdout: stdout.String(), Stderr: stderr.String()}
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			res.OK = true
		case errors.As(err, &exitErr):
			res.Exit = exitErr.ExitCode()
		default:
			res.Exit = -1
			res.Error = err.Error()
		}
		return res
	}
}

type cmdRequest struct {
	Argv []string `json:"argv"`
}

func (s *Server) serveCmd(w http.ResponseWriter, r *http.Request) {
	var req cmdRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, CmdResult{Exit: -1, Error: "body must be {\"argv\": [...]}"})
		return
	}
	if !Allowed(req.Argv) {
		writeJSON(w, http.StatusForbidden, CmdResult{Exit: -1, Error: "command not allowed"})
		return
	}
	if s.opts.Run == nil {
		writeJSON(w, http.StatusServiceUnavailable, CmdResult{Exit: -1, Error: "no cmdr runner configured"})
		return
	}
	res := s.opts.Run(r.Context(), req.Argv)
	// The regular poll coalesces mutation bursts; do not start a full read
	// for every write while another writer may be waiting.
	writeJSON(w, http.StatusOK, res)
}
