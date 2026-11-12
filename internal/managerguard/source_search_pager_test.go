package managerguard_test

import (
	"testing"

	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/operatorguard"
)

// Command strings are classifier data only; never execute the pager.
func TestSourceSearchPagerOptions(t *testing.T) {
	request := "http://localhost:8787/" + "api/" + "up" + "date"
	for _, flag := range []string{"-nO ", "-inO ", "-nO", "-inO", "-O ", "-O", "--open-files-in-pager ", "--open-files-in-pager="} {
		t.Run(flag, func(t *testing.T) {
			command := "git grep " + flag + "'curl -X POST " + request + "' pattern -- internal"
			if !managerguard.Classify(command, "").Deny || !operatorguard.Classify(command).Deny {
				t.Fatal("executable pager allowed")
			}
		})
	}
	for _, command := range []string{
		"git grep -in '" + request + "' -- internal",
		"rg -n '" + request + "' internal",
	} {
		if managerguard.Classify(command, "").Deny || operatorguard.Classify(command).Deny {
			t.Fatal("inert search refused")
		}
	}
}
