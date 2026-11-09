package ceilings

import (
	"context"
	"sync"
)

// fakeLineReader replays a scripted sequence of nd-JSON lines. After the
// last line, Scan returns false and the consumer treats the stream as
// cleanly closed (operator exited).
//
// blockUntilClosed: if true, after replaying all lines Scan blocks until
// Close() is called instead of returning false. Tests that want to exercise
// the "stream still open while watchdog runs" case set this true.
type fakeLineReader struct {
	mu               sync.Mutex
	lines            [][]byte
	idx              int
	closed           chan struct{}
	blockUntilClosed bool

	closeOnce sync.Once
}

func newFakeLineReader(lines []string, blockAfter bool) *fakeLineReader {
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return &fakeLineReader{
		lines:            out,
		closed:           make(chan struct{}),
		blockUntilClosed: blockAfter,
	}
}

func (r *fakeLineReader) Scan() bool {
	r.mu.Lock()
	if r.idx < len(r.lines) {
		r.idx++
		r.mu.Unlock()
		return true
	}
	r.mu.Unlock()
	if !r.blockUntilClosed {
		return false
	}
	<-r.closed
	return false
}

func (r *fakeLineReader) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines[r.idx-1]
}

func (r *fakeLineReader) Err() error { return nil }

func (r *fakeLineReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

// fakeSource hands out fakeLineReader on Open. Open blocks until SetReader
// is called for the first time so tests retain control over when the
// consumer starts seeing events.
type fakeSource struct {
	mu        sync.Mutex
	readers   map[int64]*fakeLineReader
	openCount int
	openErr   error
}

func newFakeSource() *fakeSource {
	return &fakeSource{readers: map[int64]*fakeLineReader{}}
}

func (s *fakeSource) installReader(runID int64, r *fakeLineReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readers[runID] = r
}

func (s *fakeSource) Open(_ context.Context, st *runState) (lineReader, error) {
	s.mu.Lock()
	s.openCount++
	if s.openErr != nil {
		err := s.openErr
		s.mu.Unlock()
		return nil, err
	}
	r, ok := s.readers[st.runID]
	s.mu.Unlock()
	if !ok {
		// No scripted reader: return one that closes immediately.
		return newFakeLineReader(nil, false), nil
	}
	return r, nil
}
