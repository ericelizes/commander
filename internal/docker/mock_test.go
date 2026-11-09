package docker

import (
	"context"
	"errors"
	"testing"
)

// Compile-time assertion (mirror the one in mock.go for double-coverage).
var _ Client = (*MockClient)(nil)

func TestMockClient_RecordsCalls(t *testing.T) {
	m := &MockClient{
		SpawnIDs:        []string{"id1", "id2"},
		InspectResponse: &ContainerInfo{ID: "abc", Status: "running"},
		StatsResponse:   &RuntimeStats{CPUPercent: 1.5},
		ListResponse:    []ContainerInfo{{ID: "x"}},
	}
	ctx := context.Background()

	id1, err := m.Spawn(ctx, SpawnOptions{Image: "img", Name: "n1"})
	if err != nil || id1 != "id1" {
		t.Fatalf("Spawn1 = %q, %v", id1, err)
	}
	id2, _ := m.Spawn(ctx, SpawnOptions{Image: "img", Name: "n2"})
	if id2 != "id2" {
		t.Errorf("Spawn2 = %q; want id2", id2)
	}
	id3, _ := m.Spawn(ctx, SpawnOptions{Image: "img", Name: "n3"})
	if id3 != "" {
		t.Errorf("Spawn3 should be empty after exhausting scripted IDs; got %q", id3)
	}

	if err := m.Stop(ctx, "abc"); err != nil {
		t.Errorf("Stop: %v", err)
	}

	info, _ := m.Inspect(ctx, "abc")
	if info == nil || info.ID != "abc" {
		t.Errorf("Inspect = %+v", info)
	}

	stats, _ := m.Stats(ctx, "abc")
	if stats.CPUPercent != 1.5 {
		t.Errorf("Stats = %+v", stats)
	}

	list, _ := m.ListByLabel(ctx, "k")
	if len(list) != 1 || list[0].ID != "x" {
		t.Errorf("ListByLabel = %+v", list)
	}

	// Verify call recording.
	wantPrefixes := []string{
		"Spawn(",
		"Spawn(",
		"Spawn(",
		"Stop(",
		"Inspect(",
		"Stats(",
		"ListByLabel(",
	}
	if len(m.Calls) != len(wantPrefixes) {
		t.Fatalf("calls = %v; want %d entries", m.Calls, len(wantPrefixes))
	}
	for i, p := range wantPrefixes {
		if !startsWith(m.Calls[i], p) {
			t.Errorf("Calls[%d] = %q; want prefix %q", i, m.Calls[i], p)
		}
	}
}

func TestMockClient_FuncOverride(t *testing.T) {
	wantErr := errors.New("boom")
	m := &MockClient{
		SpawnFunc: func(ctx context.Context, opts SpawnOptions) (string, error) {
			return "", wantErr
		},
	}
	if _, err := m.Spawn(context.Background(), SpawnOptions{Image: "i", Name: "n"}); !errors.Is(err, wantErr) {
		t.Errorf("Spawn err = %v; want %v", err, wantErr)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
