package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Line is one line of a group: a step that runs at the same time as the
// group's other lines, and keeps what follows it, its advice and its notes,
// to print with it once it ends.
type Line interface {
	Step
	// Explain is what to do about the line, printed under it when it ends,
	// at every verbosity. Called before the line ends.
	Explain(text string)
	// Note is a note for the operator, printed after the line, and after its
	// advice, when it ends. Called before the line ends.
	Note(hint, detail string)
}

// Grouper is a reporter that draws a group of lines itself.
type Grouper interface {
	Group(titles ...string) []Line
}

// Group opens one line per title, each a step that runs at the same time as
// the others and is ended from whichever goroutine runs it. A line is
// printed whole when it ends, its details, advice and notes under it, so the
// lines never interleave; they print in the order they end. On a terminal
// the lines still running are a block of spinners below the ended ones,
// redrawn in place, each with its own elapsed time. Every line must be ended,
// and nothing else opens a step while the group runs.
//
// A reporter that draws no group of its own reports each line, when it ends,
// as a step opened and ended at once.
func Group(r Reporter, titles ...string) []Line {
	if g, ok := r.(Grouper); ok {
		return g.Group(titles...)
	}
	mu := &sync.Mutex{}
	out := make([]Line, len(titles))
	for i, title := range titles {
		out[i] = &bufferedLine{r: r, mu: mu, title: title}
	}
	return out
}

// bufferedLine is a line of a reporter that draws no group: what is said
// about it is kept, and reported as one step when it ends.
type bufferedLine struct {
	r        Reporter
	mu       *sync.Mutex
	title    string
	details  []string
	explains []string
	notes    [][2]string
	ended    bool
}

func (l *bufferedLine) Detail(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ended {
		l.details = append(l.details, fmt.Sprintf(format, args...))
	}
}

func (l *bufferedLine) Progress(string) {}

func (l *bufferedLine) Explain(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ended && text != "" {
		l.explains = append(l.explains, text)
	}
}

func (l *bufferedLine) Note(hint, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ended {
		l.notes = append(l.notes, [2]string{hint, detail})
	}
}

func (l *bufferedLine) Done(result string)        { l.end(func(s Step) { s.Done(result) }) }
func (l *bufferedLine) Fail(err error)            { l.end(func(s Step) { s.Fail(err) }) }
func (l *bufferedLine) End(m Mark, result string) { l.end(func(s Step) { s.End(m, result) }) }

func (l *bufferedLine) end(finish func(Step)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	l.ended = true
	s := l.r.Step(l.title)
	for _, d := range l.details {
		s.Detail("%s", d)
	}
	finish(s)
	for _, e := range l.explains {
		l.r.Explain(e)
	}
	for _, n := range l.notes {
		l.r.Note(n[0], n[1])
	}
}

// lineGroup is a writer's group: the lines still running, in the order they
// were opened, and how many rows of the terminal their block takes.
type lineGroup struct {
	running []*line
	// drawn is how many rows the block takes, with the cursor at the start
	// of the row below it; 0 when it is not drawn.
	drawn int
	// queued are the lines that ended while a hold was in place, printed
	// once every hold is resumed so that none lands on a prompt.
	queued []func()
	stop   chan struct{}
}

type line struct {
	r        *writer
	g        *lineGroup
	title    string
	started  time.Time
	progress string
	nested   string
	details  []string
	explains []string
	notes    [][2]string
	ended    bool
}

// Group opens a group of lines on the writer. A step still open stops
// drawing; it prints its own line when it ends.
func (r *writer) Group(titles ...string) []Line {
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
	g := &lineGroup{}
	out := make([]Line, len(titles))
	now := r.now()
	for i, title := range titles {
		l := &line{r: r, g: g, title: title, started: now}
		g.running = append(g.running, l)
		out[i] = l
	}
	if len(titles) == 0 {
		return out
	}
	r.group = g
	if r.terminal {
		r.frame_ = 0
		r.drawGroupLocked()
		if r.tick > 0 {
			g.stop = make(chan struct{})
			go r.spinGroup(g, g.stop)
		}
	}
	return out
}

func (r *writer) spinGroup(g *lineGroup, stop chan struct{}) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.mu.Lock()
			if r.group == g {
				r.frame_++
				r.drawGroupLocked()
			}
			r.mu.Unlock()
		}
	}
}

// drawGroupLocked draws the block of running lines over the one drawn
// before, unless a hold is in place. Each row is cut to the terminal's
// width, so none wraps and the block is as many rows as it has lines.
func (r *writer) drawGroupLocked() {
	g := r.group
	if g == nil || !r.terminal || r.held > 0 {
		return
	}
	var b strings.Builder
	if g.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", g.drawn)
	}
	now := r.now()
	for _, l := range g.running {
		b.WriteString(clear + r.spinnerLine(l.title, l.progress, l.nested, now.Sub(l.started)) + "\n")
	}
	b.WriteString("\x1b[J")
	fmt.Fprint(r.w, b.String())
	g.drawn = len(g.running)
}

// eraseGroupLocked clears the block, leaving the cursor where it started.
func (r *writer) eraseGroupLocked() {
	if g := r.group; g != nil && g.drawn > 0 {
		fmt.Fprintf(r.w, "\x1b[%dA\r\x1b[J", g.drawn)
		g.drawn = 0
	}
}

// flushGroupLocked prints the lines that ended during a hold, and closes the
// group once none is running.
func (r *writer) flushGroupLocked() {
	g := r.group
	if g == nil {
		return
	}
	for _, commit := range g.queued {
		commit()
	}
	g.queued = nil
	if len(g.running) == 0 {
		r.group = nil
	}
}

func (l *line) Detail(format string, args ...any) {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	if l.r.verbose && !l.ended {
		l.details = append(l.details, fmt.Sprintf(format, args...))
	}
}

func (l *line) Progress(text string) {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.ended {
		return
	}
	l.progress = text
	if r.group == l.g {
		r.drawGroupLocked()
	}
}

func (l *line) showNested(text string) {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.ended || l.nested == text {
		return
	}
	l.nested = text
	if r.group == l.g {
		r.drawGroupLocked()
	}
}

func (l *line) Explain(text string) {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	if !l.ended && text != "" {
		l.explains = append(l.explains, text)
	}
}

func (l *line) Note(hint, detail string) {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	if !l.ended {
		l.notes = append(l.notes, [2]string{hint, detail})
	}
}

func (l *line) Done(result string) { l.End(OK, l.timed(result)) }

func (l *line) Fail(error) { l.End(Failed, l.timed("")) }

// timed is result, or for none the elapsed time when the line took a second
// or more, as a step's.
func (l *line) timed(result string) string {
	if result != "" {
		return result
	}
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	if d := l.r.now().Sub(l.started); d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return ""
}

// End prints the line whole: its mark and result, then its details, advice
// and notes. On a terminal it leaves the block, which is drawn again below
// it; during a hold it waits for the hold to be resumed.
func (l *line) End(m Mark, result string) {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.ended {
		return
	}
	l.ended = true
	g := l.g
	for i, other := range g.running {
		if other == l {
			g.running = append(g.running[:i:i], g.running[i+1:]...)
			break
		}
	}
	text := l.title
	if result != "" {
		text += ": " + result
	}
	commit := func() {
		fmt.Fprintln(r.w, r.endLine(m, text))
		for _, d := range l.details {
			r.detailLocked(d)
		}
		for _, e := range l.explains {
			r.explainLocked(e)
		}
		for _, n := range l.notes {
			r.hintLocked(false, n[0])
			if n[1] != "" {
				r.explainLocked(n[1])
			}
		}
	}
	if len(g.running) == 0 && g.stop != nil {
		close(g.stop)
		g.stop = nil
	}
	if r.held > 0 && r.group == g {
		g.queued = append(g.queued, commit)
		return
	}
	if r.group == g {
		r.eraseGroupLocked()
	}
	commit()
	if r.group == g {
		if len(g.running) == 0 {
			r.group = nil
			return
		}
		r.drawGroupLocked()
	}
}
