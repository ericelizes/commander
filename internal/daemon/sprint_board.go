package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
)

// sprintRefresh is how often the board asks the Linear client for a fresh
// sprint; the client's own TTL decides whether that calls Linear.
const sprintRefresh = 30 * time.Second

// sprintFeed hands the board the user's Linear sprint without ever making a
// snapshot wait on Linear: it reads the client's cache and refreshes it in
// the background.
type sprintFeed struct {
	lin  *linearsync.Client
	last atomic.Int64
	busy atomic.Bool
}

func (f *sprintFeed) read(now time.Time) linearsync.Response {
	if f == nil || f.lin == nil {
		return linearsync.Response{}
	}
	if now.Unix()-f.last.Load() >= int64(sprintRefresh/time.Second) && f.busy.CompareAndSwap(false, true) {
		f.last.Store(now.Unix())
		go func() {
			defer f.busy.Store(false)
			f.lin.Get(context.Background())
		}()
	}
	return f.lin.Peek()
}

// ticketCarrier is a workstream row that carries a ticket; working is judged
// by the track that carries it, or by the row when the workstream itself does.
type ticketCarrier struct {
	row     livedata.BoardRow
	working bool
}

// ticketCarriers is the ticket join the sidebar's My Progress makes (title ids
// and ticket: labels, open tracks only), by upper-cased id, in row order.
func ticketCarriers(d livedata.Display, workstreams []livedata.SnapWorkstream) map[string][]ticketCarrier {
	raw := map[string]livedata.SnapWorkstream{}
	for _, ws := range workstreams {
		raw[ws.Project+":"+ws.BeadID] = ws
	}
	out := map[string][]ticketCarrier{}
	for _, w := range d.Workstreams {
		for _, t := range livedata.WorkstreamTickets(raw[w.Key]) {
			working := t.Working || (!t.OnTrack && w.Working > 0)
			cs := out[t.ID]
			if n := len(cs); n > 0 && cs[n-1].row.Key == w.Key {
				cs[n-1].working = cs[n-1].working || working
				continue
			}
			out[t.ID] = append(cs, ticketCarrier{row: w, working: working})
		}
	}
	return out
}

// sprintEntries is Commander's view of the sprint: one summary line, then one
// line per open ticket that no workstream or track carries, so Commander
// sees what to map (the sidebar matches tickets by the id in a title or a ticket: label).
func sprintEntries(d livedata.Display, workstreams []livedata.SnapWorkstream, r linearsync.Response) []boardturn.Entry {
	if r.State != "ok" || r.Cycle == nil || len(r.Issues) == 0 {
		return nil
	}
	carriers := ticketCarriers(d, workstreams)
	started, open := 0, []linearsync.Issue{}
	for _, i := range r.Issues {
		closed := i.StatusType == "completed" || i.StatusType == "canceled"
		if closed || i.StatusType == "started" {
			started++
		}
		if !closed && len(carriers[strings.ToUpper(i.ID)]) == 0 {
			open = append(open, i)
		}
	}
	end := ""
	if t, err := time.Parse(time.RFC3339, r.Cycle.End); err == nil {
		end = ", ends " + t.Format("Jan 2")
	}
	summary := fmt.Sprintf("%s%s: %d of %d tickets started or done, %d open on no workstream", r.Cycle.Name, end, started, len(r.Issues), len(open))
	out := []boardturn.Entry{{ID: "sprint", Label: "Linear sprint", Kind: "sprint", Text: summary, Key: summary}}
	for _, i := range open {
		text := fmt.Sprintf("%s %s (%s): on no workstream. If a workstream covers it, add %s to that title or give it the label ticket:%s; if none does, ask the user whether it needs one.", i.ID, i.Title, i.StatusName, i.ID, i.ID)
		out = append(out, boardturn.Entry{ID: "sprint:" + i.ID, Label: "Unmapped", Kind: "sprint", Text: text, Key: text})
	}
	return out
}
