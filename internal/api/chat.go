package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/example-user/commander/internal/chat"
)

// chatRoutes registers the native chat API (internal/chat). Every route is
// behind the same token as the rest of /api. Roles are validated by the engine
// (commander, manager-<bead>, operator-<flat>).
func (s *Server) chatRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/chat", s.auth(http.HandlerFunc(s.serveChatList)))
	mux.Handle("GET /api/chat/{role}", s.auth(http.HandlerFunc(s.serveChatStatus)))
	mux.Handle("GET /api/chat/{role}/events", s.auth(http.HandlerFunc(s.serveChatEvents)))
	mux.Handle("GET /api/chat/{role}/mail", s.auth(http.HandlerFunc(s.serveChatMail)))
	mux.Handle("POST /api/chat/{role}/{action}", s.auth(http.HandlerFunc(s.serveChatAction)))
	mux.Handle("POST /api/chat/accounts", s.auth(http.HandlerFunc(s.serveAccountAdd)))
	mux.Handle("POST /api/chat/accounts/{name}/login", s.auth(http.HandlerFunc(s.serveAccountLogin)))
	mux.Handle("POST /api/chat/accounts/{name}/signin", s.auth(http.HandlerFunc(s.serveSigninStart)))
	mux.Handle("GET /api/chat/accounts/{name}/signin", s.auth(http.HandlerFunc(s.serveSigninState)))
	mux.Handle("DELETE /api/chat/accounts/{name}/signin", s.auth(http.HandlerFunc(s.serveSigninCancel)))
	mux.Handle("POST /api/chat/accounts/{name}/signin/code", s.auth(http.HandlerFunc(s.serveSigninCode)))
	mux.Handle("POST /api/chat/accounts/{name}/signout", s.auth(http.HandlerFunc(s.serveSignout)))
}

// ChatResponse is the body of every chat POST and GET /api/chat/{role}.
type ChatResponse struct {
	OK     bool         `json:"ok"`
	Error  string       `json:"error,omitempty"`
	Status *chat.Status `json:"status,omitempty"`
	// Delivered is "mail" when the message went to a terminal chat as mail.
	Delivered string `json:"delivered,omitempty"`
	// OwnedBy / PID are set when the session belongs to another process.
	OwnedBy string `json:"owned_by,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type chatRequest struct {
	Text     string            `json:"text"`
	Files    []chat.Attachment `json:"attachments"`
	Start    bool              `json:"start"` // send: start the process first if needed
	Model    string            `json:"model"`
	ID       string            `json:"id"`
	Answers  map[string]string `json:"answers"`
	Confirm  bool              `json:"confirm"`
	Force    bool              `json:"force"`
	Runtime  string            `json:"runtime"`  // runtime: "claude" or "codex"
	Account  string            `json:"account"`  // account: a Claude account name
	Continue bool              `json:"continue"` // runtime/account: send the agent a continue message after the switch
	chat.Decision
}

// chatEngine returns the engine once the chats running before a restart are
// picked back up (a request before that could start a second process on one).
func (s *Server) chatEngine(w http.ResponseWriter, r *http.Request) *chat.Engine {
	if s.opts.Chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, ChatResponse{Error: "native chat engine not running"})
		return nil
	}
	if err := s.opts.Chat.WaitResumed(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, ChatResponse{Error: "chats are still starting"})
		return nil
	}
	return s.opts.Chat
}

func (s *Server) serveChatList(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	list := e.List()
	if list == nil {
		list = []chat.Status{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": list,
		"accounts": chat.Accounts(e.Home(), e.Usage().All(time.Now()), time.Now()), "providers": chat.Providers(e.Home())})
}

func (s *Server) serveChatStatus(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	st, err := e.Status(r.PathValue("role"))
	s.chatReply(w, &st, err)
}

// serveChatMail lists the mail waiting for role: sender, subject, first line, time.
func (s *Server) serveChatMail(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	if !chat.ValidRole(role) {
		writeJSON(w, http.StatusBadRequest, ChatResponse{Error: "invalid role"})
		return
	}
	if s.opts.WaitingMail == nil {
		writeJSON(w, http.StatusServiceUnavailable, ChatResponse{Error: "mail not available"})
		return
	}
	mails, err := s.opts.WaitingMail(r.Context(), role)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ChatResponse{Error: err.Error()})
		return
	}
	out := make([]WaitingMail, 0, len(mails))
	for _, m := range mails {
		out = append(out, WaitingMail{ID: m.ID, From: m.From, Subject: m.Subject, Preview: firstBodyLine(m.Body), SentAt: m.SentAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mail": out})
}

// WaitingMail is one message in the app's waiting popup.
type WaitingMail struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Preview string    `json:"preview"`
	SentAt  time.Time `json:"sent_at"`
}

func firstBodyLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 160 {
				l = string(r[:159]) + "…"
			}
			return l
		}
	}
	return ""
}

func (s *Server) serveChatAction(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	role, action := r.PathValue("role"), r.PathValue("action")
	var req chatRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ChatResponse{Error: "bad JSON body: " + err.Error()})
			return
		}
	}
	ctx := r.Context()
	if (action == "account" || action == "runtime") && strings.HasPrefix(role, "operator-") && !e.Running(role) && s.opts.Run != nil {
		s.switchOperator(w, r, e, role, req)
		return
	}
	var err error
	switch action {
	case "start":
		_, err = e.Start(ctx, role, chat.StartOptions{Model: req.Model})
	case "stop":
		err = e.Stop(ctx, role)
	case "send":
		if !req.Start && !e.WaitRunning(ctx, role, 5*time.Second) && s.opts.MailToRole != nil {
			// Not native yet (Commander, or a Manager still in a terminal): deliver
			// the user's message to the live session through the mail path instead of
			// showing a raw "no running process" error.
			text := req.Text
			if len(req.Files) > 0 {
				saved, saveErr := e.SaveUploads(role, req.Files)
				if saveErr != nil {
					s.chatReply(w, nil, saveErr)
					return
				}
				text = chat.WithPaths(text, saved)
			}
			if mailErr := s.opts.MailToRole(ctx, role, text); mailErr != nil {
				writeJSON(w, http.StatusBadGateway, ChatResponse{Error: "could not deliver the message: " + mailErr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, ChatResponse{OK: true, Delivered: "mail"})
			return
		}
		if req.Start && !e.Running(role) {
			if _, err = e.Start(ctx, role, chat.StartOptions{Model: req.Model}); err != nil {
				break
			}
		}
		err = e.SendWithAttachments(ctx, role, req.Text, req.Files)
	case "interrupt":
		err = e.Interrupt(ctx, role)
	case "typing":
		err = e.MarkTalking(role)
	case "mail-now":
		err = e.ReleaseMail(role)
	case "approve":
		err = e.Approve(ctx, role, req.ID, req.Decision)
	case "answer":
		err = e.Answer(ctx, role, req.ID, req.Answers)
	case "handover":
		_, err = e.Handover(ctx, role, chat.HandoverOptions{Confirm: req.Confirm, Force: req.Force})
	case "popout":
		err = e.PopOut(ctx, role, chat.HandoverOptions{Confirm: req.Confirm, Force: req.Force})
	case "runtime":
		_, err = e.SetRuntime(ctx, role, req.Runtime, chat.SwitchOptions{Continue: req.Continue})
	case "account":
		_, err = e.SetAccount(ctx, role, req.Account, chat.SwitchOptions{Continue: req.Continue})
	case "fresh":
		_, err = e.Fresh(ctx, role)
	default:
		writeJSON(w, http.StatusNotFound, ChatResponse{Error: "unknown chat action " + action})
		return
	}
	if err != nil {
		s.chatReply(w, nil, err)
		return
	}
	go s.Refresh(context.Background()) // agent state changed
	st, serr := e.Status(role)
	s.chatReply(w, &st, serr)
}

func (s *Server) chatReply(w http.ResponseWriter, st *chat.Status, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, ChatResponse{OK: true, Status: st})
		return
	}
	resp := ChatResponse{Error: err.Error()}
	code := http.StatusBadRequest
	var owned *chat.OwnedError
	switch {
	case errors.As(err, &owned):
		code, resp.OwnedBy, resp.PID = http.StatusConflict, owned.By, owned.PID
	case errors.Is(err, chat.ErrNotRunning), errors.Is(err, chat.ErrBusy):
		code = http.StatusConflict
	case errors.Is(err, chat.ErrNoRequest):
		code = http.StatusNotFound
	case errors.Is(err, chat.ErrHandover):
		code = http.StatusForbidden
	}
	writeJSON(w, code, resp)
}

// serveChatEvents is a WebSocket: every event with seq > since (history), one
// {"kind":"replay_done","seq":<last>} marker, then live events. When the
// client falls too far behind the server closes with code 4000; reconnect
// with since = the last seq received.
func (s *Server) serveChatEvents(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	role := r.PathValue("role")
	if !chat.ValidRole(role) {
		writeJSON(w, http.StatusBadRequest, ChatResponse{Error: "invalid role"})
		return
	}
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ChatResponse{Error: "since must be a number"})
			return
		}
		since = n
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	replay, sub, err := e.Subscribe(role, since)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer sub.Close()
	ctx := c.CloseRead(r.Context())
	write := func(v any) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return wsjson.Write(wctx, c, v)
	}
	last := since
	for _, ev := range replay {
		if write(ev) != nil {
			return
		}
		last = ev.Seq
	}
	if write(chat.Event{Kind: chat.KindReplayDone, Role: role, Seq: last, Time: time.Now(), Source: chat.SourceEngine}) != nil {
		return
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				_ = c.Close(4000, "too slow: reconnect with since")
				return
			}
			if write(ev) != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// switchOperator moves a host Operator (a headless run, not a chat here) to
// another account or runtime: cmdr internal-operator-provider respawns it on
// the same bead, branch and worktree.
func (s *Server) switchOperator(w http.ResponseWriter, r *http.Request, e *chat.Engine, role string, req chatRequest) {
	argv := []string{"internal-operator-provider", role}
	if req.Account != "" {
		argv = append(argv, "--account", req.Account)
	}
	if req.Runtime != "" {
		argv = append(argv, "--runtime", req.Runtime)
	}
	res := s.opts.Run(r.Context(), argv)
	if !res.OK {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = res.Error
		}
		writeJSON(w, http.StatusBadRequest, ChatResponse{Error: msg})
		return
	}
	go s.Refresh(context.Background())
	st, err := e.Status(role)
	s.chatReply(w, &st, err)
}
