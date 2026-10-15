package ui

import (
	"sync"
)

// Lines is a group whose lines join it as they start (see Stream).
type Lines interface {
	// Add opens a line, which runs until it is ended.
	Add(title string) Line
	// Close ends the group once its last line has ended. A line still
	// running is printed as waiting, and one ended after Close prints
	// nothing.
	Close()
}

// Streamer is a reporter that draws a stream of lines itself.
type Streamer interface {
	Stream() Lines
}

// Stream opens a group of lines that join it as they start, for work whose
// lines are not all known up front: a line is added when its work begins,
// so nothing spins for work that waits on other work. Its drawing is
// Group's: each line is printed whole, its details, advice and notes under
// it, when it ends, so lines ended from several goroutines never interleave
// mid line, and on a terminal the lines still running are a block of
// spinners below the ended ones. Off a terminal each line prints whole when
// it ends, with nothing drawn while it runs. Nothing else opens a step until
// Close.
//
// A reporter that draws no group of its own reports each line, when it
// ends, as a step opened and ended at once.
func Stream(r Reporter) Lines {
	if s, ok := r.(Streamer); ok {
		return s.Stream()
	}
	return &bufferedStream{r: r}
}

// bufferedStream is the stream of a reporter that draws no group.
type bufferedStream struct {
	r  Reporter
	mu sync.Mutex
}

func (s *bufferedStream) Add(title string) Line {
	return &bufferedLine{r: s.r, mu: &s.mu, title: title}
}

func (s *bufferedStream) Close() {}

// writerStream is a writer's stream: an unordered group that lines join.
type writerStream struct {
	r *writer
	g *lineGroup
}

// Stream opens a stream on the writer. A step still open stops drawing; it
// prints its own line when it ends.
func (r *writer) Stream() Lines {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev := r.open; prev != nil {
		r.interruptLocked()
		if prev.stop != nil {
			close(prev.stop)
			prev.stop = nil
		}
		r.open = nil
	}
	return &writerStream{r: r, g: &lineGroup{r: r}}
}

// Add puts a running line at the foot of the block. A stream whose lines
// have all ended has left the writer (see printLinesLocked), so the first
// line added after that brings it back, and its spinner with it.
func (s *writerStream) Add(title string) Line {
	r, g := s.r, s.g
	r.mu.Lock()
	defer r.mu.Unlock()
	l := &line{r: r, g: g, title: title, visiting: true, visit: r.now()}
	if g.closed {
		l.ended = true
		return l
	}
	g.running = append(g.running, l)
	r.group = g
	if r.terminal {
		r.drawGroupLocked()
		if r.tick > 0 && g.stop == nil {
			g.stop = make(chan struct{})
			go r.spinGroup(g, g.stop)
		}
	}
	return l
}

// Close prints any line still running as waiting, and closes the group.
func (s *writerStream) Close() {
	r, g := s.r, s.g
	r.mu.Lock()
	defer r.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	ready := g.running
	g.running = nil
	for _, l := range ready {
		l.stopLocked()
	}
	if len(ready) == 0 {
		if g.stop != nil {
			close(g.stop)
			g.stop = nil
		}
		if r.group == g {
			r.group = nil
		}
		return
	}
	r.printLinesLocked(g, ready)
}
