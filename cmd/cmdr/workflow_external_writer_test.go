package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Runs the real `cmdr workflow bind-writers` dispatch (flags, authorization,
// lock, GitHub reads through the fake, the binding comment) and then the real
// close door of the review step. No live project, agent or GitHub call.
func TestExternalWriterBindingScenario(t *testing.T) {
	var sc struct {
		PR struct {
			Head    string   `json:"head"`
			Commits []string `json:"commits"`
			Range   struct {
				From, To string
				Commits  []string
			} `json:"range"`
		} `json:"pr"`
		Cases []struct {
			Name, Role, Manager, Stderr string
			Args                        []string
			Exit                        int
			Commits, Tasks              []string
			ReviewCloses                bool `json:"review_closes"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../tests/evals/scenarios/external-writer-binding.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ctx := context.Background()
			t.Setenv("COMMANDER_HOME", t.TempDir())
			mem := &beads.MemClient{}
			res := seedReviewWorkflow(t, mem)
			mem.Seed(beads.Bead{ID: "fix-task", Status: "closed"})
			mem.SetParent(ctx, "/d", "fix-task", "B")
			var oids []string
			for _, c := range sc.PR.Commits {
				oids = append(oids, `{"oid":"`+c+`"}`)
			}
			pinWorkflowGH(t, func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "pr" {
					return []byte(`{"headRefOid":"` + sc.PR.Head + `","state":"OPEN","commits":[` + strings.Join(oids, ",") + `],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
				}
				if args[0] == "api" && strings.Contains(args[1], "/compare/"+sc.PR.Range.From+"..."+sc.PR.Range.To) {
					if slices.Contains(args, "--paginate") {
						return []byte(strings.Join(sc.PR.Range.Commits, "\n")), nil
					}
					return []byte("ahead " + strconv.Itoa(len(sc.PR.Range.Commits))), nil
				}
				return workflowFakeGH(ctx, args...)
			})
			oldProject, oldClient := resolveProjectRecFn, newBeadsClientFn
			resolveProjectRecFn = func(string) (*store.Project, error) { return &store.Project{Name: "p", Path: "/d"}, nil }
			newBeadsClientFn = func() beads.Client { return mem }
			t.Cleanup(func() { resolveProjectRecFn, newBeadsClientFn = oldProject, oldClient })
			t.Setenv("COMMANDER_ROLE", tc.Role)
			t.Setenv("COMMANDER_MANAGER_ROLE", tc.Manager)
			out, _ := os.CreateTemp(t.TempDir(), "out")
			errf, _ := os.CreateTemp(t.TempDir(), "err")
			args := append([]string{"bind-writers", "--project", "p", "--block", res.Container, "--head", sc.PR.Head}, tc.Args...)
			before, _ := mem.Comments(ctx, "/d", res.Container)
			code := runWorkflow(args, out, errf)
			stdout, _ := os.ReadFile(out.Name())
			stderr, _ := os.ReadFile(errf.Name())
			if code != tc.Exit {
				t.Fatalf("exit %d want %d: %s%s", code, tc.Exit, stdout, stderr)
			}
			after, _ := mem.Comments(ctx, "/d", res.Container)
			if tc.Exit != 0 {
				if !strings.Contains(string(stderr), tc.Stderr) {
					t.Fatalf("stderr %q want %q", stderr, tc.Stderr)
				}
				if len(after) != len(before) {
					t.Fatal("refusal wrote a binding")
				}
				return
			}
			var printed workflowtemplate.WriterBinding
			if err := json.Unmarshal(stdout, &printed); err != nil {
				t.Fatal(err, string(stdout))
			}
			stored, err := workflowtemplate.Binding(after)
			if err != nil || stored.Revision != printed.Revision || stored.External == nil || stored.External.Author != "user" ||
				!slices.Equal(stored.External.Commits, tc.Commits) || !slices.Equal(stored.Tasks, tc.Tasks) {
				t.Fatalf("stored %+v printed %+v err %v", stored, printed, err)
			}
			if !tc.ReviewCloses {
				return
			}
			// The reviewer once ran as the earlier-bound writer-task: no longer a writer.
			oldRoles := workflowTaskRoles
			workflowTaskRoles = func(_ context.Context, _, task string) ([]string, error) {
				if task == "review-task" {
					return []string{"operator-writer-task", "operator-review"}, nil
				}
				return []string{"operator-" + task}, nil
			}
			t.Cleanup(func() { workflowTaskRoles = oldRoles })
			t.Setenv("COMMANDER_ROLE", "manager-B")
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[0].BeadID); err != nil {
				t.Fatal(err)
			}
			mem.Close(ctx, "/d", res.Remaining[0].BeadID)
			mem.Seed(beads.Bead{ID: "review-task", Status: "closed"})
			mem.SetParent(ctx, "/d", "review-task", "B")
			mem.AddComment(ctx, "/d", "review-task", "Head: "+sc.PR.Head+"\nCLEAN")
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: sc.PR.Head, NoFindings: true})
			mem.AddComment(ctx, "/d", res.Remaining[1].BeadID, workflowtemplate.ProofPrefix+string(raw))
			if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[1].BeadID); err != nil {
				t.Fatal(err)
			}
			cs, _ := mem.Comments(ctx, "/d", res.Remaining[1].BeadID)
			if e, ok := workflowtemplate.ParseEvidence(cs[len(cs)-1].Text); !ok || e.Binding != stored.Revision || e.Head != sc.PR.Head {
				t.Fatal(cs)
			}
		})
	}
}
