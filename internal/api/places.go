package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/place"
)

// RunTokenHeader carries a place run's token. Run endpoints take it and
// nothing else: the agent on the Mac never holds the api token.
const RunTokenHeader = "X-Commander-Run-Token"

// helloTimeout bounds how long a place has to answer the challenge.
const helloTimeout = 15 * time.Second

func (s *Server) placeRoutes(mux *http.ServeMux) {
	// The app (api token): pairing request and the channel.
	mux.Handle("POST /api/places/pair", s.auth(http.HandlerFunc(s.servePlacePair)))
	mux.Handle("GET /api/places/connect", s.auth(http.HandlerFunc(s.servePlaceConnect)))
	mux.Handle("POST /api/places/offer", s.auth(http.HandlerFunc(s.servePlaceOffer)))
	mux.Handle("POST /api/places/stop", s.auth(http.HandlerFunc(s.servePlaceStop)))
	// The run on the Mac (run token only): its own bead, its own Manager, its own inbox.
	mux.HandleFunc("POST /api/places/runs/{id}/comment", s.placeRun(s.servePlaceComment))
	mux.HandleFunc("POST /api/places/runs/{id}/mail", s.placeRun(s.servePlaceMailSend))
	mux.HandleFunc("GET /api/places/runs/{id}/mail", s.placeRun(s.servePlaceMailRead))
	mux.HandleFunc("POST /api/places/runs/{id}/heartbeat", s.placeRun(s.servePlaceHeartbeat))
	mux.HandleFunc("POST /api/places/runs/{id}/exit", s.placeRun(s.servePlaceExit))
}

func (s *Server) places(w http.ResponseWriter) *place.Service {
	if s.opts.Places == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "places are not enabled on this cmdrd"})
		return nil
	}
	return s.opts.Places
}

// servePlacePair records the app's device key under a one-time code. Pairing
// is complete only when the user types the code into `cmdr place pair`.
func (s *Server) servePlacePair(w http.ResponseWriter, r *http.Request) {
	if s.places(w) == nil {
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	code, err := place.RequestPairing(s.opts.PlacesHome, req.PublicKey, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code,
		"command": "cmdr place pair " + code + " --name <name>", "expires_in": int(place.PairingTTL.Seconds())})
}

// servePlaceOffer sends an offer internal-spawn-operator recorded to its place
// now, when the place is connected. It only re-sends what the store holds.
func (s *Server) servePlaceOffer(w http.ResponseWriter, r *http.Request) {
	svc := s.places(w)
	if svc == nil {
		return
	}
	var req struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	sent, err := svc.Offer(r.Context(), req.RunID)
	placeResult(w, err, map[string]any{"sent": sent})
}

// servePlaceStop ends a place run (internal-mail --interrupt): the token dies
// and the Mac is told to end the process group. It only takes power away.
func (s *Server) servePlaceStop(w http.ResponseWriter, r *http.Request) {
	svc := s.places(w)
	if svc == nil {
		return
	}
	var req struct {
		RunID int64  `json:"run_id"`
		By    string `json:"by"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	if req.By == "" {
		req.By = "the API"
	}
	ended, err := svc.Stop(r.Context(), req.RunID, req.By, "")
	placeResult(w, err, map[string]any{"ended": ended})
}

// servePlaceConnect is a place's channel: challenge, hello, then offers out and
// fetch/accept/decline in. The server sends ids, a stop and a mail signal;
// nothing it sends is a command.
func (s *Server) servePlaceConnect(w http.ResponseWriter, r *http.Request) {
	svc := s.places(w)
	if svc == nil {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	nonce := place.NewNonce()
	if err := wsjson.Write(ctx, c, place.Message{Type: "challenge", Nonce: nonce}); err != nil {
		return
	}
	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	var hello place.Message
	err = wsjson.Read(hctx, c, &hello)
	hcancel()
	if err != nil {
		return
	}
	conn, err := svc.Hello(ctx, nonce, hello)
	if err != nil {
		_ = wsjson.Write(ctx, c, place.Message{Type: "error", Error: err.Error()})
		c.Close(websocket.StatusPolicyViolation, "refused")
		return
	}
	defer svc.Drop(conn)
	if err := wsjson.Write(ctx, c, place.Message{Type: "welcome", Place: conn.Place}); err != nil {
		return
	}

	replies := make(chan place.Message, 8)
	go func() {
		defer cancel()
		for {
			var m place.Message
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			if rep := svc.Handle(ctx, conn, m); rep != nil {
				select {
				case replies <- *rep:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	write := func(m place.Message) bool {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return wsjson.Write(wctx, c, m) == nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.Gone():
			_ = write(place.Message{Type: "error", Error: place.ErrPairingGone.Error()})
			c.Close(websocket.StatusPolicyViolation, "pairing revoked")
			return
		case m := <-conn.Send:
			if !write(m) {
				return
			}
		case m := <-replies:
			if !write(m) {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// placeRun authorizes a run-token request for the run in the path. Any
// failure answers 401 with no detail about the run.
func (s *Server) placeRun(next func(http.ResponseWriter, *http.Request, *place.Run)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := s.places(w)
		if svc == nil {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": place.ErrUnauthorized.Error()})
			return
		}
		run, err := svc.Authorize(r.Context(), id, strings.TrimSpace(r.Header.Get(RunTokenHeader)))
		if err != nil {
			status := http.StatusUnauthorized
			if !errors.Is(err, place.ErrUnauthorized) {
				status = http.StatusInternalServerError
			}
			writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		next(w, r, run)
	}
}

func decodePlace(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, place.MaxComment+4<<10)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request: " + err.Error()})
		return false
	}
	return true
}

func placeResult(w http.ResponseWriter, err error, extra map[string]any) {
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := map[string]any{"ok": true}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) servePlaceComment(w http.ResponseWriter, r *http.Request, run *place.Run) {
	var req struct {
		ClientID string `json:"client_id"`
		Text     string `json:"text"`
		Report   bool   `json:"report"`
	}
	if !decodePlace(w, r, &req) {
		return
	}
	dup, err := s.opts.Places.Comment(r.Context(), run, req.ClientID, req.Text, req.Report)
	placeResult(w, err, map[string]any{"bead": run.AgentRun.TaskBeadID, "duplicate": dup})
}

func (s *Server) servePlaceMailSend(w http.ResponseWriter, r *http.Request, run *place.Run) {
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if !decodePlace(w, r, &req) {
		return
	}
	err := s.opts.Places.MailManager(r.Context(), run, req.Subject, req.Body)
	placeResult(w, err, map[string]any{"to": run.PlaceRun.ManagerRole})
}

func (s *Server) servePlaceMailRead(w http.ResponseWriter, r *http.Request, run *place.Run) {
	mails, err := s.opts.Places.ReadMail(r.Context(), run)
	placeResult(w, err, map[string]any{"mail": mails})
}

func (s *Server) servePlaceHeartbeat(w http.ResponseWriter, r *http.Request, run *place.Run) {
	var b place.Beat
	if !decodePlace(w, r, &b) {
		return
	}
	placeResult(w, s.opts.Places.Heartbeat(r.Context(), run, b), nil)
}

func (s *Server) servePlaceExit(w http.ResponseWriter, r *http.Request, run *place.Run) {
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decodePlace(w, r, &req) {
		return
	}
	placeResult(w, s.opts.Places.Exit(r.Context(), run, req.Status, req.Reason), nil)
}

// DoorRunComment carries the authenticated place run identity through the write
// door. The PostgreSQL adapter rechecks it against the current bead claim.
func (s *Server) DoorRunComment(ctx context.Context, run *place.Run, text string) error {
	return s.doorComment(ctx, run.Project.Path, run.AgentRun.TaskBeadID, text, []string{"COMMANDER_ROLE=" + run.AgentRun.AgentName, fmt.Sprintf("COMMANDER_RUN_ID=%d", run.AgentRun.ID), "COMMANDER_RUN_SESSION=" + run.AgentRun.OpencodeSessionID})
}

// DoorComment retains the legacy role-only comment path (empty role is cmdrd).
func (s *Server) DoorComment(ctx context.Context, dir, bead, role, text string) error {
	var env []string
	if role != "" {
		env = []string{"COMMANDER_ROLE=" + role}
	}
	return s.doorComment(ctx, dir, bead, text, env)
}

func (s *Server) doorComment(ctx context.Context, dir, bead, text string, env []string) error {
	// "--" ends the flags: a run's text starting with "-" is text, never a
	// flag of bd/br (both stop flag parsing there).
	req := beads.DoorRequest{Dir: dir, Args: []string{"comments", "add", bead, "--", text}, NoWait: true}
	req.Env = env
	status, resp := s.applyDoor(ctx, req)
	if status != http.StatusOK || resp.Exit != 0 || resp.Error != "" {
		return fmt.Errorf("comment on %s: %s", bead, strings.TrimSpace(resp.Error+" "+resp.Stderr))
	}
	return nil
}
