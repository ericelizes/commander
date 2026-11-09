package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/linearsync"
)

func TestLinearCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	var out, errs bytes.Buffer
	if code := runLinear([]string{"status"}, strings.NewReader(""), &out, &errs); code != 0 || !strings.Contains(out.String(), linearsync.KeyPath(home)) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("%d %s %s", code, &out, &errs)
	}
	out.Reset()
	if code := runLinear([]string{"set-key"}, strings.NewReader("secret-cli-key\n"), &out, &errs); code != 0 {
		t.Fatalf("%d %s", code, &errs)
	}
	info, err := os.Stat(linearsync.KeyPath(home))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	if code := runLinear([]string{"status"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatal(code)
	}
	if code := runLinear([]string{"set-key", "secret-cli-key"}, strings.NewReader(""), &out, &errs); code != 2 {
		t.Fatal("accepted argv key")
	}
	if code := runLinear([]string{"set-key"}, strings.NewReader("\n"), &out, &errs); code != 1 {
		t.Fatal("accepted empty")
	}
	if strings.Contains(out.String()+errs.String(), "secret-cli-key") {
		t.Fatal("leaked key")
	}
}
