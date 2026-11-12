package daemon

import "testing"

func TestPRCreatedReadsTheNewPullRequest(t *testing.T) {
	repo, n, ok := prCreated(`{"command":"gh pr create --fill --base main"}`, "Creating pull request...\nhttps://github.com/acme/caesar/pull/20412\n")
	if !ok || repo != "acme/caesar" || n != 20412 {
		t.Fatalf("%q %d %v", repo, n, ok)
	}
	if _, _, ok := prCreated(`{"command":"gh pr view 12"}`, "https://github.com/acme/caesar/pull/12"); ok {
		t.Fatal("viewing a pull request is not creating one")
	}
	if got := prLabel("acme/vibe", "acme/vibe", 7); got != "pr:7" {
		t.Fatal(got)
	}
	if got := prLabel("acme/vibe", "acme/caesar", 9); got != "pr:acme/caesar#9" {
		t.Fatal(got)
	}
}
