package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

const placeUsage = `usage:
  cmdr place pair <code> --name <slug>   pair the Mac whose app shows <code> (the user, in their own terminal)
  cmdr place revoke <mac:slug>           revoke a pairing (the user); its running run is stopped
  cmdr place list [--json]               paired and revoked places with their active runs
On the Mac, inside a place run (COMMANDER_PLACE_RUN/TOKEN/URL set by the app):
  cmdr place report --file <path>        post the final report (a file in the run directory)
  cmdr place comment --text <t>          post a progress note on the run's task bead
  cmdr place mail --subject <s> --body <b>   mail the run's Manager`

func runPlace(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, placeUsage)
		return 2
	}
	switch args[0] {
	case "pair":
		return runPlacePair(args[1:], stdout, stderr)
	case "revoke":
		return runPlaceRevoke(args[1:], stdout, stderr)
	case "list":
		return runPlaceList(args[1:], stdout, stderr)
	case "revision":
		// What the app sends in its hello: cmdrd refuses any other revision.
		fmt.Fprintln(stdout, place.BuildRevision())
		return 0
	case "prompt":
		// The role prompt the app's launch template passes to claude on the Mac.
		fmt.Fprint(stdout, promptassets.PlaceOperatorPrompt())
		return 0
	case "report", "comment", "mail":
		return runPlaceRunVerb(args[0], args[1:], os.Getenv, stdout, stderr)
	}
	fmt.Fprintln(stderr, placeUsage)
	return 2
}

func openPlaceStore(stderr *os.File) (*store.Store, int) {
	path, err := stateDBPath()
	if err != nil {
		emitErr(stderr, fmt.Sprintf("resolve state dir: %v", err))
		return nil, 1
	}
	st, err := store.Open(path)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open store: %v", err))
		return nil, 1
	}
	return st, 0
}

// flagsAnywhere lets flags follow positional words (`pair CODE --name x`).
func flagsAnywhere(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func runPlacePair(args []string, stdout, stderr *os.File) int {
	if err := place.RefuseAgent(os.Getenv); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fs := flag.NewFlagSet("place pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "a short name for this Mac (becomes mac:<name>)")
	pos, err := flagsAnywhere(fs, args)
	if err != nil || len(pos) != 1 || *name == "" {
		fmt.Fprintln(stderr, "usage: cmdr place pair <code> --name <slug>")
		return 2
	}
	full := "mac:" + strings.TrimPrefix(*name, "mac:")
	if !place.ValidName(full) {
		emitErr(stderr, fmt.Sprintf("%q is not a place name: lower-case letters, digits and dashes, up to 31", *name))
		return 1
	}
	home := commanderHomeDir()
	pub, err := place.TakePairing(home, pos[0], time.Now())
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	st, code := openPlaceStore(stderr)
	if st == nil {
		return code
	}
	defer st.Close()
	who := firstNonEmpty(os.Getenv("USER"), "user")
	if err := st.AddPlace(context.Background(), store.Place{Name: full, Kind: "mac", PublicKey: pub, PairedAt: time.Now().Unix(), PairedBy: who}); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "paired %s. Steps run there only with the label %s and a closed user gate over them.\n", full, place.Label(full))
	return 0
}

func runPlaceRevoke(args []string, stdout, stderr *os.File) int {
	if err := place.RefuseAgent(os.Getenv); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if len(args) != 1 || !place.ValidName(args[0]) {
		fmt.Fprintln(stderr, "usage: cmdr place revoke mac:<slug>")
		return 2
	}
	st, code := openPlaceStore(stderr)
	if st == nil {
		return code
	}
	defer st.Close()
	ctx := context.Background()
	ok, err := st.RevokePlace(ctx, args[0], time.Now().Unix())
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if !ok {
		emitErr(stderr, args[0]+" is not paired")
		return 1
	}
	// Its active run ends now through cmdrd's stop path: the token dies and the
	// Mac, if connected, is sent the stop and ends the process group. With
	// cmdrd down the run is ended here; the Mac's next heartbeat is then
	// refused (401) and it stops the run itself. The sweep settles it, and
	// the Mac is refused at its next hello.
	runs, _ := st.ListPlaceRuns(ctx, args[0], true)
	for _, r := range runs {
		if err := postPlaceStop(ctx, appCommanderHome(), r.RunID, "the user (place revoked)"); err != nil {
			fmt.Fprintf(stderr, "note: cmdrd did not take the stop of run %d (%v): ended here; the Mac stops it at its next heartbeat\n", r.RunID, err)
		}
		_, _ = st.EndPlaceRun(ctx, r.RunID, store.PlaceStopped, "place revoked", time.Now().Unix())
	}
	fmt.Fprintf(stdout, "revoked %s (%d active run(s) stopped)\n", args[0], len(runs))
	return 0
}

func runPlaceList(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("place list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, code := openPlaceStore(stderr)
	if st == nil {
		return code
	}
	defer st.Close()
	ctx := context.Background()
	places, err := st.ListPlaces(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	type row struct {
		Name     string           `json:"name"`
		Paired   bool             `json:"paired"`
		PairedAt int64            `json:"paired_at"`
		PairedBy string           `json:"paired_by"`
		Runs     []store.PlaceRun `json:"active_runs"`
	}
	var rows []row
	for _, p := range places {
		runs, _ := st.ListPlaceRuns(ctx, p.Name, true)
		for i := range runs {
			runs[i].TokenHash = "" // never printed
		}
		rows = append(rows, row{Name: p.Name, Paired: p.Paired(), PairedAt: p.PairedAt, PairedBy: p.PairedBy, Runs: runs})
	}
	if *asJSON {
		return writeJSONOut(stdout, rows)
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PLACE\tSTATE\tPAIRED\tRUN")
	now := time.Now()
	for _, r := range rows {
		state := "paired"
		if !r.Paired {
			state = "revoked"
		}
		run := "-"
		if len(r.Runs) > 0 {
			run = fmt.Sprintf("%d (%s)", r.Runs[0].RunID, place.Words(r.Runs[0], now))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s by %s\t%s\n", r.Name, state, time.Unix(r.PairedAt, 0).Format("2006-01-02"), r.PairedBy, run)
	}
	if tw.Flush() != nil {
		return 1
	}
	return 0
}

func writeJSONOut(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

// --- the Mac side: verbs a place run calls with its run token -------------

// placeRunEnv is what the app's launch template puts in a run's environment.
type placeRunEnv struct {
	URL, Token, Dir string
	RunID           int64
}

func readPlaceRunEnv(getenv func(string) string) (placeRunEnv, error) {
	var e placeRunEnv
	e.URL = strings.TrimRight(getenv("COMMANDER_PLACE_URL"), "/")
	e.Token = getenv("COMMANDER_PLACE_TOKEN")
	e.Dir = getenv("COMMANDER_PLACE_DIR")
	if _, err := fmt.Sscan(getenv("COMMANDER_PLACE_RUN"), &e.RunID); err != nil || e.URL == "" || e.Token == "" {
		return e, fmt.Errorf("not inside a place run (COMMANDER_PLACE_RUN, COMMANDER_PLACE_TOKEN and COMMANDER_PLACE_URL are set by the app)")
	}
	// The run token goes only to the server the app recorded for this run,
	// in a file beside the run directory the agent cannot write: a
	// COMMANDER_PLACE_URL changed inside the run is refused.
	pinned, err := pinnedPlaceURL(e.Dir, e.RunID)
	if err != nil {
		return e, err
	}
	if pinned != e.URL {
		return e, fmt.Errorf("refused: COMMANDER_PLACE_URL (%s) is not the server the app recorded for this run", e.URL)
	}
	return e, nil
}

// placeURLFile is where the app records a run's server: <runs>/<id>.launch/place-url.
func placeURLFile(dir string, runID int64) string {
	return filepath.Join(filepath.Dir(dir), strconv.FormatInt(runID, 10)+".launch", "place-url")
}

// pinnedPlaceURL reads the server the app recorded for the run in dir: the
// run directory must have the app's shape and the file must be private.
func pinnedPlaceURL(dir string, runID int64) (string, error) {
	if !appRunDir(dir, runID) {
		return "", fmt.Errorf("refused: COMMANDER_PLACE_DIR is not the run directory the app made")
	}
	f := placeURLFile(dir, runID)
	fi, err := os.Lstat(f)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("refused: the app's record of this run's server (%s) is missing or not private", f)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(b)), "/"), nil
}

// placeCall posts body to the run endpoint path with the run token.
func placeCall(e placeRunEnv, method, path string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%s/api/places/runs/%d/%s", e.URL, e.RunID, path), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Commander-Run-Token", e.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w (a report sent but unanswered may have landed: send it again, it is written once)", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out["ok"] != true {
		return out, fmt.Errorf("cmdrd answered %d: %v", resp.StatusCode, out["error"])
	}
	return out, nil
}

// appRunDir reports whether dir has the shape the app gives a run directory,
// <runs dir>/runs/<run id>, a private directory of this user: a variable
// pointed at / or a home does not pass, so the report verb cannot be aimed
// at files outside the run.
func appRunDir(dir string, runID int64) bool {
	if dir == "" || filepath.Base(dir) != strconv.FormatInt(runID, 10) || filepath.Base(filepath.Dir(dir)) != "runs" {
		return false
	}
	fi, err := os.Lstat(dir)
	return err == nil && fi.IsDir() && fi.Mode().Perm()&0o077 == 0
}

// insideDir reports whether path is dir or under it, by real paths.
func insideDir(path, dir string) bool {
	rp, err1 := filepath.EvalSymlinks(path)
	rd, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(rd, rp)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runPlaceRunVerb(verb string, args []string, getenv func(string) string, stdout, stderr *os.File) int {
	e, err := readPlaceRunEnv(getenv)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fs := flag.NewFlagSet("place "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "report file (in the run directory)")
	text := fs.String("text", "", "comment text")
	subject := fs.String("subject", "", "mail subject")
	body := fs.String("body", "", "mail body")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var out map[string]any
	switch verb {
	case "report":
		if *file == "" {
			emitErr(stderr, "usage: cmdr place report --file <path in the run directory>")
			return 2
		}
		if !appRunDir(e.Dir, e.RunID) || !insideDir(*file, e.Dir) {
			emitErr(stderr, "refused: the report must be a file in the run directory ("+e.Dir+")")
			return 1
		}
		b, err := os.ReadFile(*file)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		out, err = placeCall(e, "POST", "comment", map[string]any{"client_id": "report-" + place.BriefSHA(string(b))[:32], "text": string(b), "report": true})
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "report posted on %v\n", out["bead"])
	case "comment":
		if strings.TrimSpace(*text) == "" {
			emitErr(stderr, "usage: cmdr place comment --text <note>")
			return 2
		}
		out, err = placeCall(e, "POST", "comment", map[string]any{"client_id": "note-" + place.BriefSHA(*text)[:32], "text": *text})
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "commented on %v\n", out["bead"])
	case "mail":
		if strings.TrimSpace(*subject) == "" {
			emitErr(stderr, "usage: cmdr place mail --subject <s> --body <b>")
			return 2
		}
		out, err = placeCall(e, "POST", "mail", map[string]any{"subject": *subject, "body": *body})
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "mailed %v\n", out["to"])
	}
	return 0
}
