package main

import (
	"context"
	"testing"
)

func TestTab_SlowSourcesDefaultToFiveMinuteRefresh(t *testing.T) {
	st := tabEnv(t, "")
	for _, u := range []string{"https://www.notion.so/Spec-0123456789abcdef0123456789abcdef", "https://linear.app/example-app/issue/TEAM-1/x"} {
		if code, _, e := tab("add", "--url", u); code != 0 {
			t.Fatalf("add %s: %s", u, e)
		}
	}
	if code, _, e := tab("add", "--url", "https://www.notion.so/Other-0123456789abcdef0123456789abcdef", "--refresh", "30s"); code != 0 {
		t.Fatal(e)
	}
	tabs, _ := st.ListTabs(context.Background(), 0, "")
	if len(tabs) != 3 || tabs[0].RefreshSecs != 300 || tabs[1].RefreshSecs != 300 || tabs[2].RefreshSecs != 30 {
		t.Errorf("tabs = %+v", tabs)
	}
}

func TestTabAdd_DecisionFlagIsAKnownValueFlag(t *testing.T) {
	flags, _, err := splitTabArgs([]string{"--decision", "ws.1", "--focus"})
	if err != nil || flags["decision"] != "ws.1" || flags["focus"] != "true" {
		t.Fatalf("flags = %v, err = %v", flags, err)
	}
}
