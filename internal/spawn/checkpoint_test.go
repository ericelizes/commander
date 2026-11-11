package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

func TestCheckpointRequiresOriginalSessionProviderAndNativeThread(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	sessionFile := filepath.Join(home, "session")
	write := func(path, value string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	threadFile := chat.CodexThreadFile(home, "operator-checkpoint")
	r := SpawnRequest{Resume: true, ResumeSourceRunID: 42, ResumeSessionID: hostSessionID, ResumeRuntime: chat.RuntimeCodex, Runtime: chat.RuntimeCodex, ResumeNativeThread: "native-thread-original"}
	check := func(wantOK bool) {
		t.Helper()
		err := validateCheckpointSession(r, "operator-checkpoint", sessionFile, hostSessionID)
		if (err == nil) != wantOK {
			t.Fatalf("checkpoint acceptance=%t, error=%v", wantOK, err)
		}
	}
	check(false)
	write(sessionFile, hostSessionID)
	check(false)
	write(threadFile, "native-thread-replaced")
	check(false)
	write(threadFile, r.ResumeNativeThread)
	check(true)
	write(sessionFile, "different-logical-session")
	check(false)
	write(sessionFile, hostSessionID)
	r.Runtime = "claude"
	check(false)
	r.ResumeRuntime = "claude"
	r.ResumeNativeThread = ""
	check(false)
	write(filepath.Join(claudeacct.ProjectsDir(home, claudeacct.Default), "fixture", hostSessionID+".jsonl"), "{}\n")
	check(true)
}

func TestNativeCheckpointUsesRecordedSessionThreadAndAccountWithoutRoleCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	actor := "operator-native-checkpoint"
	r := SpawnRequest{Resume: true, ResumeDriver: "native", ResumeSourceRunID: 42, ResumeSessionID: hostSessionID, ResumeRuntime: chat.RuntimeCodex, Runtime: chat.RuntimeCodex, ResumeNativeThread: "exact-saved-thread", ResumeAccount: "default", Account: "default"}
	sessionFile := filepath.Join(home, "missing-derived-session")
	path := chat.NativeCodexThreadFile(home, actor, r.ResumeSessionID)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(r.ResumeNativeThread), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(chat.CodexThreadFile(home, actor), []byte("unrelated-role-thread"), 0600); e != nil {
		t.Fatal(e)
	}
	if got := checkpointSessionID(r, sessionFile); got != r.ResumeSessionID {
		t.Fatal("native identity came from a derived file", got)
	}
	if e := validateCheckpointSession(r, actor, sessionFile, r.ResumeSessionID); e != nil {
		t.Fatal("canonical checkpoint depended on role copy", e)
	}
	bad := r
	bad.Account = "different-account"
	if e := validateCheckpointSession(bad, actor, sessionFile, r.ResumeSessionID); e == nil {
		t.Fatal("checkpoint silently moved accounts")
	}
	bad = r
	bad.ResumeNativeThread = "different-thread"
	if e := validateCheckpointSession(bad, actor, sessionFile, r.ResumeSessionID); e == nil {
		t.Fatal("checkpoint silently moved threads")
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := validateCheckpointSession(r, actor, sessionFile, r.ResumeSessionID); e != nil {
		t.Fatal("exact saved native cursor could not be restored", e)
	}
	if e := prepareNativeCheckpoint(r, actor); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != r.ResumeNativeThread+"\n" {
		t.Fatal("cursor recovery borrowed the foreign role thread", string(raw), e)
	}
	bad = r
	bad.ResumeNativeThread = ""
	if e := validateCheckpointSession(bad, actor, sessionFile, r.ResumeSessionID); e == nil {
		t.Fatal("native resume had no saved conversation identity")
	}
}

func TestLegacyCodexCheckpointPromotesExactThreadWithoutReplacingAnotherContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	actor := "operator-promoted-checkpoint"
	sessionFile := filepath.Join(home, "session")
	r := SpawnRequest{Resume: true, ResumeSourceRunID: 9, ResumeSessionID: hostSessionID, ResumeRuntime: chat.RuntimeCodex, Runtime: chat.RuntimeCodex, ResumeNativeThread: "legacy-exact-thread"}
	if e := os.MkdirAll(filepath.Join(home, "chat"), 0700); e != nil {
		t.Fatal(e)
	}
	for path, value := range map[string]string{sessionFile: r.ResumeSessionID, chat.CodexThreadFile(home, actor): r.ResumeNativeThread} {
		if e := os.WriteFile(path, []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := validateCheckpointSession(r, actor, sessionFile, r.ResumeSessionID); e != nil {
		t.Fatal(e)
	}
	if e := prepareNativeCheckpoint(r, actor); e != nil {
		t.Fatal(e)
	}
	if e := prepareNativeCheckpoint(r, actor); e != nil {
		t.Fatal("exact retry failed", e)
	}
	native := chat.NativeCodexThreadFile(home, actor, r.ResumeSessionID)
	raw, e := os.ReadFile(native)
	if e != nil || string(raw) != r.ResumeNativeThread+"\n" {
		t.Fatal("wrong promoted thread", string(raw), e)
	}
	r.ResumeNativeThread = "different-saved-thread"
	if e = prepareNativeCheckpoint(r, actor); e == nil {
		t.Fatal("promotion replaced another native context")
	}
	raw, e = os.ReadFile(native)
	if e != nil || string(raw) != "legacy-exact-thread\n" {
		t.Fatal("refused promotion changed the original context", string(raw), e)
	}
}
