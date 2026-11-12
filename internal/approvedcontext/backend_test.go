package approvedcontext

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestRestoreExactBackend(t *testing.T) {
	// This exercises the legacy CLI wire contract with a fake executable, not
	// the backend selected by the caller's live Commander installation.
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if beads.PostgresConfigured() {
		t.Fatal("legacy fixture unexpectedly selected Postgres")
	}
	for _, tc := range []struct {
		name, raw string
		deny      bool
	}{
		{"native omitted empty", `native`, false}, {"native omitted whitespace", `native-whitespace`, true}, {"native omitted padded", `native-padded`, true}, {"empty", `""`, false}, {"whitespace", `" \t\n"`, true},
		{"different padded", `" scope "`, true}, {"missing", `missing`, true},
		{"null", `null`, true}, {"wrong type", `7`, true}, {"malformed", `broken`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			st, err := store.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: dir, Type: "code"})
			if err != nil {
				t.Fatal(err)
			}
			scope := " \tExact scope.\nPreserve limits.\n "
			decision, _ := json.Marshal(store.Decision{Question: "Proceed?", Context: scope, Input: true})
			if err = st.RecordPitch(ctx, pid, "demo-gate", "Proceed?", string(decision), nil); err != nil {
				t.Fatal(err)
			}
			r := store.NeedsResolution{ProjectID: pid, BeadID: "demo-gate", Generation: "1", AnswerID: "fixture", Action: "approve"}
			if err = st.SaveNeedsResolution(ctx, r, "Approved"); err != nil {
				t.Fatal(err)
			}
			if err = st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "description"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "fake-br")
			script := `#!/usr/bin/env python3
import sys,json,pathlib
a=sys.argv[1:]; p=pathlib.Path('description')
if a[0]=='show':
 if '--json' not in a:
  print('ID: demo-gate\nStatus: closed\nLabels: gate, approver:user'); sys.exit()
 raw=p.read_text()
 if raw=='broken': print('{'); sys.exit()
 b={'id':'demo-gate','status':'closed','labels':['gate','approver:user'],'dependencies':[]}
 if raw.startswith('native'):
  b.update(title='Example gate',priority=2,issue_type='task',created_at='2026-01-01T00:00:00Z',updated_at='2026-01-02T00:00:00Z',dependent_count=0,dependency_count=0,comment_count=0,revision='123')
 elif raw!='missing': b['description']=json.loads(raw)
 print(json.dumps([b]))
elif a==['--version']: print('bd version 1.3.1 (c1c4b642a)')
elif a[0]=='--readonly':
 assert a==['--readonly','sql',"SELECT id, description FROM issues WHERE id = 'demo-gate'",'--json'],a
 raw=p.read_text()
 value={'native':'','native-whitespace':' \t\n','native-padded':' scope '}[raw]
 print(json.dumps([{'id':'demo-gate','description':value}]))
elif a[0]=='update':
 assert a[1:3]==['demo-gate','--description'],a
 p.write_text(json.dumps(a[3])); pathlib.Path('writes').write_text('write')
else: raise Exception(a)
`
			if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COMMANDER_BEADS_BIN", bin)
			t.Setenv(beads.DoorEnv, "off")
			bc := beads.New()
			restore := func(dry bool) (Result, error) {
				return Restore(ctx, st, bc, store.Project{ID: pid, Path: dir}, "demo-gate", "user", 1, "fixture", dry)
			}
			if _, err = restore(true); (err != nil) != tc.deny {
				t.Fatalf("dry: %v", err)
			}
			if _, err = os.Stat(filepath.Join(dir, "writes")); !os.IsNotExist(err) {
				t.Fatal("dry wrote")
			}
			result, err := restore(false)
			if (err != nil) != tc.deny {
				t.Fatalf("%+v %v", result, err)
			}
			if tc.deny {
				if _, err = os.Stat(filepath.Join(dir, "writes")); !os.IsNotExist(err) {
					t.Fatal("refusal wrote")
				}
				return
			}
			exact, err := bc.(beads.ExactDescriptionReader).ExactDescription(ctx, dir, "demo-gate")
			if err != nil || exact != scope || !result.Changed {
				t.Fatalf("exact=%q result=%+v err=%v", exact, result, err)
			}
			if err = os.Remove(filepath.Join(dir, "writes")); err != nil {
				t.Fatal(err)
			}
			result, err = restore(false)
			if err != nil || result.Changed {
				t.Fatalf("retry %+v %v", result, err)
			}
			if _, err = os.Stat(filepath.Join(dir, "writes")); !os.IsNotExist(err) {
				t.Fatal("retry wrote")
			}
		})
	}
}
