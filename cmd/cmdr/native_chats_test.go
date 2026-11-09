package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCmdrdChatsPostsStartWithToken(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("tok\n"), 0o600)
	if err := (cmdrdChats{home: home}).Start(context.Background(), "manager-x"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/chat/manager-x/start" || auth != "Bearer tok" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
}

func TestCmdrdChatsErrorsWhenCmdrdIsDownOrRefuses(t *testing.T) {
	home := t.TempDir()
	if err := (cmdrdChats{home: home}).Start(context.Background(), "manager-x"); err == nil {
		t.Fatal("no api-url must be an error (caller falls back to a terminal)")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"owned"}`, http.StatusConflict)
	}))
	defer srv.Close()
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	if err := (cmdrdChats{home: home}).Start(context.Background(), "manager-x"); err == nil {
		t.Fatal("409 must be an error")
	}
}
