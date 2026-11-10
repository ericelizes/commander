package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type postgresExport struct {
	ProjectID int64  `json:"project_id"`
	File      string `json:"file"`
	Empty     bool   `json:"empty"`
}
type postgresManifest struct {
	Source  string           `json:"source"`
	Exports []postgresExport `json:"exports"`
}

// storage postgres prepare is read-only over existing state. migrate writes
// only the explicitly selected empty candidate. Selection/installation is a
// separate cutover step after the candidate has been verified.
func runStoragePostgres(args []string, stdout, stderr *os.File) int {
	if len(args) < 2 || args[0] != "postgres" {
		fmt.Fprintln(stderr, "usage: cmdr storage postgres prepare|migrate [options]")
		return 1
	}
	fs := flag.NewFlagSet("storage postgres "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "", "existing SQLite state.db (prepare)")
	dir := fs.String("dir", "", "private migration directory")
	dsnFile := fs.String("dsn-file", "", "private candidate Postgres connection file (migrate)")
	if e := fs.Parse(args[2:]); e != nil {
		return 1
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "--dir is required")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var report any
	var e error
	switch args[1] {
	case "prepare":
		if *source == "" {
			fmt.Fprintln(stderr, "--source is required")
			return 1
		}
		report, e = preparePostgresMigration(ctx, *source, *dir)
	case "migrate":
		if *dsnFile == "" {
			fmt.Fprintln(stderr, "--dsn-file is required")
			return 1
		}
		report, e = migratePostgresCandidate(ctx, *dir, *dsnFile)
	default:
		e = errors.New("unknown storage operation")
	}
	if e != nil {
		fmt.Fprintln(stderr, "Postgres migration:", e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(report); e != nil {
		return 1
	}
	return 0
}

func preparePostgresMigration(ctx context.Context, source, dir string) (any, error) {
	if _, e := os.Stat(dir); e == nil {
		return nil, errors.New("migration directory already exists; use a new empty path")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	abs, e := filepath.Abs(source)
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	db, e := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout(5000)")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	backup := filepath.Join(dir, "state.db")
	if _, e = db.ExecContext(ctx, `VACUUM INTO ?`, backup); e != nil {
		return nil, e
	}
	if e = os.Chmod(backup, 0600); e != nil {
		return nil, e
	}
	copyStore, e := store.OpenProvenance(backup)
	if e != nil {
		return nil, e
	}
	defer copyStore.Close()
	projects, e := copyStore.ListProjects(ctx)
	if e != nil {
		return nil, e
	}
	manifest := postgresManifest{Source: "state.db"}
	for _, p := range projects {
		file := fmt.Sprintf("project-%d.jsonl", p.ID)
		if _, e = os.Stat(filepath.Join(p.Path, ".beads", "metadata.json")); e != nil {
			return nil, fmt.Errorf("project %d lacks a readable Beads workspace: %w", p.ID, e)
		}
		out, e := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		cmd := exec.CommandContext(ctx, "bd", "--readonly", "export", "--all")
		cmd.Dir = p.Path
		cmd.Stdout = out
		var diagnostic cappedMigrationDiagnostic
		cmd.Stderr = &diagnostic
		e = cmd.Run()
		out.Close()
		if e != nil {
			return nil, fmt.Errorf("project %d export failed: %w", p.ID, e)
		}
		manifest.Exports = append(manifest.Exports, postgresExport{ProjectID: p.ID, File: file})
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if e = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); e != nil {
		return nil, e
	}
	return map[string]any{"prepared": true, "projects": len(manifest.Exports), "directory": dir}, nil
}

type cappedMigrationDiagnostic struct{ n int }

func (b *cappedMigrationDiagnostic) Write(p []byte) (int, error) { b.n += len(p); return len(p), nil }

func migratePostgresCandidate(ctx context.Context, dir, dsnFile string) (any, error) {
	info, e := os.Lstat(dsnFile)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("candidate connection file must be regular and private (0600)")
	}
	raw, e := os.ReadFile(dsnFile)
	if e != nil {
		return nil, e
	}
	dsn := strings.TrimSpace(string(raw))
	s, e := store.OpenPostgres(dsn)
	if e != nil {
		return nil, e
	}
	defer s.Close()
	raw, e = os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return nil, e
	}
	var manifest postgresManifest
	if e = json.Unmarshal(raw, &manifest); e != nil {
		return nil, e
	}
	if manifest.Source != "state.db" {
		return nil, errors.New("invalid migration source")
	}
	counts, e := s.ImportSQLite(ctx, filepath.Join(dir, manifest.Source))
	if e != nil {
		return nil, e
	}
	projects, e := s.ListProjects(ctx)
	if e != nil {
		return nil, e
	}
	seen := map[int64]bool{}
	beadCounts := map[int64]beads.ImportCounts{}
	for _, entry := range manifest.Exports {
		if seen[entry.ProjectID] {
			return nil, errors.New("duplicate export project")
		}
		seen[entry.ProjectID] = true
		if entry.Empty {
			return nil, errors.New("unverified empty project exports are not accepted")
		}
		if filepath.Base(entry.File) != entry.File {
			return nil, errors.New("export must be a direct migration-directory file")
		}
		f, e := os.Open(filepath.Join(dir, entry.File))
		if e != nil {
			return nil, e
		}
		n, e := beads.ImportPostgresProject(ctx, s, entry.ProjectID, f)
		f.Close()
		if e != nil {
			return nil, fmt.Errorf("project %d: %w", entry.ProjectID, e)
		}
		beadCounts[entry.ProjectID] = n
	}
	for _, p := range projects {
		if !seen[p.ID] {
			return nil, fmt.Errorf("project %d has no verified export", p.ID)
		}
	}
	if e = s.RestoreRunClaims(ctx); e != nil {
		return nil, e
	}
	return map[string]any{"migrated": true, "state_rows": counts, "beads": beadCounts, "live_backend_changed": false}, nil
}
