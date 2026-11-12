package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// runGraph is `cmdr graph check [--project p] [--json]`: the app's Graph
// against the beads it draws, per workstream. Read-only. `cmdr graph check
// <n|bead>` checks one workstream's, track's or step's diagram instead.
func runGraph(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "usage: cmdr graph check [<n|workstream|track|bead>] [--project <p>] [--json] [--snapshot <file>] [--audit]")
		return 2
	}
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		return runDiagramCheck(args[1], args[2:], stdout, stderr)
	}
	fs := flag.NewFlagSet("graph check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "only this project's workstreams")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	file := fs.String("snapshot", "", "check this snapshot JSON file instead of the live one from cmdrd")
	audit := fs.Bool("audit", false, "only print the Commander check-in report (rows, NEEDS YOU, sanity audit) for the snapshot; no beads or node needed")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *audit {
		return runCheckinPreview(appCommanderHome(), *file, *asJSON, stdout, stderr)
	}
	results, err := graphCheck(appCommanderHome(), resolveCommanderRepo(), *project, *file, brOutput)
	if err != nil {
		fmt.Fprintln(stderr, "graph check:", err)
		return 2
	}
	bad := printGraphCheck(stdout, results, *asJSON)
	if bad > 0 {
		return 1
	}
	return 0
}

type graphMismatch struct {
	Bead   string `json:"bead"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type graphWsResult struct {
	Project    string          `json:"project"`
	BeadID     string          `json:"bead_id"`
	Title      string          `json:"title"`
	Mismatches []graphMismatch `json:"mismatches"`
	// Violations are open beads with no agent, recorded wait or the user on them
	// (livedata.OpenBeadInvariant): broken data, counted like a mismatch.
	Violations []livedata.Violation `json:"violations"`
	// Audit: rows and beads the sanity audit (livedata.AuditSnapshot) finds
	// suspect, counted like a mismatch.
	Audit []livedata.Suspect `json:"audit"`
}

// brRunner runs br in a project directory and returns its stdout.
type brRunner func(dir string, args ...string) ([]byte, error)

func brOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command(beads.Binary(), args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("br %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// brBeads is every bead of a project with its edges: `br list --all`, then
// single-ID `br show` calls (list carries only counts).
func brBeads(run brRunner, dir string) (json.RawMessage, error) {
	raw, err := run(dir, "list", "--json", "--all", "--limit", "0")
	if err != nil {
		return nil, err
	}
	var listed struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		var plain []struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(raw, &plain); err2 != nil {
			return nil, fmt.Errorf("br list: decode: %w", err)
		}
		for _, p := range plain {
			listed.Issues = append(listed.Issues, p)
		}
	}
	all := []json.RawMessage{}
	// The PostgreSQL backend accepts one ID per show. Reading in list order
	// also preserves the result order and the first backend error.
	for _, issue := range listed.Issues {
		out, err := run(dir, "show", issue.ID, "--json")
		if err != nil {
			return nil, err
		}
		var shown []json.RawMessage
		if err := json.Unmarshal(out, &shown); err != nil {
			// The PostgreSQL compatibility command returns a single object;
			// upstream br/bd returns an array even for one ID.
			var one map[string]json.RawMessage
			if err := json.Unmarshal(out, &one); err != nil || one == nil {
				return nil, fmt.Errorf("br show: decode: %w", err)
			}
			// Adapt canonical edge fields to the graph checker's br wire format.
			var deps []struct {
				ID            string `json:"id"`
				Type          string `json:"dependency_type"`
				DependsOnID   string `json:"depends_on_id"`
				CanonicalType string `json:"type"`
			}
			if raw := one["dependencies"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &deps); err != nil {
					return nil, fmt.Errorf("br show: dependencies: %w", err)
				}
				for i := range deps {
					if deps[i].ID == "" {
						deps[i].ID = deps[i].DependsOnID
					}
					if deps[i].Type == "" {
						deps[i].Type = deps[i].CanonicalType
					}
				}
				one["dependencies"], _ = json.Marshal(deps)
			}
			normalized, err := json.Marshal(one)
			if err != nil {
				return nil, fmt.Errorf("br show: encode: %w", err)
			}
			shown = []json.RawMessage{normalized}
		}
		all = append(all, shown...)
	}

	return json.Marshal(all)
}

// loadSnapshot reads the snapshot from a file, or live from cmdrd.
func loadSnapshot(home, file string) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	base, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return nil, fmt.Errorf("cmdrd is not running (no api-url in %s)", home)
	}
	token, err := os.ReadFile(filepath.Join(home, "api-token"))
	if err != nil {
		return nil, fmt.Errorf("no API token: %w", err)
	}
	req, _ := http.NewRequest(http.MethodGet, strings.TrimSpace(string(base))+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot: %s", resp.Status)
	}
	return body, nil
}

func graphCheck(home, repo, project, file string, run brRunner) ([]graphWsResult, error) {
	body, err := loadSnapshot(home, file)
	if err != nil {
		return nil, err
	}
	return graphCheckSnapshot(repo, body, project, run)
}

// runCheckinPreview prints what cmdrd's Commander check-in carries, against
// the live (or given) snapshot, so the user can audit it on demand. Exit 1 when
// the audit has suspects.
func runCheckinPreview(home, file string, asJSON bool, stdout, stderr *os.File) int {
	body, err := loadSnapshot(home, file)
	if err != nil {
		fmt.Fprintln(stderr, "graph check:", err)
		return 2
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		fmt.Fprintln(stderr, "graph check: snapshot: decode:", err)
		return 2
	}
	now := time.Now().Unix()
	suspects := livedata.AuditSnapshot(snap, now, livedata.AuditOptions{})
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": len(suspects) == 0, "board": boardOf(snap, now), "rows": livedata.Rows(snap, now), "needs_you": snap.NeedsYou, "audit": suspects})
	} else {
		fmt.Fprint(stdout, livedata.CheckinReport(snap, now, livedata.AuditOptions{}))
	}
	if len(suspects) > 0 {
		return 1
	}
	return 0
}

// graphCheckSnapshot compares a snapshot (JSON) with the beads of its projects.
func graphCheckSnapshot(repo string, snapshot []byte, project string, run brRunner) ([]graphWsResult, error) {
	var snap struct {
		Projects []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"projects"`
		Workstreams []struct {
			Project string `json:"project"`
		} `json:"workstreams"`
	}
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		return nil, fmt.Errorf("snapshot: decode: %w", err)
	}
	used := map[string]bool{}
	for _, w := range snap.Workstreams {
		used[w.Project] = true
	}
	brData := map[string]json.RawMessage{}
	for _, p := range snap.Projects {
		if !used[p.Name] || (project != "" && p.Name != project) {
			continue
		}
		beads, err := brBeads(run, p.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		brData[p.Name] = beads
	}
	input, err := json.Marshal(map[string]any{"snapshot": json.RawMessage(snapshot), "br": brData, "project": project})
	if err != nil {
		return nil, err
	}
	script := filepath.Join(repo, "app", "scripts", "graph-check.sh")
	if _, err := os.Stat(filepath.Join(repo, "app", "node_modules")); err != nil {
		return nil, fmt.Errorf("app dependencies missing: run `cd %s/app && npm ci` first", repo)
	}
	cmd := exec.Command(script)
	cmd.Stdin = bytes.NewReader(input)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// Exit 1 means mismatches (the JSON is still on stdout); anything else is a failure to run.
	runErr := cmd.Run()
	var results []graphWsResult
	if jerr := json.Unmarshal(out.Bytes(), &results); jerr != nil {
		msg := strings.TrimSpace(errb.String())
		if runErr != nil {
			return nil, fmt.Errorf("graph-check script: %v: %s (is node on PATH?)", runErr, msg)
		}
		return nil, fmt.Errorf("graph-check script: bad output: %v", jerr)
	}
	var full livedata.Snapshot
	if err := json.Unmarshal(snapshot, &full); err == nil {
		for _, v := range freshViolations(livedata.SnapshotViolations(full), brData) {
			for i := range results {
				if results[i].BeadID == v.Workstream {
					results[i].Violations = append(results[i].Violations, v)
				}
			}
		}
		for _, ws := range full.Workstreams {
			for _, f := range livedata.CheckDiagram(ws, ws.BeadID) {
				if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
					for i := range results {
						if results[i].BeadID == ws.BeadID {
							results[i].Mismatches = append(results[i].Mismatches, graphMismatch{Bead: f.Bead, Kind: f.Check, Detail: f.Why})
						}
					}
				}
			}
		}
		wsOf := map[string]string{}
		for _, w := range full.Workstreams {
			wsOf[w.ManagerRole] = w.BeadID
		}
		for _, x := range livedata.AuditSnapshot(full, time.Now().Unix(), livedata.AuditOptions{}) {
			for i := range results {
				if results[i].BeadID == wsOf[x.ManagerRole] {
					results[i].Audit = append(results[i].Audit, x)
				}
			}
		}
	}
	return results, nil
}

// freshViolations drops the violations whose bead the fresh br read says is
// closed or parked: the snapshot lags that write (a close the board has not
// loaded yet), so the bead holds nothing to fix. The status mismatch the
// comparison reports for it still says the snapshot is behind.
func freshViolations(vs []livedata.Violation, brData map[string]json.RawMessage) []livedata.Violation {
	status := map[string]string{}
	for project, raw := range brData {
		var bs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &bs) != nil {
			continue
		}
		for _, b := range bs {
			status[project+"\x00"+b.ID] = b.Status
		}
	}
	out := vs[:0:0]
	for _, v := range vs {
		if st := status[v.Project+"\x00"+v.BeadID]; st == "closed" || st == beads.StatusDeferred {
			continue
		}
		out = append(out, v)
	}
	return out
}

// printGraphCheck prints per-workstream OK/mismatch lines (or JSON) and returns the mismatch count.
func printGraphCheck(w io.Writer, results []graphWsResult, asJSON bool) int {
	bad := 0
	for _, r := range results {
		bad += len(r.Mismatches) + len(r.Violations) + len(r.Audit)
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": bad == 0, "mismatches": bad, "workstreams": results})
		return bad
	}
	for _, r := range results {
		if len(r.Mismatches)+len(r.Violations)+len(r.Audit) == 0 {
			fmt.Fprintf(w, "OK        %s  %s\n", r.BeadID, r.Title)
			continue
		}
		fmt.Fprintf(w, "MISMATCH  %s  %s  (%d)\n", r.BeadID, r.Title, len(r.Mismatches)+len(r.Violations)+len(r.Audit))
		for _, m := range r.Mismatches {
			fmt.Fprintf(w, "  - [%s] %s: %s\n", m.Kind, m.Bead, m.Detail)
		}
		for _, v := range r.Violations {
			fmt.Fprintf(w, "  - [needs-wait-or-owner] %s: %s (open, no agent, no recorded wait, not the user's)\n", v.BeadID, v.Title)
		}
		for _, a := range r.Audit {
			fmt.Fprintf(w, "  - [audit:%s] %s (%s): %s\n", a.Rule, a.Bead, a.ManagerRole, a.Why)
		}
	}
	return bad
}

// runDiagramCheck is `cmdr graph check <n|bead> [--json] [--snapshot f]`: the
// diagram's findings for a workstream (its tracks included), a track or a
// step, from the same snapshot the app draws. Exit 1 when there are any.
func runDiagramCheck(which string, args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("graph check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the findings as JSON")
	file := fs.String("snapshot", "", "check this snapshot JSON file instead of the live one from cmdrd")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	body, err := loadSnapshot(appCommanderHome(), *file)
	if err != nil {
		fmt.Fprintln(stderr, "graph check:", err)
		return 2
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		fmt.Fprintln(stderr, "graph check: snapshot: decode:", err)
		return 2
	}
	findings, title, err := diagramFindings(snap, time.Now().Unix(), which)
	if err != nil {
		fmt.Fprintln(stderr, "graph check:", err)
		return 2
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": len(findings) == 0, "scope": which, "findings": findings})
	} else if len(findings) == 0 {
		fmt.Fprintf(stdout, "OK        %s  %s: the diagram reads right\n", which, title)
	} else {
		fmt.Fprintf(stdout, "FINDINGS  %s  %s  (%d)\n", which, title, len(findings))
		for _, l := range livedata.FindingLines(findings) {
			fmt.Fprintln(stdout, l)
		}
	}
	if len(findings) > 0 {
		return 1
	}
	return 0
}

// diagramFindings are the findings of the diagram for which (a row number, a
// workstream or track bead), or of the graph under any other step bead.
func diagramFindings(snap livedata.Snapshot, now int64, which string) ([]livedata.DiagramFinding, string, error) {
	if dg, err := diagramOf(snap, boardOf(snap, now), now, which); err == nil {
		return dg.Findings, dg.Title, nil
	}
	for _, ws := range snap.Workstreams {
		for _, st := range flattenSnapSteps(ws.Steps) {
			if st.BeadID == which {
				findings := livedata.CheckDiagram(ws, which)
				// Cards are on the board, not the workstream: its finding comes from the row.
				for _, r := range boardOf(snap, now).Workstreams {
					if r.BeadID == ws.BeadID && r.Diagram != nil {
						for _, f := range r.Diagram.Findings {
							if f.Check == livedata.CheckNeedsYouLane && slices.ContainsFunc(flattenSnapSteps([]livedata.SnapStep{st}), func(c livedata.SnapStep) bool { return c.BeadID == f.Bead }) {
								findings = append(findings, f)
							}
						}
					}
				}
				return findings, st.Title, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no workstream, track or step %q on the board", which)
}

func flattenSnapSteps(steps []livedata.SnapStep) []livedata.SnapStep {
	var out []livedata.SnapStep
	for _, s := range steps {
		out = append(out, s)
		out = append(out, flattenSnapSteps(s.Children)...)
	}
	return out
}
