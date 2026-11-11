package chat

import (
	"reflect"
	"testing"
)

func TestOrderAccountsExpandsProvidersAndNames(t *testing.T) {
	accounts := []AccountView{{Name: "default", Runtime: RuntimeClaude}, {Name: "work", Runtime: RuntimeClaude}, {Name: "codex", Runtime: RuntimeCodex}}
	got := OrderAccounts([]string{"work", RuntimeCodex, RuntimeClaude, "missing"}, accounts)
	want := []Provider{{Runtime: RuntimeClaude, Account: "work"}, {Runtime: RuntimeCodex}, {Runtime: RuntimeClaude, Account: "default"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
