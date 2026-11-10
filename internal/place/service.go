package place

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
)

// Bead is what a run fetch needs from a bead.
type Bead struct {
	Title       string
	Description string
	Status      string   // as the beads CLI prints it ("closed", "● P2 · CLOSED" ...)
	Labels      []string // for the run-on label and a prod gate's approval
}

// Outcome is how a place run ended, for the settle.
type Outcome string

const (
	Finished Outcome = "finished" // exited with a report: Manager to close
	Lost     Outcome = "died"     // no heartbeat, or exited with no report
	Stopped  Outcome = "stopped"  // stopped on request or by a usage limit
	Declined Outcome = "declined" // the user said no, or nobody answered
)

// Settlement is one ended run handed to the settle.
type Settlement struct {
	Run       store.AgentRun
	PlaceRun  store.PlaceRun
	Outcome   Outcome
	Why       string
	ProjectID int64
}

// Deps are what the service reads and writes outside the store.
type Deps struct {
	Store *store.Store
	// ReadBead reads a bead's title and description (fresh).
	ReadBead func(ctx context.Context, projectDir, id string) (Bead, error)
	// Comment writes text on bead through the write door as role (door checks apply).
	Comment func(ctx context.Context, projectDir, bead, role, text string) error
	// Mail sends from -> to.
	Mail func(ctx context.Context, from, to, subject, body string) error
	// Inbox reads role's unread mail and marks what it returns read.
	Inbox func(ctx context.Context, role string) ([]agentmail.Mail, error)
	// RecordUsage keeps a usage event the Mac forwarded, under account place.
	RecordUsage func(place string, event json.RawMessage)
	// Settle writes the bead side of an ended run (owner, wait or operator-died
	// label, Manager mail). Never a respawn: a new run needs the user's dialog.
	Settle func(ctx context.Context, s Settlement) error
	// Revision is cmdrd's build revision; a Mac cmdr of any other is refused.
	Revision string
	Now      func() time.Time
}

// Service is the server side of place runs. Methods are safe for concurrent use.
type Service struct {
	d   Deps
	hub hub
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, hub: hub{conns: map[string]*Conn{}}}
}

// Errors the HTTP layer maps to statuses.
var (
	ErrUnauthorized = errors.New("not this run's token, or the run has ended")
	ErrNotFound     = errors.New("no such run on this place")
	ErrConflict     = errors.New("the run is not in a state that allows this")
)

// --- the channel (one per connected place) -------------------------------

// Message is one frame on /api/places/connect, either way.
type Message struct {
	Type string `json:"type"`
	// server -> Mac
	Nonce    string   `json:"nonce,omitempty"`     // challenge
	RunID    int64    `json:"run_id,omitempty"`    // offer, stop, mail, run, accepted, error
	Project  string   `json:"project,omitempty"`   // offer
	TaskID   string   `json:"task_id,omitempty"`   // offer
	StepID   string   `json:"step_id,omitempty"`   // offer
	ProdGate string   `json:"prod_gate,omitempty"` // offer
	BriefSHA string   `json:"brief_sha,omitempty"` // offer, accept
	ScopeSHA string   `json:"scope_sha,omitempty"` // accept: sha256 of the prod-gate scope the dialog showed
	By       string   `json:"by,omitempty"`        // stop
	Run      *RunInfo `json:"run,omitempty"`       // run (the fetch answer)
	Token    string   `json:"token,omitempty"`     // accepted
	Error    string   `json:"error,omitempty"`
	// Mac -> server
	Place       string `json:"place,omitempty"`        // welcome: the name the user paired it as
	PublicKey   string `json:"public_key,omitempty"`   // hello
	Signature   string `json:"signature,omitempty"`    // hello
	AppVersion  string `json:"app_version,omitempty"`  // hello
	CmdrVersion string `json:"cmdr_version,omitempty"` // hello
	Reason      string `json:"reason,omitempty"`       // decline
}

// RunInfo is the run as the Mac's dialog and launch template see it. It holds
// text and ids, never argv: the Mac builds the command line itself.
type RunInfo struct {
	RunID       int64  `json:"run_id"`
	Place       string `json:"place"`
	Project     string `json:"project"`
	TaskID      string `json:"task_id"`
	TaskTitle   string `json:"task_title"`
	StepID      string `json:"step_id"`
	StepTitle   string `json:"step_title"`
	Brief       string `json:"brief"`
	BriefSHA    string `json:"brief_sha"`
	ProdGate    string `json:"prod_gate,omitempty"`
	ProdScope   string `json:"prod_scope,omitempty"`
	Role        string `json:"role"`
	ManagerRole string `json:"manager_role"`
}

// Conn is one connected place. Send carries frames to the socket writer.
// A connection is bound to the pairing it said hello under (device key and
// pairing time): once that pairing is revoked or replaced, even under the
// same name, the connection takes no frame and gets no offer, and Gone closes
// so the socket ends.
type Conn struct {
	Place    string
	Send     chan Message
	key      string
	pairedAt int64
	gone     chan struct{}
	once     sync.Once
}

// Gone is closed once the connection's pairing no longer stands.
func (c *Conn) Gone() <-chan struct{} { return c.gone }

func (c *Conn) retire() { c.once.Do(func() { close(c.gone) }) }

// NewConn is a connection bound to a pairing (tests and Hello).
func NewConn(p store.Place) *Conn {
	return &Conn{Place: p.Name, Send: make(chan Message, 32), key: p.PublicKey, pairedAt: p.PairedAt, gone: make(chan struct{})}
}

type hub struct {
	mu    sync.Mutex
	conns map[string]*Conn
}

func (h *hub) put(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c.Place] = c // the newest connection of a place wins
}

func (h *hub) drop(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.Place] == c {
		delete(h.conns, c.Place)
	}
}

func (h *hub) get(place string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[place]
}

func (h *hub) all() []*Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		out = append(out, c)
	}
	return out
}

func (h *hub) send(place string, m Message) bool {
	return h.sendTo(h.get(place), m)
}

func (h *hub) sendTo(c *Conn, m Message) bool {
	if c == nil {
		return false
	}
	select {
	case c.Send <- m:
		return true
	default:
		return false
	}
}

// Connected reports whether a place holds a channel now.
func (s *Service) Connected(place string) bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.hub.conns[place] != nil
}

// Hello checks a place's hello against the nonce it was sent: a paired, not
// revoked place, a signature by its device key and the same cmdr revision. On
// success the connection is registered and every offer waiting for the place
// is sent again.
func (s *Service) Hello(ctx context.Context, nonce string, m Message) (*Conn, error) {
	if m.Type != "hello" || m.PublicKey == "" {
		return nil, errors.New("expected hello with the device's public key")
	}
	if !VerifyChallenge(m.PublicKey, nonce, m.Signature) {
		return nil, errors.New("device signature does not match its key")
	}
	p, err := s.d.Store.GetPlaceByKey(ctx, m.PublicKey)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Paired() {
		return nil, errors.New("this Mac is not paired (or was revoked): pair it with cmdr place pair")
	}
	m.Place = p.Name
	if s.d.Revision == "" || m.CmdrVersion != s.d.Revision {
		return nil, fmt.Errorf("cmdr on this Mac is %q, cmdrd runs %q: update cmdr on this Mac", m.CmdrVersion, s.d.Revision)
	}
	c := NewConn(*p)
	s.hub.put(c)
	runs, err := s.d.Store.ListPlaceRuns(ctx, m.Place, true)
	if err == nil {
		for _, r := range runs {
			if r.State == store.PlaceOffered {
				if off, err := s.offerMessage(ctx, r); err == nil {
					s.hub.sendTo(c, off)
				}
			}
		}
	}
	return c, nil
}

// Drop unregisters a connection that closed.
func (s *Service) Drop(c *Conn) { s.hub.drop(c) }

// ErrPairingGone answers a frame from a connection whose pairing was revoked
// or replaced since its hello.
var ErrPairingGone = errors.New("this Mac's pairing was revoked or replaced: pair it again with cmdr place pair")

// current reports whether c's pairing still stands (same name, key and
// pairing time, not revoked). A connection whose pairing is gone is retired:
// unregistered and its Gone closed. A failed read keeps the connection but
// refuses the frame.
func (s *Service) current(ctx context.Context, c *Conn) error {
	p, err := s.d.Store.GetPlace(ctx, c.Place)
	if err != nil {
		return err
	}
	if p == nil || !p.Paired() || p.PublicKey != c.key || p.PairedAt != c.pairedAt {
		s.hub.drop(c)
		c.retire()
		return ErrPairingGone
	}
	return nil
}

// sendCurrent sends m to place's connection only while its pairing stands.
func (s *Service) sendCurrent(ctx context.Context, place string, m Message) bool {
	c := s.hub.get(place)
	if c == nil || s.current(ctx, c) != nil {
		return false
	}
	return s.hub.sendTo(c, m)
}

// Handle answers one frame from an authenticated place. A nil reply means none.
func (s *Service) Handle(ctx context.Context, c *Conn, m Message) *Message {
	reply := func(err error) *Message {
		if err != nil {
			return &Message{Type: "error", RunID: m.RunID, Error: err.Error()}
		}
		return nil
	}
	if m.Type == "ping" {
		return &Message{Type: "pong"}
	}
	if err := s.current(ctx, c); err != nil {
		return reply(err)
	}
	place := c.Place
	switch m.Type {
	case "fetch":
		info, err := s.Fetch(ctx, place, m.RunID)
		if err != nil {
			return reply(err)
		}
		return &Message{Type: "run", RunID: m.RunID, Run: info}
	case "accept":
		tok, err := s.Accept(ctx, place, m.RunID, m.BriefSHA, m.ScopeSHA)
		if err != nil {
			return reply(err)
		}
		return &Message{Type: "accepted", RunID: m.RunID, Token: tok}
	case "decline":
		return reply(s.Decline(ctx, place, m.RunID, m.Reason))
	}
	return reply(fmt.Errorf("unknown frame %q", m.Type))
}

// --- offers --------------------------------------------------------------

func (s *Service) runOf(ctx context.Context, runID int64) (*store.AgentRun, *store.PlaceRun, *store.Project, error) {
	pr, err := s.d.Store.GetPlaceRun(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	if pr == nil {
		return nil, nil, nil, ErrNotFound
	}
	run, err := s.d.Store.GetAgentRun(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	if run == nil {
		return nil, nil, nil, ErrNotFound
	}
	proj, err := s.d.Store.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	if proj == nil {
		return nil, nil, nil, fmt.Errorf("run %d: project %d is gone", runID, run.ProjectID)
	}
	return run, pr, proj, nil
}

func (s *Service) offerMessage(ctx context.Context, pr store.PlaceRun) (Message, error) {
	run, _, proj, err := s.runOf(ctx, pr.RunID)
	if err != nil {
		return Message{}, err
	}
	return Message{Type: "offer", RunID: pr.RunID, Project: proj.Name, TaskID: run.TaskBeadID,
		StepID: pr.StepID, ProdGate: pr.ProdGate, BriefSHA: pr.BriefSHA}, nil
}

// Offer sends a recorded offer to its place when it is connected; a place that
// connects later gets it at its hello. It reports whether it went out now.
func (s *Service) Offer(ctx context.Context, runID int64) (bool, error) {
	pr, err := s.d.Store.GetPlaceRun(ctx, runID)
	if err != nil || pr == nil {
		return false, errors.Join(err, ErrNotFound)
	}
	m, err := s.offerMessage(ctx, *pr)
	if err != nil {
		return false, err
	}
	return s.sendCurrent(ctx, pr.Place, m), nil
}

// Fetch is the run as the dialog shows it, for an offered run of this place.
func (s *Service) Fetch(ctx context.Context, place string, runID int64) (*RunInfo, error) {
	run, pr, proj, err := s.runOf(ctx, runID)
	if err != nil {
		return nil, err
	}
	if pr.Place != place {
		return nil, ErrNotFound
	}
	if pr.State != store.PlaceOffered {
		return nil, fmt.Errorf("%w: run %d is %s (a new run needs a new offer)", ErrConflict, runID, pr.State)
	}
	task, err := s.d.ReadBead(ctx, proj.Path, run.TaskBeadID)
	if err != nil {
		return nil, err
	}
	step, err := s.d.ReadBead(ctx, proj.Path, pr.StepID)
	if err != nil {
		return nil, err
	}
	info := &RunInfo{RunID: runID, Place: place, Project: proj.Name, TaskID: run.TaskBeadID, TaskTitle: task.Title,
		StepID: pr.StepID, StepTitle: step.Title, Brief: task.Description, BriefSHA: BriefSHA(task.Description),
		ProdGate: pr.ProdGate, Role: run.AgentName, ManagerRole: pr.ManagerRole}
	if pr.ProdGate != "" {
		g, err := s.d.ReadBead(ctx, proj.Path, pr.ProdGate)
		if err != nil {
			return nil, err
		}
		info.ProdScope = g.Description
	}
	return info, nil
}

// Accept is the user's yes in the Mac's dialog: the brief must still be the one
// offered (and shown), the step must still name this place, and a prod gate
// must still be closed, approved by the user and cover the scope the dialog
// showed (scopeSHA); the run goes running and its token is minted, once.
func (s *Service) Accept(ctx context.Context, place string, runID int64, briefSHA, scopeSHA string) (string, error) {
	run, pr, proj, err := s.runOf(ctx, runID)
	if err != nil {
		return "", err
	}
	if pr.Place != place {
		return "", ErrNotFound
	}
	if pr.State != store.PlaceOffered {
		return "", fmt.Errorf("%w: run %d is %s", ErrConflict, runID, pr.State)
	}
	task, err := s.d.ReadBead(ctx, proj.Path, run.TaskBeadID)
	if err != nil {
		return "", err
	}
	if briefSHA != pr.BriefSHA || BriefSHA(task.Description) != pr.BriefSHA {
		s.end(ctx, runID, store.PlaceDeclined, "brief changed after the offer")
		return "", fmt.Errorf("%w: the brief changed after the offer; the Manager must offer a new run", ErrConflict)
	}
	if why, err := s.stillAllowed(ctx, proj.Path, place, pr, scopeSHA); err != nil {
		return "", err
	} else if why != "" {
		s.end(ctx, runID, store.PlaceDeclined, why)
		return "", fmt.Errorf("%w: %s; the Manager must offer a new run", ErrConflict, why)
	}
	tok, hash := NewToken()
	ok, err := s.d.Store.AcceptPlaceRun(ctx, runID, hash, s.d.Now().Unix())
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: run %d was answered already", ErrConflict, runID)
	}
	return tok, nil
}

// stillAllowed re-reads what the offer was made against: the step's run-on
// label and, for a prod-gate run, the gate (closed, a gate bead, approved by
// the user, its scope unchanged since the dialog showed it). why is the
// refusal; err a read that failed (the run stays offered).
func (s *Service) stillAllowed(ctx context.Context, dir, place string, pr *store.PlaceRun, scopeSHA string) (why string, err error) {
	step, err := s.d.ReadBead(ctx, dir, pr.StepID)
	if err != nil {
		return "", err
	}
	if named, err := FromLabels(step.Labels); err != nil || named != place {
		return "step " + pr.StepID + " no longer runs on " + place, nil
	}
	if pr.ProdGate == "" {
		return "", nil
	}
	g, err := s.d.ReadBead(ctx, dir, pr.ProdGate)
	if err != nil {
		return "", err
	}
	switch {
	case !slices.Contains(g.Labels, "kind:gate") && !slices.Contains(g.Labels, "gate"):
		return "prod gate " + pr.ProdGate + " is no longer a gate bead", nil
	case !strings.Contains(strings.ToLower(g.Status), "closed"):
		return "prod gate " + pr.ProdGate + " is no longer closed (approved)", nil
	case !approvedByUser(g.Labels):
		return "prod gate " + pr.ProdGate + " is no longer approved by the user", nil
	case strings.TrimSpace(g.Description) == "":
		return "prod gate " + pr.ProdGate + " names no scope", nil
	case scopeSHA != BriefSHA(g.Description):
		return "prod gate " + pr.ProdGate + "'s scope changed after the dialog showed it", nil
	}
	return "", nil
}

func approvedByUser(labels []string) bool {
	for _, l := range labels {
		if who, ok := strings.CutPrefix(l, "approver:"); ok && ownercompat.Normalize(who) == "user" {
			return true
		}
	}
	return false
}

// Decline is the user's no (or the dialog's timeout).
func (s *Service) Decline(ctx context.Context, place string, runID int64, reason string) error {
	_, pr, _, err := s.runOf(ctx, runID)
	if err != nil {
		return err
	}
	if pr.Place != place {
		return ErrNotFound
	}
	if reason == "" {
		reason = "declined on the Mac"
	}
	if !s.end(ctx, runID, store.PlaceDeclined, clip(reason, 200)) {
		return fmt.Errorf("%w: run %d is %s", ErrConflict, runID, pr.State)
	}
	return nil
}

// Stop ends a run from the server side: the token dies at once and the Mac is
// told to end the process group. It only takes power away.
func (s *Service) Stop(ctx context.Context, runID int64, by, reason string) (bool, error) {
	pr, err := s.d.Store.GetPlaceRun(ctx, runID)
	if err != nil || pr == nil {
		return false, errors.Join(err, ErrNotFound)
	}
	if reason == "" {
		reason = "stopped by " + by
	}
	ended := s.end(ctx, runID, store.PlaceStopped, reason)
	s.hub.send(pr.Place, Message{Type: "stop", RunID: runID, By: by})
	return ended, nil
}

// SignalMail tells the place a run's role has mail (the hook reads it at the
// next tool call). A no-op for a role with no running place run.
func (s *Service) SignalMail(ctx context.Context, role string) {
	runs, err := s.d.Store.ListPlaceRuns(ctx, "", true)
	if err != nil {
		return
	}
	for _, pr := range runs {
		if run, _ := s.d.Store.GetAgentRun(ctx, pr.RunID); run != nil && sameRole(run.AgentName, role) {
			s.sendCurrent(ctx, pr.Place, Message{Type: "mail", RunID: pr.RunID})
		}
	}
}

func sameRole(a, b string) bool {
	return strings.ReplaceAll(a, ".", "-") == strings.ReplaceAll(b, ".", "-")
}

func (s *Service) end(ctx context.Context, runID int64, state, reason string) bool {
	ok, err := s.d.Store.EndPlaceRun(ctx, runID, state, reason, s.d.Now().Unix())
	return err == nil && ok
}

// --- run-token endpoints --------------------------------------------------

// Run is an authorized run: what its token may reach.
type Run struct {
	AgentRun store.AgentRun
	PlaceRun store.PlaceRun
	Project  store.Project
}

// Authorize returns the run a token belongs to: the run must be running, the
// token's hash must match and the run must be younger than TokenTTL.
func (s *Service) Authorize(ctx context.Context, runID int64, token string) (*Run, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	run, pr, proj, err := s.runOf(ctx, runID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if pr.State != store.PlaceRunning || pr.TokenHash == "" ||
		subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(pr.TokenHash)) != 1 {
		return nil, ErrUnauthorized
	}
	if s.d.Now().Sub(time.Unix(pr.AcceptedAt, 0)) > TokenTTL {
		s.end(ctx, runID, store.PlaceStopped, "run token expired (12 h)")
		return nil, ErrUnauthorized
	}
	return &Run{AgentRun: *run, PlaceRun: *pr, Project: *proj}, nil
}

// MaxComment bounds one comment from a place run.
const MaxComment = 64 << 10

// Comment writes on the run's own task bead as its Operator role. A client id
// already written answers ok without writing again. report marks the run
// reported (the settle's test for a finished run).
func (s *Service) Comment(ctx context.Context, r *Run, clientID, text string, report bool) (dup bool, err error) {
	if strings.TrimSpace(text) == "" {
		return false, errors.New("empty comment")
	}
	if len(text) > MaxComment {
		return false, fmt.Errorf("comment over %d bytes", MaxComment)
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 64 {
		return false, errors.New("client_id required (1-64 chars), so a retry is written once")
	}
	fresh, err := s.d.Store.RecordPlaceComment(ctx, r.AgentRun.ID, clientID, s.d.Now().Unix())
	if err != nil {
		return false, err
	}
	if !fresh {
		return true, nil
	}
	if err := s.d.Comment(ctx, r.Project.Path, r.AgentRun.TaskBeadID, r.AgentRun.AgentName, text); err != nil {
		_ = s.d.Store.ForgetPlaceComment(ctx, r.AgentRun.ID, clientID)
		return false, err
	}
	if report {
		if err := s.d.Store.MarkPlaceRun(ctx, r.AgentRun.ID, "reported", true); err != nil {
			return false, err
		}
	}
	return false, nil
}

// MailManager sends from the run's role to its Manager: the recipient is the
// run's, never the caller's choice.
func (s *Service) MailManager(ctx context.Context, r *Run, subject, body string) error {
	if strings.TrimSpace(subject) == "" {
		return errors.New("empty subject")
	}
	return s.d.Mail(ctx, r.AgentRun.AgentName, r.PlaceRun.ManagerRole, clip(subject, 200), clip(body, MaxComment))
}

// ReadMail is the run role's unread mail.
func (s *Service) ReadMail(ctx context.Context, r *Run) ([]agentmail.Mail, error) {
	return s.d.Inbox(ctx, r.AgentRun.AgentName)
}

// Beat is one heartbeat from the Mac.
type Beat struct {
	PID        int               `json:"pid"`
	Alive      bool              `json:"alive"`
	LastTool   int64             `json:"last_tool,omitempty"`
	Tokens     int64             `json:"tokens,omitempty"`
	Refusals   []string          `json:"refusals,omitempty"`
	Usage      []json.RawMessage `json:"usage,omitempty"`
	UsageLimit bool              `json:"usage_limit,omitempty"`
}

// Heartbeat records a beat, its refusals (as one bead comment), usage events,
// and a usage stop (the run ends stopped/usage_limit and the Mac is told to stop).
func (s *Service) Heartbeat(ctx context.Context, r *Run, b Beat) error {
	if _, err := s.d.Store.BeatPlaceRun(ctx, r.AgentRun.ID, s.d.Now().Unix()); err != nil {
		return err
	}
	if b.Tokens > 0 {
		_ = s.d.Store.UpdateUsage(ctx, r.AgentRun.ID, b.Tokens, r.AgentRun.UsdUsed)
	}
	if s.d.RecordUsage != nil {
		for _, ev := range b.Usage {
			s.d.RecordUsage(r.PlaceRun.Place, ev)
		}
	}
	if len(b.Refusals) > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "cmdrd: the guard on %s refused %d command(s):\n", r.PlaceRun.Place, len(b.Refusals))
		for i, x := range b.Refusals {
			if i == 20 {
				fmt.Fprintf(&sb, "- ... %d more\n", len(b.Refusals)-20)
				break
			}
			fmt.Fprintf(&sb, "- %s\n", clip(oneLine(x), 300))
		}
		_ = s.d.Comment(ctx, r.Project.Path, r.AgentRun.TaskBeadID, "", sb.String())
	}
	if b.UsageLimit {
		s.end(ctx, r.AgentRun.ID, store.PlaceStopped, "usage_limit")
		s.hub.send(r.PlaceRun.Place, Message{Type: "stop", RunID: r.AgentRun.ID, By: "usage limit"})
	}
	return nil
}

// Exit is the Mac's word that the run's process ended.
func (s *Service) Exit(ctx context.Context, r *Run, status, reason string) error {
	state := store.PlaceExited
	if status == "stopped" {
		state = store.PlaceStopped
	}
	if reason == "" {
		reason = status
	}
	s.end(ctx, r.AgentRun.ID, state, clip(reason, 200))
	return nil
}

// --- the sweep ------------------------------------------------------------

// Sweep reads every place run once: an unreachable run's Manager is mailed
// once, a dead one (or an offer nobody answered) ends, and every ended run not
// yet settled is handed to Settle. Called from cmdrd's minute loop.
func (s *Service) Sweep(ctx context.Context) error {
	// A connection whose pairing was revoked (cmdr place revoke runs in
	// another process) or replaced ends here at the latest.
	for _, c := range s.hub.all() {
		_ = s.current(ctx, c)
	}
	runs, err := s.d.Store.ListPlaceRuns(ctx, "", false)
	if err != nil {
		return err
	}
	now := s.d.Now()
	var firstErr error
	for _, pr := range runs {
		if pr.Settled {
			continue
		}
		switch LivenessOf(pr, now) {
		case Unreachable:
			if !pr.UnreachableMailed {
				run, _ := s.d.Store.GetAgentRun(ctx, pr.RunID)
				if run != nil {
					subj := fmt.Sprintf("[place] %s on %s unreachable", run.TaskBeadID, pr.Place)
					body := fmt.Sprintf("No heartbeat from %s for %d min (Mac asleep, offline or the app quit). The run is not lost yet: it is settled as died after %d min without one. Nothing to do unless the user says the Mac is gone.",
						pr.Place, int(now.Sub(time.Unix(pr.LastBeat, 0)).Minutes()), int(DiedAfter.Minutes()))
					if err := s.d.Mail(ctx, "cmdrd", pr.ManagerRole, subj, body); err == nil {
						_ = s.d.Store.MarkPlaceRun(ctx, pr.RunID, "unreachable_mailed", true)
					}
				}
			}
			continue
		case Died:
			why := fmt.Sprintf("no heartbeat for %d min", int(DiedAfter.Minutes()))
			if pr.State == store.PlaceOffered {
				s.end(ctx, pr.RunID, store.PlaceDeclined, "nobody answered the offer on "+pr.Place)
			} else {
				s.end(ctx, pr.RunID, store.PlaceDied, why)
				// Given up as died, the run must not go on if the Mac is in
				// fact there: send the stop (its next heartbeat is refused
				// too, and the Mac stops on that).
				s.hub.send(pr.Place, Message{Type: "stop", RunID: pr.RunID, By: "cmdrd (" + why + ")"})
			}
			fresh, err := s.d.Store.GetPlaceRun(ctx, pr.RunID)
			if err != nil || fresh == nil {
				continue
			}
			pr = *fresh
		}
		if store.PlaceRunActive(pr.State) {
			continue
		}
		if err := s.settle(ctx, pr); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OutcomeOf reads an ended run.
func OutcomeOf(pr store.PlaceRun) (Outcome, string) {
	switch pr.State {
	case store.PlaceExited:
		if pr.Reported {
			return Finished, "exited with a report"
		}
		return Lost, "exited with no report"
	case store.PlaceStopped:
		return Stopped, pr.Reason
	case store.PlaceDeclined:
		return Declined, pr.Reason
	}
	return Lost, pr.Reason
}

func (s *Service) settle(ctx context.Context, pr store.PlaceRun) error {
	run, err := s.d.Store.GetAgentRun(ctx, pr.RunID)
	if err != nil || run == nil {
		return errors.Join(err, ErrNotFound)
	}
	out, why := OutcomeOf(pr)
	if s.d.Settle != nil {
		if err := s.d.Settle(ctx, Settlement{Run: *run, PlaceRun: pr, Outcome: out, Why: why, ProjectID: run.ProjectID}); err != nil {
			return fmt.Errorf("settle place run %d: %w", pr.RunID, err)
		}
	}
	return s.d.Store.MarkPlaceRun(ctx, pr.RunID, "settled", true)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Paired reports whether name is a paired, not revoked place.
func (s *Service) Paired(ctx context.Context, name string) (bool, error) {
	if !ValidName(name) {
		return false, nil
	}
	p, err := s.d.Store.GetPlace(ctx, name)
	if err != nil {
		return false, err
	}
	return p != nil && p.Paired(), nil
}

// HookMailLimit bounds a delivered hook batch on both sides of the API.
const HookMailLimit = 10
