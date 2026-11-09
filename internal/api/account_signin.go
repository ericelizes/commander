package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

// Signing in from the app works wherever cmdrd runs, including a machine with no
// browser or terminal: cmdrd runs the provider's own login command and hands
// its link to the app. Claude then wants the code its page shows, which passes
// straight to the command's stdin and is neither logged nor kept; Codex finishes
// by itself once the user enters its device code on the provider's page.
const (
	signinLife     = 15 * time.Minute // Codex's device code lasts this long
	signinLinkWait = 15 * time.Second
	signinCodeWait = 60 * time.Second
)

var (
	signinURL  = regexp.MustCompile(`https://[^\s\x1b\x07]+`)
	deviceCode = regexp.MustCompile(`^\s*([A-Z0-9]{4}-[A-Z0-9]{4,})\s*$`)
)

type signin struct {
	url, userCode string
	stdin         io.WriteCloser
	cancel        context.CancelFunc
	ready         chan struct{} // closed once the link (and any device code) is known
	done          chan struct{} // closed once the command exits
	err           error         // read only after done
}

type signins struct {
	mu     sync.Mutex
	active map[string]*signin
}

func (l *signins) get(name string) *signin {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active[name]
}

// replace ends any sign-in already open for name, then keeps next (nil: none).
func (l *signins) replace(name string, next *signin) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if old := l.active[name]; old != nil {
		old.cancel()
	}
	if l.active == nil {
		l.active = map[string]*signin{}
	}
	if next == nil {
		delete(l.active, name)
		return
	}
	l.active[name] = next
}

// accountCommand runs a provider's CLI for one account, with the environment of
// that account rather than of whichever agent or shell started cmdrd.
func accountCommand(ctx context.Context, home, name string, args ...string) *exec.Cmd {
	bin := "claude"
	var env []string
	if name == chat.RuntimeCodex {
		bin = "codex"
	} else {
		env = claudeacct.Env(home, name)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key != "CLAUDECODE" && key != "CLAUDE_CONFIG_DIR" && !strings.HasPrefix(key, "COMMANDER_ROLE") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func startSignin(home, name string) (*signin, error) {
	args := []string{"auth", "login", "--claudeai"}
	if name == chat.RuntimeCodex {
		args = []string{"login", "--device-auth"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), signinLife)
	cmd := accountCommand(ctx, home, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	si := &signin{stdin: stdin, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer cancel()
		defer close(si.done)
		readSigninOutput(out, si, name == chat.RuntimeCodex)
		si.err = cmd.Wait()
	}()
	return si, nil
}

func readSigninOutput(out io.Reader, si *signin, wantsDeviceCode bool) {
	announced := false
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		line := lines.Text()
		if si.url == "" {
			si.url = signinURL.FindString(line)
		}
		if m := deviceCode.FindStringSubmatch(line); m != nil {
			si.userCode = m[1]
		}
		if !announced && si.url != "" && (!wantsDeviceCode || si.userCode != "") {
			announced = true
			close(si.ready)
		}
	}
	// Drain what the scanner gave up on, so the command never blocks writing.
	_, _ = io.Copy(io.Discard, out)
}

// signinAccount resolves {name} to the account it can sign in, or answers 404.
func (s *Server) signinAccount(w http.ResponseWriter, r *http.Request) (home, name string, ok bool) {
	e := s.chatEngine(w, r)
	if e == nil {
		return "", "", false
	}
	name = r.PathValue("name")
	if name != chat.RuntimeCodex && !claudeacct.Exists(e.Home(), name) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no account " + name})
		return "", "", false
	}
	return e.Home(), name, true
}

// serveSigninStart begins a sign-in and answers its link (and Codex's device code).
func (s *Server) serveSigninStart(w http.ResponseWriter, r *http.Request) {
	home, name, ok := s.signinAccount(w, r)
	if !ok {
		return
	}
	s.signins.replace(name, nil)
	si, err := startSignin(home, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not start the sign-in: " + err.Error()})
		return
	}
	select {
	case <-si.ready:
		s.signins.replace(name, si)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": name, "url": si.url, "user_code": si.userCode})
	case <-si.done:
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "the sign-in ended before giving a link"})
	case <-time.After(signinLinkWait):
		si.cancel()
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "the sign-in did not give a link"})
	}
}

// serveSigninCode finishes a Claude sign-in with the code its page showed.
func (s *Server) serveSigninCode(w http.ResponseWriter, r *http.Request) {
	home, name, ok := s.signinAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad JSON body"})
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || strings.ContainsAny(code, " \t\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "That doesn't look like a sign-in code. Copy it from the sign-in page and paste it again."})
		return
	}
	si := s.signins.get(name)
	if si == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This sign-in has ended. Start again."})
		return
	}
	defer s.signins.replace(name, nil)
	if _, err := io.WriteString(si.stdin, code+"\n"); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This sign-in has ended. Start again."})
		return
	}
	select {
	case <-si.done:
	case <-time.After(signinCodeWait):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "The sign-in didn't finish. Start again."})
		return
	}
	a := claudeacct.Get(home, name)
	if si.err != nil || !a.LoggedIn {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "That code didn't work. Start again and paste the newest code."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": name, "email": a.Email})
}

// serveSigninState says how an open sign-in stands; Codex's finishes by itself.
func (s *Server) serveSigninState(w http.ResponseWriter, r *http.Request) {
	_, name, ok := s.signinAccount(w, r)
	if !ok {
		return
	}
	state := "none"
	if si := s.signins.get(name); si != nil {
		select {
		case <-si.done:
			state = "done"
			if si.err != nil {
				state = "failed"
			}
			s.signins.replace(name, nil)
		default:
			state = "waiting"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}

// serveSigninCancel abandons an open sign-in.
func (s *Server) serveSigninCancel(w http.ResponseWriter, r *http.Request) {
	_, name, ok := s.signinAccount(w, r)
	if !ok {
		return
	}
	s.signins.replace(name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// serveSignout removes an account's login with the provider's own command. Its
// agents stop working until it is connected again; the app says so first.
func (s *Server) serveSignout(w http.ResponseWriter, r *http.Request) {
	home, name, ok := s.signinAccount(w, r)
	if !ok {
		return
	}
	s.signins.replace(name, nil)
	args := []string{"auth", "logout"}
	if name == chat.RuntimeCodex {
		args = []string{"logout"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := accountCommand(ctx, home, name, args...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not disconnect: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": name})
}
