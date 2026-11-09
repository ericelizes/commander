package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// captureTimeout is how long /api/app/screenshot waits for the app to answer.
var captureTimeout = 10 * time.Second

// maxCaptureBytes bounds one capture reply (a base64 PNG plus the page text).
const maxCaptureBytes = 64 << 20

// appClient is one app window connected to /api/app/connect. cmdrd asks it for
// a capture over the socket; the window replies with a PNG and its visible text.
type appClient struct {
	kind string // "electron" or "web", for the reply only
	send chan captureRequest
	// replies receives the matching captureReply; set per request.
	reply atomic.Pointer[pendingCapture]
}

type pendingCapture struct {
	id string
	ch chan captureReply
}

type captureRequest struct {
	Type string `json:"type"` // "capture"
	ID   string `json:"id"`
}

// captureReply is the app's answer. PNG is base64; Source says how it was made
// ("electron-capturePage", or "dom" for the renderer's own DOM render).
type captureReply struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	PNG    string `json:"png,omitempty"`
	Text   string `json:"text,omitempty"`
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ShotResult is the response of /api/app/screenshot.
type ShotResult struct {
	OK       bool   `json:"ok"`
	PNG      string `json:"png,omitempty"`       // path of the PNG, when the app sent one
	TextPath string `json:"text_path,omitempty"` // path of the text dump
	Text     string `json:"text,omitempty"`
	Source   string `json:"source,omitempty"`
	Warning  string `json:"warning,omitempty"` // e.g. the picture failed but the text arrived
	Error    string `json:"error,omitempty"`
}

func (s *Server) registerAppShot(mux *http.ServeMux) {
	mux.Handle("GET /api/app/connect", s.auth(http.HandlerFunc(s.serveAppConnect)))
	mux.Handle("GET /api/app/screenshot", s.auth(http.HandlerFunc(s.serveAppScreenshot)))
	mux.Handle("POST /api/app/screenshot", s.auth(http.HandlerFunc(s.serveAppScreenshot)))
}

// serveAppConnect holds one app window's socket: requests go out, replies come back.
func (s *Server) serveAppConnect(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(maxCaptureBytes)

	cl := &appClient{kind: r.URL.Query().Get("kind"), send: make(chan captureRequest, 1)}
	s.appMu.Lock()
	s.appClients = append(s.appClients, cl)
	s.appMu.Unlock()
	defer func() {
		s.appMu.Lock()
		for i, o := range s.appClients {
			if o == cl {
				s.appClients = append(s.appClients[:i], s.appClients[i+1:]...)
				break
			}
		}
		s.appMu.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // reader: route replies to the waiting request
		defer cancel()
		for {
			var rep captureReply
			if err := wsjson.Read(ctx, c, &rep); err != nil {
				return
			}
			if p := cl.reply.Load(); p != nil && rep.Type == "capture" && rep.ID == p.id {
				select {
				case p.ch <- rep:
				default:
				}
			}
		}
	}()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-cl.send:
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(wctx, c, req)
			wcancel()
			if err != nil {
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

var captureSeq atomic.Uint64

// capture asks the newest connected window for a screenshot.
func (s *Server) capture(ctx context.Context) (captureReply, error) {
	s.appMu.Lock()
	var cl *appClient
	if n := len(s.appClients); n > 0 {
		cl = s.appClients[n-1]
	}
	s.appMu.Unlock()
	if cl == nil {
		return captureReply{}, errNoApp
	}
	p := &pendingCapture{id: strconv.FormatUint(captureSeq.Add(1), 10), ch: make(chan captureReply, 1)}
	cl.reply.Store(p)
	defer cl.reply.CompareAndSwap(p, nil)
	select {
	case cl.send <- captureRequest{Type: "capture", ID: p.id}:
	case <-ctx.Done():
		return captureReply{}, ctx.Err()
	}
	t := time.NewTimer(captureTimeout)
	defer t.Stop()
	select {
	case rep := <-p.ch:
		return rep, nil
	case <-t.C:
		return captureReply{}, fmt.Errorf("the app window did not answer within %s", captureTimeout)
	case <-ctx.Done():
		return captureReply{}, ctx.Err()
	}
}

var errNoApp = fmt.Errorf("no app window connected: open the Commander app (or %s) and retry", "cmdr app --web")

func (s *Server) serveAppScreenshot(w http.ResponseWriter, r *http.Request) {
	if s.opts.ShotsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, ShotResult{Error: "shots dir not configured"})
		return
	}
	rep, err := s.capture(r.Context())
	if err != nil {
		status := http.StatusGatewayTimeout
		if err == errNoApp {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, ShotResult{Error: err.Error()})
		return
	}
	res, err := writeShot(s.opts.ShotsDir, rep)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ShotResult{Error: err.Error()})
		return
	}
	status := http.StatusOK
	if !res.OK {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, res)
}

// writeShot stores a reply as app.png and app-text.txt under dir. The text is
// worth keeping even when the picture failed (and the other way round).
func writeShot(dir string, rep captureReply) (ShotResult, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ShotResult{}, err
	}
	res := ShotResult{Source: rep.Source, Text: rep.Text}
	if rep.PNG != "" {
		raw, err := base64.StdEncoding.DecodeString(rep.PNG)
		if err != nil {
			return ShotResult{}, fmt.Errorf("bad png from the app: %w", err)
		}
		path := filepath.Join(dir, "app.png")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return ShotResult{}, err
		}
		res.PNG = path
	}
	if rep.Text != "" {
		path := filepath.Join(dir, "app-text.txt")
		if err := os.WriteFile(path, []byte(rep.Text), 0o644); err != nil {
			return ShotResult{}, err
		}
		res.TextPath = path
	}
	res.OK = res.PNG != "" || res.TextPath != ""
	switch {
	case !res.OK:
		res.Error = "the app returned nothing"
		if rep.Error != "" {
			res.Error = "the app could not capture: " + rep.Error
		}
	case rep.Error != "":
		res.Warning = rep.Error
	}
	return res, nil
}
