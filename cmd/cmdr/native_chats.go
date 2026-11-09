package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/chat"
)

// cmdrdChats starts and messages native chats through cmdrd's API. Any error
// (cmdrd down, session held by a terminal) makes the caller fall back to a
// terminal window.
type cmdrdChats struct{ home string }

func (c cmdrdChats) Fresh(string) bool { return true }

func (c cmdrdChats) Start(ctx context.Context, role string) error {
	return c.post(ctx, role, "start", nil)
}

func (c cmdrdChats) Send(ctx context.Context, role, text string) error {
	return c.post(ctx, role, "send", map[string]any{"text": text})
}

func (c cmdrdChats) post(ctx context.Context, role, action string, body map[string]any) error {
	_, err := c.postStatus(ctx, role, action, body)
	return err
}

// postStatus posts action and returns the role's status from cmdrd's answer
// (zero when the answer carries none).
func (c cmdrdChats) postStatus(ctx context.Context, role, action string, body map[string]any) (chat.Status, error) {
	return c.call(ctx, http.MethodPost, role, action, body)
}

// status reads what cmdrd knows of role's chat. cmdrd's status always names its role,
// so an answer that does not (a body that does not decode, {}) is no answer.
func (c cmdrdChats) status(ctx context.Context, role string) (chat.Status, error) {
	st, err := c.call(ctx, http.MethodGet, role, "", nil)
	if err == nil && st.Role != role {
		return chat.Status{}, fmt.Errorf("cmdrd's answer for %s names no such chat", role)
	}
	return st, err
}

func (c cmdrdChats) call(ctx context.Context, method, role, action string, body map[string]any) (chat.Status, error) {
	var answer struct {
		Status chat.Status `json:"status"`
	}
	raw, err := os.ReadFile(filepath.Join(c.home, "api-url"))
	if err != nil {
		return answer.Status, fmt.Errorf("cmdrd has no api-url: %w", err)
	}
	token, _ := os.ReadFile(filepath.Join(c.home, "api-token"))
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}
	u := strings.TrimRight(strings.TrimSpace(string(raw)), "/") + "/api/chat/" + url.PathEscape(role)
	if action != "" {
		u += "/" + action
	}
	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return answer.Status, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return answer.Status, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return answer.Status, fmt.Errorf("cmdrd %s %s: %s", action, role, strings.TrimSpace(string(msg)))
	}
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	return answer.Status, nil
}
