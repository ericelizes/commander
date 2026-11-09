package api

import (
	"encoding/json"
	"github.com/example-user/commander/internal/linearsync"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) serveLinear(w http.ResponseWriter, r *http.Request) {
	result := linearsync.Response{State: "off", Issues: []linearsync.Issue{}}
	if s.opts.Linear != nil {
		result = s.opts.Linear.Get(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	key := "not set"
	if value, err := linearsync.ReadKey(filepath.Dir(s.opts.ConfigPath)); err == nil && value != "" {
		key = "saved"
	}
	json.NewEncoder(w).Encode(struct {
		linearsync.Response
		Key string `json:"key"`
	}{result, key})
}

// Credential bodies are decoded locally and never logged or included in errors.
func (s *Server) serveLinearKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if s.opts.ConfigPath == "" {
		http.Error(w, "Linear key unavailable", 503)
		return
	}
	home := filepath.Dir(s.opts.ConfigPath)
	if r.Method == http.MethodDelete {
		if err := os.Remove(linearsync.KeyPath(home)); err != nil && !os.IsNotExist(err) {
			http.Error(w, "Could not remove Linear key", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"state": "not set"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "Invalid Linear key", 400)
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		http.Error(w, "Invalid Linear key", 400)
		return
	}
	if err := linearsync.ValidateKey(body.Key); err != nil {
		http.Error(w, "Invalid Linear key", 400)
		return
	}
	if err := linearsync.WriteKey(home, body.Key); err != nil {
		http.Error(w, "Could not save Linear key", 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"state": "saved"})
}
