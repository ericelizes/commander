package livedata

import (
	"testing"
	"time"
)

func TestRecentReturnsOnlyAFreshBuild(t *testing.T) {
	var s Source
	if _, ok := s.Recent(time.Minute); ok {
		t.Fatal("no build yet, but Recent answered")
	}
	s.recentData, s.recentAt = Data{Postgres: true}, time.Now()
	if d, ok := s.Recent(time.Minute); !ok || !d.Postgres {
		t.Fatal("fresh build not returned")
	}
	s.recentAt = time.Now().Add(-time.Hour)
	if _, ok := s.Recent(time.Minute); ok {
		t.Fatal("stale build returned")
	}
}
