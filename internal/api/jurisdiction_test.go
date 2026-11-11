package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresFocusedCommanderCreationAndHandoffAreAtomic(t *testing.T) {
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "threads", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'urgent','Urgent','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	src := &fakeSource{}
	src.set("unchanged sidebar")
	handler := New(Options{Token: testToken, StateStore: st, Source: src.load}).Handler()
	request := func(path, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	body := fmt.Sprintf(`{"project_id":%d,"bead_id":"urgent","expected_owner":"commander","revision":1,"title":"Deployment incident","reason":"urgent investigation"}`, pid)
	if w := request("/api/commanders", body, false); w.Code != 401 {
		t.Fatalf("unauthenticated create=%d", w.Code)
	}
	w := request("/api/commanders", body, true)
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var j store.Jurisdiction
	if e = json.Unmarshal(w.Body.Bytes(), &j); e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(j.CommanderID, "commander-") || j.Revision != 2 {
		t.Fatalf("bad created scope: %+v", j)
	}
	if w = request("/api/commanders", body, true); w.Code != 409 {
		t.Fatalf("stale creation=%d %s", w.Code, w.Body.String())
	}
	sessions, e := st.ListCommanders(ctx)
	if e != nil || len(sessions) != 2 {
		t.Fatalf("failed create leaked conversation: %+v %v", sessions, e)
	}
	back := fmt.Sprintf(`{"project_id":%d,"bead_id":"urgent","expected_owner":%q,"revision":2,"inherit":true,"reason":"incident resolved"}`, pid, j.CommanderID)
	if w = request("/api/jurisdiction", back, true); w.Code != 200 {
		t.Fatalf("return=%d %s", w.Code, w.Body.String())
	}
	owner, e := st.Jurisdiction(ctx, pid, "urgent")
	if e != nil || owner.CommanderID != "commander" {
		t.Fatalf("return lost default owner: %+v %v", owner, e)
	}
	session, e := st.GetCommander(ctx, j.CommanderID)
	if e != nil || session == nil || session.Title != "Deployment incident" {
		t.Fatal("return discarded conversation", e)
	}
}
