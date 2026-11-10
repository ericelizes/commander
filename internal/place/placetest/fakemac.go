// Package placetest is a fake Mac: it pairs, holds the channel and calls the
// run endpoints exactly as the app and the Mac's cmdr do, so tests and evals
// exercise cmdrd's /api/places with no Mac.
package placetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/example-user/commander/internal/place"
)

// Mac is one connected fake Mac.
type Mac struct {
	Base   string // http://host:port
	Device place.Device
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

// Pair asks cmdrd for a pairing code for dev (the app's "Pair this Mac").
func Pair(base, apiToken string, dev place.Device) (string, error) {
	b, _ := json.Marshal(map[string]string{"public_key": dev.Pub})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/places/pair", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Code == "" {
		return "", fmt.Errorf("pair: %d %s", resp.StatusCode, out.Error)
	}
	return out.Code, nil
}

// Connect opens the channel, answers the challenge and waits for welcome
// (or the refusal, returned as the error).
func Connect(base, apiToken string, dev place.Device, cmdrVersion string) (*Mac, error) {
	ctx, cancel := context.WithCancel(context.Background())
	url := "ws" + strings.TrimPrefix(base, "http") + "/api/places/connect"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + apiToken}}})
	if err != nil {
		cancel()
		return nil, err
	}
	m := &Mac{Base: base, Device: dev, c: c, ctx: ctx, cancel: cancel}
	ch, err := m.Next(5 * time.Second)
	if err != nil || ch.Type != "challenge" {
		m.Close()
		return nil, fmt.Errorf("no challenge: %v %+v", err, ch)
	}
	if err := wsjson.Write(ctx, c, dev.Hello(ch.Nonce, cmdrVersion)); err != nil {
		m.Close()
		return nil, err
	}
	w, err := m.Next(5 * time.Second)
	if err != nil || w.Type != "welcome" {
		m.Close()
		if err == nil {
			err = fmt.Errorf("%s", w.Error)
		}
		return nil, err
	}
	return m, nil
}

// Close ends the channel.
func (m *Mac) Close() {
	m.c.CloseNow()
	m.cancel()
}

// Next reads the next frame from cmdrd.
func (m *Mac) Next(timeout time.Duration) (place.Message, error) {
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	var msg place.Message
	err := wsjson.Read(ctx, m.c, &msg)
	return msg, err
}

// Ask sends a frame and returns cmdrd's answer.
func (m *Mac) Ask(msg place.Message) (place.Message, error) {
	if err := wsjson.Write(m.ctx, m.c, msg); err != nil {
		return place.Message{}, err
	}
	return m.Next(5 * time.Second)
}

// Call is one run endpoint call with the run token: method, path under
// /api/places/runs/<id>/, a JSON body (nil = none). It returns the status and
// the decoded answer.
func Call(base string, runID int64, token, method, path string, body any) (int, map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%s/api/places/runs/%d/%s", base, runID, path), rd)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("X-Commander-Run-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}
