package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
)

// TestOverviewSpikeCompare runs the real reader on workstreams from a saved snapshot
// (read-only: no cmdrd, no beads, no state). Gated by OVERVIEW_SPIKE_SNAPSHOT,
// OVERVIEW_SPIKE_OUT (label dir), OVERVIEW_SPIKE_TARGETS ("project:bead[:track],...")
// and OVERVIEW_SPIKE_CHATS (a commander home to read Manager text from).
func TestOverviewSpikeCompare(t *testing.T) {
	snapFile := os.Getenv("OVERVIEW_SPIKE_SNAPSHOT")
	if snapFile == "" {
		t.Skip("spike only")
	}
	b, err := os.ReadFile(snapFile)
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	// The output holds real notes and Manager chat: never write it into the repo.
	out := os.Getenv("OVERVIEW_SPIKE_OUT")
	if out == "" {
		out = t.TempDir()
	} else if !filepath.IsAbs(out) || overviewInsideRepo(out) {
		t.Fatalf("OVERVIEW_SPIKE_OUT must be an absolute path outside the repo, got %q", out)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("OVERVIEW_SPIKE_CHATS")
	gen := overview.Claude([]string{"HOME=" + os.Getenv("OVERVIEW_SPIKE_HOME"), "CLAUDE_CONFIG_DIR=" + os.Getenv("OVERVIEW_SPIKE_CLAUDE_DIR")})
	for _, target := range strings.Split(os.Getenv("OVERVIEW_SPIKE_TARGETS"), ",") {
		parts := strings.Split(target, ":")
		if len(parts) < 2 {
			t.Fatalf("target %q is not project:bead[:track]", target)
		}
		q := overviewRequest{Project: parts[0], BeadID: parts[1]}
		if len(parts) > 2 {
			q.TrackID = parts[2]
		}
		var ws *livedata.SnapWorkstream
		for i := range snap.Workstreams {
			if snap.Workstreams[i].Project == q.Project && snap.Workstreams[i].BeadID == q.BeadID {
				ws = &snap.Workstreams[i]
			}
		}
		if ws == nil {
			t.Fatalf("no workstream %s", target)
		}
		in, ok := buildOverviewInput(snap, ws, q, home)
		if !ok {
			t.Fatalf("no track %s", target)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		r, err := gen(ctx, in)
		cancel()
		res := map[string]any{"target": target, "input": in, "recap": r}
		if err != nil {
			res["error"] = err.Error()
		} else if err := overview.Validate(r, in); err != nil {
			res["invalid"] = err.Error()
		}
		j, _ := json.MarshalIndent(res, "", " ")
		file := filepath.Join(out, strings.ReplaceAll(target, ":", "_")+".json")
		if err := os.WriteFile(file, j, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", file)
	}
}

// overviewInsideRepo reports whether dir is in this source tree (the module root above
// the test's directory; real paths, so a link into it counts too; a dir not made yet is
// judged by its nearest existing parent). Unsure = inside.
func overviewInsideRepo(dir string) bool {
	wd, err := os.Getwd()
	if err != nil {
		return true
	}
	repo := ""
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			repo = d
			break
		}
		if filepath.Dir(d) == d {
			return true
		}
	}
	if repo, err = filepath.EvalSymlinks(repo); err != nil {
		return true
	}
	p, rest := filepath.Clean(dir), ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		rest, p = filepath.Join(filepath.Base(p), rest), parent
	}
	rel, err := filepath.Rel(repo, p)
	return err != nil || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

func TestOverviewSpikeRefusesAnOutputInsideTheRepo(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{wd, filepath.Join(wd, "..", ".."), filepath.Join(wd, "not-made-yet", "out")} {
		if !overviewInsideRepo(dir) {
			t.Fatalf("%s is inside the repo", dir)
		}
	}
	if overviewInsideRepo(t.TempDir()) {
		t.Fatal("a temp dir is outside the repo")
	}
}
