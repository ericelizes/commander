package main

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

type memCheckins map[string]store.Checkin

func (m memCheckins) Checkins(context.Context) (map[string]store.Checkin, error) { return m, nil }
func (m memCheckins) SetCheckin(_ context.Context, role string, c store.Checkin) error {
	m[role] = c
	return nil
}

func TestSetCheckinBoundsAndKeepsLastLook(t *testing.T) {
	now := time.Unix(1_000, 0)
	m := memCheckins{"manager-p-1": {Due: 5, Checked: 900}}
	if _, err := setCheckin(context.Background(), m, "manager-p-1", 10*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if m["manager-p-1"] != (store.Checkin{Due: 1_600, Checked: 900}) {
		t.Fatalf("checkin = %+v", m["manager-p-1"])
	}
	for _, bad := range []time.Duration{30 * time.Second, 31 * time.Minute} {
		if _, err := setCheckin(context.Background(), m, "manager-p-1", bad, now); err == nil {
			t.Fatalf("--in %s accepted", bad)
		}
	}
	if _, err := setCheckin(context.Background(), m, "commander", 45*time.Minute, now); err != nil {
		t.Fatalf("commander 45m: %v", err)
	}
	if _, err := setCheckin(context.Background(), m, "commander", 2*time.Minute, now); err == nil {
		t.Fatal("commander 2m accepted")
	}
}
