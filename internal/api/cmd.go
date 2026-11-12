package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/livedata"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
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
	// What the click completes, read before the close takes it off the board.
	var done *completedWorkstream
	if len(req.Argv) > 0 && req.Argv[0] == "internal-close-objective" {
		if snap, ok := s.overviewSnapshot(); ok {
			done = findCompleted(snap, flagValue(req.Argv, "--project"), flagValue(req.Argv, "--bead"))
		}
	}
	res := s.opts.Run(r.Context(), req.Argv)
	if done != nil && res.OK && !strings.Contains(res.Stdout, `"error"`) && s.opts.MailToRole != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.opts.MailToRole(ctx, done.commander, done.message()); err != nil {
				log.Printf("api: could not tell %s that %s is complete: %v", done.commander, done.bead, err)
			}
		}()
	}
	// The regular poll coalesces mutation bursts; do not start a full read
	// for every write while another writer may be waiting.
	writeJSON(w, http.StatusOK, res)
}

// completedWorkstream is what the user's Complete click closed, for the
// Commander that plans it: it keeps Linear in step.
type completedWorkstream struct {
	bead, title, commander string
	tickets                []string
}

func (c completedWorkstream) message() string {
	msg := "Eric clicked Complete on \u201c" + c.title + "\u201d: it is closed in Commander."
	if len(c.tickets) > 0 {
		return msg + " Set its Linear tickets to Done: " + strings.Join(c.tickets, ", ") + "."
	}
	return msg + " It has no Linear ticket recorded; if it has one, set it to Done."
}

var ticketID = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)

// findCompleted is the workstream or nested workstream bead names in project.
func findCompleted(snap livedata.Snapshot, project, bead string) *completedWorkstream {
	if project == "" || bead == "" {
		return nil
	}
	make := func(title, commander string, labels []string) *completedWorkstream {
		if commander == "" {
			commander = "commander"
		}
		seen := map[string]bool{}
		var tickets []string
		for _, t := range append(ticketID.FindAllString(title, -1), labels...) {
			if !seen[t] {
				seen[t] = true
				tickets = append(tickets, t)
			}
		}
		return &completedWorkstream{bead: bead, title: title, commander: commander, tickets: tickets}
	}
	var find func([]livedata.SnapStep, string) *completedWorkstream
	find = func(steps []livedata.SnapStep, owner string) *completedWorkstream {
		for _, st := range steps {
			if st.BeadID == bead {
				return make(st.Title, firstNonEmptyStr(st.PlanningCommander, owner), st.Tickets)
			}
			if c := find(st.Children, firstNonEmptyStr(st.PlanningCommander, owner)); c != nil {
				return c
			}
		}
		return nil
	}
	for _, ws := range snap.Workstreams {
		if ws.Project != project {
			continue
		}
		if ws.BeadID == bead {
			return make(ws.Title, ws.PlanningCommander, ws.Tickets)
		}
		if c := find(ws.Steps, ws.PlanningCommander); c != nil {
			return c
		}
	}
	return nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// flagValue is the value after flag in argv ("" when absent).
func flagValue(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}
