package brief

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the latest Brief under $COMMANDER_HOME.
const FileName = "brief.json"

// Stored is what brief.json holds: the Brief and the evidence it cites.
type Stored struct {
	Brief    Brief      `json:"brief"`
	Evidence []Evidence `json:"evidence"`
}

// Status is what GET /api/brief answers.
type Status struct {
	Brief      *Brief     `json:"brief,omitempty"` // nil until the first one is written
	Evidence   []Evidence `json:"evidence"`
	Generating bool       `json:"generating"`
	Error      string     `json:"error,omitempty"` // the last attempt failed; the Brief shown is the one before it
	NextAt     time.Time  `json:"next_at"`         // the next scheduled rewrite
}

// Gather reads the sources for the windows.
type Gather func(context.Context, Windows) (Sources, error)

// Service keeps the latest Brief: it loads brief.json, rewrites it on schedule
// and on demand (one at a time), and saves each new one.
type Service struct {
	path     string
	gather   Gather
	generate Generate
	now      func() time.Time

	mu      sync.Mutex
	cur     *Stored
	running bool
	err     string
	done    chan struct{} // closed when the running rewrite ends
}

// generateTimeout bounds one rewrite: the sources, then the writer's
// investigation and document (it has 10 minutes of this).
const generateTimeout = 12 * time.Minute

// writerTimeout bounds the writer's investigation and document.
const writerTimeout = 10 * time.Minute

// NewService loads the saved Brief from home, if any. It writes nothing until
// the next slot or a Start: a restart shows the last Brief.
func NewService(home string, gather Gather, generate Generate) *Service {
	s := &Service{path: filepath.Join(home, FileName), gather: gather, generate: generate, now: time.Now}
	if b, err := os.ReadFile(s.path); err == nil {
		var st Stored
		if json.Unmarshal(b, &st) == nil && !st.Brief.GeneratedAt.IsZero() {
			s.cur = &st
		}
	}
	return s
}

// Status is the latest Brief and whether a new one is being written.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Evidence: []Evidence{}, Generating: s.running, Error: s.err, NextAt: NextSlot(s.now(), Zone)}
	if s.cur != nil {
		b := s.cur.Brief
		st.Brief, st.Evidence = &b, s.cur.Evidence
	}
	return st
}

// Start begins a rewrite in the background unless one is running; it reports
// whether it started one.
func (s *Service) Start() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	s.done = make(chan struct{})
	done := s.done
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), generateTimeout)
		defer cancel()
		err := s.write(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.running = false
		s.err = ""
		if err != nil {
			log.Printf("brief: rewrite failed: %v", err)
			s.err = "The last attempt to write the Brief did not finish; it will try again at the next scheduled time, or press Regenerate."
		}
	}()
	return true
}

// Wait blocks until the running rewrite (if any) ends.
func (s *Service) Wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Write gathers, writes and saves one Brief now, in the caller's goroutine.
func (s *Service) Write(ctx context.Context) (Stored, error) {
	if err := s.write(ctx); err != nil {
		return Stored{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.cur, nil
}

func (s *Service) write(ctx context.Context) error {
	w := WindowsAt(s.now(), Zone)
	src, err := s.gather(ctx, w)
	if err != nil {
		return err
	}
	in := Assemble(w, src)
	md := ""
	if len(in.Evidence) > 0 {
		wctx, cancel := context.WithTimeout(ctx, writerTimeout)
		md, err = s.generate(wctx, in)
		cancel()
		if err != nil {
			return err
		}
	}
	b := Brief{GeneratedAt: s.now().UTC(), Windows: w, LinearNote: src.LinearNote}
	b.Sections, b.Dropped = Check(md, in)
	b.Markdown = Markdown(b.Sections)
	if b.Dropped > 0 {
		log.Printf("brief: dropped %d sentences or bullets the evidence did not back", b.Dropped)
	}
	st := Stored{Brief: b, Evidence: in.Evidence}
	if err := save(s.path, st); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = &st
	s.mu.Unlock()
	return nil
}

func save(path string, st Stored) error {
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".brief-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Run rewrites the Brief at each slot (7 AM Eastern, then every 6 hours) until
// ctx ends. It reads the wall clock every minute, so a suspended machine still
// catches the slot it woke past. It never writes at start.
func (s *Service) Run(ctx context.Context) {
	next := NextSlot(s.now(), Zone)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next = s.tick(next)
	}
}

// tick starts a rewrite once next has come and returns the slot after it.
func (s *Service) tick(next time.Time) time.Time {
	now := s.now()
	if now.Before(next) {
		return next
	}
	s.Start()
	return NextSlot(now, Zone)
}
