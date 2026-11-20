package brief

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/example-user/commander/internal/linearapi"
	"github.com/example-user/commander/internal/linearsync"
)

const linearQuery = `{ viewer { assignedIssues(first: 100, orderBy: updatedAt, filter: {updatedAt: {gte: "%s"}}) {
 nodes { identifier title updatedAt startedAt completedAt state { name type } } } } }`

// ReadLinear reads the user's assigned tickets that changed since since, with
// the saved Linear key. The note says why there are none when Linear could not
// be read ("" when it was).
func ReadLinear(ctx context.Context, home string, since time.Time, do linearapi.HTTPDo, endpoint string) ([]Ticket, string) {
	key, err := linearsync.ReadKey(home)
	if err != nil || key == "" {
		return nil, "Linear is not connected"
	}
	if endpoint == "" {
		endpoint = linearapi.Endpoint
	}
	payload, _ := json.Marshal(map[string]string{"query": fmt.Sprintf(linearQuery, since.UTC().Format(time.RFC3339))})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, status := linearapi.Query(ctx, do, endpoint, key, payload)
	if status/100 != 2 {
		return nil, "Linear could not be read"
	}
	body = []byte(strings.ReplaceAll(string(body), key, "[redacted]"))
	var data struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Viewer struct {
				AssignedIssues struct {
					Nodes []struct {
						Identifier, Title                 string
						UpdatedAt, StartedAt, CompletedAt string
						State                             struct{ Name, Type string }
					}
				}
			}
		}
	}
	if json.Unmarshal(body, &data) != nil || len(data.Errors) > 0 {
		return nil, "Linear could not be read"
	}
	at := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	var out []Ticket
	for _, n := range data.Data.Viewer.AssignedIssues.Nodes {
		out = append(out, Ticket{ID: n.Identifier, Title: n.Title, State: n.State.Name, StateType: n.State.Type, UpdatedAt: at(n.UpdatedAt), StartedAt: at(n.StartedAt), CompletedAt: at(n.CompletedAt)})
	}
	return out, ""
}
