package agentmail

// ServerClient talks to a running `am serve-http` over its JSON-RPC HTTP
// endpoint instead of the `am` CLI.
//
// Why this exists: the `am` CLI does direct-SQLite access, which the server
// refuses while it holds the mailbox DB ("mailbox mutation refused ... live
// Agent Mail process still holds the mailbox database"). On any real deployment
// the server is always-on (opencode's MCP needs it), so commander cannot use
// the CLI and must go through the server. See docs/agent-mail-routing.md.
//
// Naming caveat: the server rejects descriptive role names like
// "manager-commander" — it only accepts randomly generated adjective+noun names
// from its own vocabulary. So RegisterAgent does NOT take a name; it lets the
// server assign one and RETURNS it. Callers persist a role->name mapping.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultServerEndpoint = "http://127.0.0.1:8765/mcp/"

// ServerClient is an am client backed by the HTTP/MCP server.
type ServerClient struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewServerClient builds a client for an explicit endpoint + bearer token.
func NewServerClient(endpoint, token string) *ServerClient {
	if endpoint == "" {
		endpoint = defaultServerEndpoint
	}
	return &ServerClient{
		endpoint: endpoint,
		token:    token,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// NewServerClientFromEnv resolves the endpoint and bearer token from the
// environment, falling back to the am config file. Endpoint override:
// AM_HTTP_URL. Token: AM_HTTP_BEARER_TOKEN, else HTTP_BEARER_TOKEN from
// ~/.config/mcp-agent-mail/config.env.
func NewServerClientFromEnv() (*ServerClient, error) {
	endpoint := os.Getenv("AM_HTTP_URL")
	token := firstNonEmptyEnv("AM_HTTP_BEARER_TOKEN", "HTTP_BEARER_TOKEN")
	if token == "" {
		t, err := tokenFromConfigFile()
		if err != nil {
			return nil, err
		}
		token = t
	}
	if token == "" {
		return nil, fmt.Errorf("agentmail: no am bearer token (set AM_HTTP_BEARER_TOKEN or HTTP_BEARER_TOKEN in ~/.config/mcp-agent-mail/config.env)")
	}
	return NewServerClient(endpoint, token), nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func tokenFromConfigFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".config", "mcp-agent-mail", "config.env")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("agentmail: read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "HTTP_BEARER_TOKEN="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", nil
}

// --- JSON-RPC envelope (MCP streamable-HTTP, single JSON response) ---

type rpcRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Method  string    `json:"method"`
	Params  rpcParams `json:"params"`
}

type rpcParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type rpcResponse struct {
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call invokes one MCP tool and returns the tool's result payload (the JSON
// text inside content[0]). Tool-level failures (isError) and transport/RPC
// failures are both surfaced as errors.
func (c *ServerClient) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      "cmdr",
		Method:  "tools/call",
		Params:  rpcParams{Name: tool, Arguments: args},
	})
	if err != nil {
		return "", fmt.Errorf("agentmail: marshal %s request: %w", tool, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agentmail: build %s request: %w", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("agentmail: %s: %w", tool, err)
	}
	defer resp.Body.Close()

	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("agentmail: %s: decode response (HTTP %d): %w", tool, resp.StatusCode, err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("agentmail: %s: rpc error %d: %s", tool, decoded.Error.Code, decoded.Error.Message)
	}
	if decoded.Result == nil || len(decoded.Result.Content) == 0 {
		return "", fmt.Errorf("agentmail: %s: empty result (HTTP %d)", tool, resp.StatusCode)
	}
	text := decoded.Result.Content[0].Text
	if decoded.Result.IsError {
		return "", fmt.Errorf("agentmail: %s: %s", tool, strings.TrimSpace(text))
	}
	return text, nil
}

// EnsureProject idempotently creates/ensures the am project for an absolute
// working-directory path and returns its stable slug.
func (c *ServerClient) EnsureProject(ctx context.Context, humanKey string) (string, error) {
	if humanKey == "" {
		return "", fmt.Errorf("agentmail: EnsureProject requires an absolute path")
	}
	text, err := c.call(ctx, "ensure_project", map[string]any{"human_key": humanKey})
	if err != nil {
		return "", err
	}
	var out struct {
		Slug     string `json:"slug"`
		HumanKey string `json:"human_key"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("agentmail: parse ensure_project result: %w", err)
	}
	return out.Slug, nil
}

// RegisterAgent registers a new agent in projectKey and returns the
// server-assigned name. The server picks the name (random adjective+noun);
// callers must persist the returned name to address the agent later.
func (c *ServerClient) RegisterAgent(ctx context.Context, projectKey, program, model, taskDescription string) (string, error) {
	if projectKey == "" {
		return "", fmt.Errorf("agentmail: RegisterAgent requires projectKey")
	}
	if program == "" || model == "" {
		return "", fmt.Errorf("agentmail: RegisterAgent requires program and model")
	}
	args := map[string]any{
		"project_key": projectKey,
		"program":     program,
		"model":       model,
	}
	if taskDescription != "" {
		args["task_description"] = taskDescription
	}
	text, err := c.call(ctx, "register_agent", args)
	if err != nil {
		return "", err
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("agentmail: parse register_agent result: %w", err)
	}
	if out.Name == "" {
		return "", fmt.Errorf("agentmail: register_agent returned no name")
	}
	return out.Name, nil
}

// SendMessage sends mail from sender to one or more recipients (am names).
func (c *ServerClient) SendMessage(ctx context.Context, projectKey, sender string, to []string, subject, body string, opts SendOptions) error {
	if projectKey == "" || sender == "" || len(to) == 0 || subject == "" {
		return fmt.Errorf("agentmail: SendMessage requires projectKey, sender, to, subject")
	}
	args := map[string]any{
		"project_key": projectKey,
		"sender_name": sender,
		"to":          to,
		"subject":     subject,
		"body_md":     body,
	}
	if opts.Importance != "" {
		args["importance"] = opts.Importance
	}
	if opts.AckRequired {
		args["ack_required"] = true
	}
	if opts.NoAutoContact {
		args["auto_contact_if_blocked"] = false
	}
	if _, err := c.call(ctx, "send_message", args); err != nil {
		return err
	}
	return nil
}

// FetchInbox returns messages for agent at or after since.
func (c *ServerClient) FetchInbox(ctx context.Context, projectKey, agent string, since time.Time, opts InboxOptions) ([]Mail, error) {
	if projectKey == "" || agent == "" {
		return nil, fmt.Errorf("agentmail: FetchInbox requires projectKey and agent")
	}
	// mark_read defaults to true on the server, which turns every poll into a
	// write. Commander keeps its own read cursors (internal/mailstate).
	args := map[string]any{
		"project_key": projectKey,
		"agent_name":  agent,
		"mark_read":   false,
	}
	if !since.IsZero() {
		args["since_ts"] = since.UTC().Format(time.RFC3339)
	}
	if opts.Limit > 0 {
		args["limit"] = opts.Limit
	}
	if opts.IncludeBodies {
		args["include_bodies"] = true
	}
	text, err := c.call(ctx, "fetch_inbox", args)
	if err != nil {
		return nil, err
	}
	return parseServerMail(text)
}

// ListAgents returns every agent registered in projectKey.
func (c *ServerClient) ListAgents(ctx context.Context, projectKey string) ([]Agent, error) {
	if projectKey == "" {
		return nil, fmt.Errorf("agentmail: ListAgents requires projectKey")
	}
	text, err := c.call(ctx, "list_agents", map[string]any{"project_key": projectKey})
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name    string `json:"name"`
		Program string `json:"program"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &raw); err != nil {
		return nil, fmt.Errorf("agentmail: parse list_agents result: %w", err)
	}
	agents := make([]Agent, 0, len(raw))
	for _, a := range raw {
		agents = append(agents, Agent{Name: a.Name, Program: a.Program, Model: a.Model})
	}
	return agents, nil
}

// parseServerMail maps fetch_inbox's payload onto commander's Mail projection.
func parseServerMail(text string) ([]Mail, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var raw []struct {
		ID         any    `json:"id"`
		Subject    string `json:"subject"`
		From       string `json:"from"`
		BodyMD     string `json:"body_md"`
		Importance string `json:"importance"`
		CreatedTS  string `json:"created_ts"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("agentmail: parse fetch_inbox result: %w", err)
	}
	mail := make([]Mail, 0, len(raw))
	for _, m := range raw {
		sent, _ := time.Parse(time.RFC3339, m.CreatedTS)
		mail = append(mail, Mail{
			ID:         idToString(m.ID),
			From:       m.From,
			Subject:    m.Subject,
			Body:       m.BodyMD,
			Importance: m.Importance,
			SentAt:     sent,
		})
	}
	return mail, nil
}

func idToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}
