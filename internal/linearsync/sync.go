// Package linearsync reads the viewer's active sprint lazily, without storage.
package linearsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/linearapi"
)

const TTL = 30 * time.Second
const query = `query($team: String!) {
 teams(filter: {key: {eq: $team}}, first: 1) { nodes {
 activeCycle { name startsAt endsAt issues(first: 250, filter: {assignee: {isMe: {eq: true}}}) {
 nodes { identifier title url addedToCycleAt completedAt state { name type } } pageInfo { hasNextPage }
 } }
 } }
}`

type Cycle struct {
	Name  string `json:"name"`
	Start string `json:"start"`
	End   string `json:"end"`
}
type Issue struct {
	Team       string `json:"team"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	StatusName string `json:"status_name"`
	StatusType string `json:"status_type"`
	URL        string `json:"url"`
	AddedAt    string `json:"added_at,omitempty"`     // when it joined the cycle
	DoneAt     string `json:"completed_at,omitempty"` // when it was completed
	// HasWorkstream is resolved by the app against its current snapshot.
	HasWorkstream bool `json:"has_workstream"`
}
type TeamResponse struct {
	Team string `json:"team"`
	Response
}
type Response struct {
	Teams      []TeamResponse `json:"teams,omitempty"`
	State      string         `json:"state"`
	Cycle      *Cycle         `json:"cycle"`
	Issues     []Issue        `json:"issues"`
	FetchedAt  *time.Time     `json:"fetched_at"`
	StaleSince *time.Time     `json:"stale_since,omitempty"`
}
type Options struct {
	Home     string
	HTTP     linearapi.HTTPDo
	Endpoint string // tests only; defaults to Linear
	Now      func() time.Time
}
type Client struct {
	opts     Options
	mu       sync.Mutex
	cached   Response
	expires  time.Time
	flight   chan struct{}
	keyStamp keyStamp
	teams    map[string]Response
}

type keyStamp struct {
	present  bool
	modified time.Time
}

func stampKey(home string) keyStamp {
	info, err := os.Stat(KeyPath(home))
	if err != nil {
		return keyStamp{}
	}
	return keyStamp{true, info.ModTime()}
}

func New(opts Options) *Client {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Endpoint == "" {
		opts.Endpoint = linearapi.Endpoint
	}
	return &Client{opts: opts}
}
func clone(r Response) Response {
	if r.Teams != nil {
		teams := make([]TeamResponse, len(r.Teams))
		for i, t := range r.Teams {
			teams[i] = TeamResponse{Team: t.Team, Response: clone(t.Response)}
		}
		r.Teams = teams
	}
	r.Issues = append([]Issue{}, r.Issues...)
	if r.Cycle != nil {
		c := *r.Cycle
		r.Cycle = &c
	}
	if r.FetchedAt != nil {
		t := *r.FetchedAt
		r.FetchedAt = &t
	}
	if r.StaleSince != nil {
		t := *r.StaleSince
		r.StaleSince = &t
	}
	return r
}

// Peek is the last sprint read, without refreshing it (zero before the first Get).
func (c *Client) Peek() Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clone(c.cached)
}

// Get shares one bounded refresh among all readers. A disconnected reader does
// not cancel the refresh used by other requests; waiting readers may cancel.
func (c *Client) Get(ctx context.Context) Response {
	c.mu.Lock()
	stamp := stampKey(c.opts.Home)
	if c.opts.Now().Before(c.expires) && stamp == c.keyStamp {
		r := clone(c.cached)
		c.mu.Unlock()
		return r
	}
	if c.flight != nil {
		done := c.flight
		c.mu.Unlock()
		select {
		case <-done:
			return c.Get(ctx)
		case <-ctx.Done():
			return Response{State: "unreachable", Issues: []Issue{}}
		}
	}
	c.flight = make(chan struct{})
	c.mu.Unlock()
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	r := func() (r Response) {
		defer cancel()
		defer func() {
			if recover() != nil {
				r = Response{State: "unreachable", Issues: []Issue{}}
			}
		}()
		return c.fetch(refreshCtx)
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { close(c.flight); c.flight = nil }()
	now := c.opts.Now()
	if r.State == "ok" {
		r.FetchedAt = &now
	} else if len(r.Teams) == 0 && (r.State == "bad_key" || r.State == "unreachable") {
		r.Cycle, r.Issues, r.FetchedAt = c.cached.Cycle, c.cached.Issues, c.cached.FetchedAt
		if r.FetchedAt != nil {
			r.StaleSince = c.cached.StaleSince
			if r.StaleSince == nil {
				r.StaleSince = &now
			}
		}
	}
	c.cached = r
	c.expires = now.Add(TTL)
	c.keyStamp = stamp
	return clone(r)
}
func (c *Client) fetch(ctx context.Context) Response {
	r := Response{State: "unreachable", Issues: []Issue{}}
	local, err := config.LoadLocal(filepath.Join(c.opts.Home, "config.toml"))
	if err != nil {
		r.State = "off"
		return r
	}
	if len(local.Integrations.Linear.Teams) == 0 {
		r.State = "off"
		return r
	}
	if c.teams == nil {
		c.teams = map[string]Response{}
	}
	r.State = "ok"
	results := make([]Response, len(local.Integrations.Linear.Teams))
	var wg sync.WaitGroup
	for i, team := range local.Integrations.Linear.Teams {
		wg.Add(1)
		go func(i int, team string) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					results[i] = Response{State: "unreachable", Issues: []Issue{}}
				}
			}()
			results[i] = c.fetchTeam(ctx, team)
		}(i, team)
	}
	wg.Wait()
	for i, team := range local.Integrations.Linear.Teams {
		next := results[i]
		now := c.opts.Now()
		old := c.teams[team]
		if next.State == "ok" {
			next.FetchedAt = &now
		} else if next.State == "bad_key" || next.State == "unreachable" {
			next.Cycle, next.Issues, next.FetchedAt = old.Cycle, old.Issues, old.FetchedAt
			if next.FetchedAt != nil {
				next.StaleSince = old.StaleSince
				if next.StaleSince == nil {
					next.StaleSince = &now
				}
			}
		}
		if next.State != "ok" {
			r.State = next.State
		}
		c.teams[team] = next
		r.Teams = append(r.Teams, TeamResponse{Team: team, Response: next})
		r.Issues = append(r.Issues, next.Issues...)
		if i == 0 && next.Cycle != nil {
			cy := *next.Cycle
			r.Cycle = &cy
		}
	}
	if r.State != "ok" {
		for _, t := range r.Teams {
			if t.State == "ok" {
				r.State = "partial"
				break
			}
		}
	}
	// Only expose a common cycle when every team's boundaries and name agree.
	for _, t := range r.Teams {
		if t.Cycle == nil || r.Cycle == nil || *t.Cycle != *r.Cycle {
			r.Cycle = nil
			break
		}
	}
	if len(r.Teams) == 1 {
		r.FetchedAt = r.Teams[0].FetchedAt
		r.StaleSince = r.Teams[0].StaleSince
	}
	return r
}

func (c *Client) fetchTeam(ctx context.Context, team string) Response {
	r := Response{State: "unreachable", Issues: []Issue{}}
	key, err := ReadKey(c.opts.Home)
	if err != nil {
		r.State = "bad_key"
		return r
	}
	if key == "" {
		r.State = "no_key"
		return r
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]string{"team": team}})
	body, status := linearapi.Query(ctx, c.opts.HTTP, c.opts.Endpoint, key, payload)
	if status == 401 || status == 403 {
		r.State = "bad_key"
		return r
	}
	if status/100 != 2 {
		return r
	}
	// Remove any echoed credential before decoding or retaining upstream fields.
	body = []byte(strings.ReplaceAll(string(body), key, "[redacted]"))
	var data struct {
		Errors []json.RawMessage
		Data   struct {
			Teams struct {
				Nodes []struct {
					ActiveCycle *struct {
						Name     string
						StartsAt string
						EndsAt   string
						Issues   struct {
							Nodes []struct {
								Identifier, Title, URL      string
								AddedToCycleAt, CompletedAt string
								State                       struct{ Name, Type string }
							}
							PageInfo struct{ HasNextPage bool }
						}
					}
				}
			}
		}
	}
	if json.Unmarshal(body, &data) != nil || len(data.Errors) > 0 || len(data.Data.Teams.Nodes) != 1 {
		return r
	}
	cy := data.Data.Teams.Nodes[0].ActiveCycle
	if cy != nil {
		// Never present a silently truncated sprint as complete. No second API call.
		if cy.Issues.PageInfo.HasNextPage {
			return r
		}
		r.Cycle = &Cycle{Name: cy.Name, Start: cy.StartsAt, End: cy.EndsAt}
		for _, i := range cy.Issues.Nodes {
			r.Issues = append(r.Issues, Issue{Team: team, ID: i.Identifier, Title: i.Title, URL: i.URL, StatusName: i.State.Name, StatusType: i.State.Type, AddedAt: i.AddedToCycleAt, DoneAt: i.CompletedAt})
		}
	}
	// Decode first as well so JSON escape sequences cannot hide an echoed key.
	redact := func(s string) string { return strings.ReplaceAll(s, key, "[redacted]") }
	if r.Cycle != nil {
		r.Cycle.Name = redact(r.Cycle.Name)
		r.Cycle.Start = redact(r.Cycle.Start)
		r.Cycle.End = redact(r.Cycle.End)
	}
	for n := range r.Issues {
		i := &r.Issues[n]
		i.ID = redact(i.ID)
		i.Title = redact(i.Title)
		i.StatusName = redact(i.StatusName)
		i.StatusType = redact(i.StatusType)
		i.URL = redact(i.URL)
	}
	r.State = "ok"
	return r
}

// FreshIssues excludes stale team data from automated status checks.
func (r Response) FreshIssues() []Issue {
	if len(r.Teams) == 0 {
		if r.State == "ok" {
			return r.Issues
		}
		return nil
	}
	var issues []Issue
	for _, t := range r.Teams {
		if t.State == "ok" {
			issues = append(issues, t.Issues...)
		}
	}
	return issues
}
