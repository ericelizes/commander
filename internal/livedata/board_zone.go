package livedata

import (
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/example-user/commander/internal/config"
)

var boardZone atomic.Pointer[time.Location]

// BoardZone is the zone every board time is formatted in: [user] timezone of
// config.toml once SetBoardZone/LoadBoardZone ran, else the process zone.
func BoardZone() *time.Location {
	if l := boardZone.Load(); l != nil {
		return l
	}
	return time.Local
}

// SetBoardZone sets the board zone (nil = the process zone).
func SetBoardZone(l *time.Location) { boardZone.Store(l) }

// LoadBoardZone reads [user] timezone from <home>/config.toml. A missing file
// or unset key keeps the process zone; an invalid name logs a warning and does
// the same.
func LoadBoardZone(home string) {
	cfg, err := config.Load(filepath.Join(home, "config.toml"))
	if err != nil {
		SetBoardZone(nil)
		SetStaleDays(config.DefaultStaleDays)
		return
	}
	SetStaleDays(cfg.Needs.StaleDaysOr())
	loc, err := cfg.User.Location()
	if err != nil {
		log.Printf("board: %v; using the process zone", err)
	}
	SetBoardZone(loc)
}
