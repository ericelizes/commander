package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// Exact reads must replace every old holder regardless of kind or label order.
// The complete write trace also excludes changes to waits, cards, comments or
// historical writer bindings: custody is only an owner label change.
func TestExactTaskHolderCleanup(t *testing.T) {
	for _, kind := range []string{"kind:task", "kind:objective"} {
		for _, reverse := range []bool{false, true} {
			for _, tc := range []struct{ old, next string }{
				{"manager-example", "operator-reviewer"},
				{"operator-writer", "operator-replacement"},
				{"operator-writer", "none"},
			} {
				t.Run(kind+"/"+tc.old+"/"+tc.next+map[bool]string{true: "/reverse"}[reverse], func(t *testing.T) {
					labels := []string{kind, "owner:" + tc.old, "owner:operator-stale", "until:approved", "waiting:100:operator:reviewer", "approver:user", "writer:operator-writer"}
					if reverse {
						for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
							labels[i], labels[j] = labels[j], labels[i]
						}
					}
					original := append([]string(nil), labels...)
					bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1", Labels: labels}, ListResult: []beads.Bead{{ID: "p-other", Labels: []string{"kind:objective", "owner:manager-other"}}}}
					if err := applyStepOwner(context.Background(), bc, "/p", "p-1", tc.next); err != nil {
						t.Fatal(err)
					}
					want := []string{"Show:p-1|dir=/p"}
					for _, l := range labels {
						if l == "owner:"+tc.old || l == "owner:operator-stale" {
							want = append(want, "SetLabel:p-1|label="+l+"|on=false|dir=/p")
						}
					}
					if tc.next != "none" {
						want = append(want, "SetLabel:p-1|label=owner:"+tc.next+"|on=true|dir=/p")
					}
					if !reflect.DeepEqual(bc.Calls, want) {
						t.Fatalf("calls=%q want=%q", bc.Calls, want)
					}
					mem := &beads.MemClient{}
					mem.Seed(beads.Bead{ID: "p-1", Labels: append([]string(nil), original...)})
					if err := applyStepOwner(context.Background(), mem, "/p", "p-1", tc.next); err != nil {
						t.Fatal(err)
					}
					result, err := mem.Show(context.Background(), "/p", "p-1")
					if err != nil {
						t.Fatal(err)
					}
					expected := slices.DeleteFunc(append([]string(nil), original...), func(l string) bool { return l == "owner:"+tc.old || l == "owner:operator-stale" })
					if tc.next != "none" {
						expected = append(expected, "owner:"+tc.next)
					}
					slices.Sort(expected)
					slices.Sort(result.Labels)
					if !reflect.DeepEqual(result.Labels, expected) {
						t.Fatalf("labels=%q want=%q", result.Labels, expected)
					}
					if !reflect.DeepEqual(labels, original) {
						t.Fatal("changed source bead")
					}
				})
			}
		}
	}
}

func TestExactTaskUntilCleanup(t *testing.T) {
	for _, next := range []string{"merged", "none"} {
		t.Run(next, func(t *testing.T) {
			bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1", Labels: []string{"kind:task", "owner:operator-writer", "until:approved", "until:live", "waiting:100:user", "approver:user"}}}
			if err := applyUntil(context.Background(), bc, "/p", "p-1", next); err != nil {
				t.Fatal(err)
			}
			want := []string{"Show:p-1|dir=/p", "SetLabel:p-1|label=until:approved|on=false|dir=/p", "SetLabel:p-1|label=until:live|on=false|dir=/p"}
			if next != "none" {
				want = append(want, "SetLabel:p-1|label=until:"+next+"|on=true|dir=/p")
			}
			if !reflect.DeepEqual(bc.Calls, want) {
				t.Fatalf("calls=%q want=%q", bc.Calls, want)
			}
		})
	}
}

func TestExactHolderReadFailureDoesNotWrite(t *testing.T) {
	failure := errors.New("show failed")
	for _, apply := range []func(context.Context, beads.Client, string, string, string) error{applyStepOwner, applyUntil} {
		bc := &beads.MockClient{ShowErr: failure}
		if err := apply(context.Background(), bc, "/p", "p-1", "none"); !errors.Is(err, failure) {
			t.Fatalf("err=%v", err)
		}
		if !reflect.DeepEqual(bc.Calls, []string{"Show:p-1|dir=/p"}) {
			t.Fatalf("calls=%q", bc.Calls)
		}
	}
}
