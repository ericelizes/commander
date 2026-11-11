package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/example-user/commander/internal/store"
)

func (s *Server) jurisdictionRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+LegacyPlanningInventoryPath, s.auth(http.HandlerFunc(s.serveLegacyPlanningInventory)))
	mux.Handle("POST "+JurisdictionConsolidationPath, s.auth(http.HandlerFunc(s.serveJurisdictionConsolidation)))
	mux.Handle("GET /api/commanders", s.auth(http.HandlerFunc(s.serveCommanders)))
	mux.Handle("POST /api/commanders", s.auth(http.HandlerFunc(s.serveCommanderCreate)))
	mux.Handle("GET /api/jurisdiction", s.auth(http.HandlerFunc(s.serveJurisdiction)))
	mux.Handle("POST /api/jurisdiction", s.auth(http.HandlerFunc(s.serveJurisdictionTransfer)))
}

func (s *Server) jurisdictionStore(w http.ResponseWriter) *store.Store {
	st := s.opts.StateStore
	if st == nil || !st.IsPostgres() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Commander jurisdictions require PostgreSQL"})
		return nil
	}
	return st
}

func (s *Server) serveCommanders(w http.ResponseWriter, r *http.Request) {
	st := s.jurisdictionStore(w)
	if st == nil {
		return
	}
	sessions, e := st.ListCommanders(r.Context())
	if e != nil {
		writeJSON(w, 500, map[string]string{"error": "could not read Commander conversations"})
		return
	}
	writeJSON(w, 200, map[string]any{"commanders": sessions})
}

type jurisdictionRequest struct {
	ProjectID     int64  `json:"project_id"`
	BeadID        string `json:"bead_id"`
	ExpectedOwner string `json:"expected_owner"`
	Revision      int64  `json:"revision"`
	CommanderID   string `json:"commander_id"`
	Inherit       bool   `json:"inherit"`
	Reason        string `json:"reason"`
	Title         string `json:"title"`
}

func readJurisdictionRequest(w http.ResponseWriter, r *http.Request) (jurisdictionRequest, bool) {
	var q jurisdictionRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if e := d.Decode(&q); e != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid jurisdiction request"})
		return q, false
	}
	return q, true
}

func (s *Server) serveJurisdiction(w http.ResponseWriter, r *http.Request) {
	st := s.jurisdictionStore(w)
	if st == nil {
		return
	}
	pid, e := strconv.ParseInt(r.URL.Query().Get("project_id"), 10, 64)
	bead := r.URL.Query().Get("bead_id")
	if e != nil || pid <= 0 || bead == "" {
		writeJSON(w, 400, map[string]string{"error": "project_id and bead_id are required"})
		return
	}
	j, e := st.Jurisdiction(r.Context(), pid, bead)
	if e != nil {
		writeJSON(w, 404, map[string]string{"error": "jurisdiction not found"})
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) serveCommanderCreate(w http.ResponseWriter, r *http.Request) {
	s.changeJurisdiction(w, r, true)
}
func (s *Server) serveJurisdictionTransfer(w http.ResponseWriter, r *http.Request) {
	s.changeJurisdiction(w, r, false)
}

// These authenticated user-interface endpoints do not accept an actor identity
// from JSON. Agent-attributed changes use the guarded cmdr command instead.
func (s *Server) changeJurisdiction(w http.ResponseWriter, r *http.Request, create bool) {
	st := s.jurisdictionStore(w)
	if st == nil {
		return
	}
	q, ok := readJurisdictionRequest(w, r)
	if !ok {
		return
	}
	t := store.JurisdictionTransfer{ProjectID: q.ProjectID, BeadID: q.BeadID, ExpectedOwner: q.ExpectedOwner, ExpectedRevision: q.Revision, NextCommander: q.CommanderID, Inherit: q.Inherit, Reason: q.Reason}
	if create {
		if q.Title == "" || q.CommanderID != "" || q.Inherit {
			writeJSON(w, 400, map[string]string{"error": "new conversation requires a title and scope"})
			return
		}
		var entropy [12]byte
		if _, e := rand.Read(entropy[:]); e != nil {
			writeJSON(w, 500, map[string]string{"error": "could not create conversation identity"})
			return
		}
		t.NextCommander = "commander-" + hex.EncodeToString(entropy[:])
		t.CreateTitle = q.Title
	} else if q.Title != "" {
		writeJSON(w, 400, map[string]string{"error": "transfer cannot create a conversation"})
		return
	}
	j, e := st.TransferJurisdiction(r.Context(), t)
	if e != nil {
		status := http.StatusBadRequest
		if errors.Is(e, store.ErrJurisdictionConflict) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": e.Error()})
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	writeJSON(w, status, j)
}
