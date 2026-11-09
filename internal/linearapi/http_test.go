package linearapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryDoesNotExposeUpstreamErrors(t *testing.T) {
	for _, do := range []HTTPDo{
		func(*http.Request) (*http.Response, error) { return nil, errors.New("credential") },
		func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("credential"))}, nil
		},
	} {
		body, status := Query(context.Background(), do, "https://example.invalid", "credential", []byte(`{}`))
		if len(body) != 0 || status/100 == 2 {
			t.Fatal("exposed upstream text")
		}
	}
}
func TestQueryRefusesRedirect(t *testing.T) {
	reached := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer src.Close()
	body, status := Query(context.Background(), nil, src.URL, "credential", nil)
	if status != 302 || len(body) != 0 || reached {
		t.Fatalf("redirect followed: %d %v", status, reached)
	}
}
