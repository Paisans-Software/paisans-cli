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
// about it is kept, and reported as one step when it is printed.
type bufferedLine struct {
	r        Reporter
	mu       *sync.Mutex
	title    string
	details  []string
	explains []string
	notes    [][2]string
	ended    bool
	// g is the ordered group the line is in, nil for an unordered one,
	// whose lines print as they end.
	g *bufferedGroup
	// why is why it paused; finish ends its step once it is printed.
	why     string
	finish  func(Step)
	printed bool
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
	if l.ended || (l.g != nil && l.g.closed) {
		return
	}
	l.ended, l.finish = true, finish
	if l.g == nil {
		l.printLocked()
		return
	}
	l.g.flushLocked()
}

// printLocked reports the line: its step, its details and its end, then
// its advice and notes.
func (l *bufferedLine) printLocked() {
	l.printed = true
	s := l.r.Step(l.title)
	for _, d := range l.details {
		s.Detail("%s", d)
	}
	l.finish(s)
	for _, e := range l.explains {
		l.r.Explain(e)
	}
	for _, n := range l.notes {
		l.r.Note(n[0], n[1])
	}
}

// Track is a line of an ordered group: a place a run visits once or more,
// one visit at a time, whose line ends only after its last visit.
type Track interface {
	Line
	// Start starts a visit: the line spins and its clock runs.
	Start()
	// Pause ends a visit without ending the line: it stops spinning, its
	// clock stops, and it shows the time its visits took and why, Eg: what
	// it waits for. Its line is not ended, and its next visit is Start.
	Pause(why string)
}

// Orderer is a reporter that draws an ordered group itself.
type Orderer interface {
	Ordered(titles ...string) (lines []Track, done func())
}

// Ordered opens one line per title, drawn in the order of titles and kept
// there: a place a run visits one at a time, perhaps more than once. On a
// terminal each line is a row of a block redrawn in place: a dim · until
// its first visit, a spinner with its elapsed time while a visit runs, and
// between visits a dim · with the time its visits took and why it paused.
// A line's time is its visits' together. A line is printed whole, its
// details, advice and notes under it, once it and every line above it have
// ended, so the lines keep their order however they end. done prints every
// line not yet printed as it stands, the ones that have not ended as
// waiting, and draws nothing more; it is called once the run is over,
// however it ended, and a line ended after it prints nothing. Nothing else
// opens a step until done.
//
// A reporter that draws no ordered group of its own reports each line, in
// the same order, as a step opened and ended at once.
func Ordered(r Reporter, titles ...string) (lines []Track, done func()) {
	if o, ok := r.(Orderer); ok {
		return o.Ordered(titles...)
	}
	g := &bufferedGroup{}
	out := make([]Track, len(titles))
	for i, title := range titles {
		l := &bufferedLine{r: r, mu: &g.mu, title: title, g: g}
		g.lines = append(g.lines, l)
		out[i] = l
	}
	return out, g.done
}

// bufferedGroup is the lines of a reporter that draws no group, which an
// ordered group prints in their order.
type bufferedGroup struct {
	mu     sync.Mutex
	lines  []*bufferedLine
	closed bool
}

func (l *bufferedLine) Start() {}

func (l *bufferedLine) Pause(why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.why = why
}

// flushLocked prints each ended line no line above it is still waiting on.
func (g *bufferedGroup) flushLocked() {
	for _, l := range g.lines {
		if l.printed {
			continue
		}
		if !l.ended {
			return
		}
		l.printLocked()
	}
}

func (g *bufferedGroup) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	for _, l := range g.lines {
		if l.printed {
			continue
		}
		if !l.ended {
			l.ended = true
			why := l.why
			l.finish = func(s Step) { s.End(Waiting, why) }
		}
		l.printLocked()
	}
}

// lineGroup is a writer's group: the lines not yet printed, in the order
// they were opened, and how many rows of the terminal their block takes.
type lineGroup struct {
	r *writer
	// ordered keeps the lines in the order they were opened (see Ordered):
	// a line that ends stays in its row until every line above it has.
	ordered bool
	running []*line
	// drawn is how many rows the block takes, with the cursor at the start
	// of the row below it; 0 when it is not drawn.
	drawn int
	// queued are the lines that ended while a hold was in place, printed
	// once every hold is resumed so that none lands on a prompt.
	queued []func()
	stop   chan struct{}
	// closed is set by an ordered group's done: nothing it has is printed
	// again.
	closed bool
}

type line struct {
	r     *writer
	g     *lineGroup
	title string
	// visiting is whether a visit runs, since visit; spent is what the
	// visits before it took. A line of an unordered group is one visit.
	visiting bool
	visit    time.Time
	spent    time.Duration
	// why is why an ordered line paused, shown until its next visit.
	why      string
	progress string
	nested   string
	details  []string
	explains []string
	notes    [][2]string
	ended    bool
	mark     Mark
	result   string
}

// Group opens a group of lines on the writer. A step still open stops
// drawing; it prints its own line when it ends.
func (r *writer) Group(titles ...string) []Line {
	lines := r.openGroup(false, titles)
	out := make([]Line, len(lines))
	for i, l := range lines {
		out[i] = l
	}
	return out
}

// Ordered opens an ordered group of lines on the writer (see Ordered).
func (r *writer) Ordered(titles ...string) ([]Track, func()) {
	lines := r.openGroup(true, titles)
	out := make([]Track, len(lines))
	for i, l := range lines {
		out[i] = l
	}
	if len(lines) == 0 {
		return out, func() {}
	}
	return out, lines[0].g.done
}

func (r *writer) openGroup(ordered bool, titles []string) []*line {
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
	g := &lineGroup{r: r, ordered: ordered}
	now := r.now()
	for _, title := range titles {
		l := &line{r: r, g: g, title: title}
		if !ordered {
			l.visiting, l.visit = true, now
		}
		g.running = append(g.running, l)
	}
	if len(titles) == 0 {
		return g.running
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
	return append([]*line(nil), g.running...)
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

// drawGroupLocked draws the block of lines not yet printed over the one
// drawn before, unless a hold is in place. Each row is cut to the
// terminal's width, so none wraps and the block is as many rows as it has
// lines.
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
		b.WriteString(clear + r.rowLocked(l, now) + "\n")
	}
	b.WriteString("\x1b[J")
	fmt.Fprint(r.w, b.String())
	g.drawn = len(g.running)
}

// rowLocked is l's row of the block: its mark once it has ended, a spinner
// while a visit runs, and otherwise a dim · with what it has done so far.
func (r *writer) rowLocked(l *line, now time.Time) string {
	switch {
	case l.ended:
		return r.endLine(l.mark, l.text())
	case l.visiting:
		return r.spinnerLine(l.title, l.progress, l.nested, l.elapsedLocked(now))
	}
	text := l.waitingText()
	if r.termCols > 5 {
		if runes := []rune(text); len(runes) > r.termCols-5 {
			text = string(runes[:r.termCols-5])
		}
	}
	return r.endLine(Waiting, text)
}

// text is an ended line's title and result.
func (l *line) text() string {
	if l.result == "" {
		return l.title
	}
	return l.title + ": " + l.result
}

// waitingText is a line that is not visited as it stands: its title, the
// time its visits took, and why it paused.
func (l *line) waitingText() string {
	text := l.title
	if l.spent >= time.Second {
		text += fmt.Sprintf(": %.1fs", l.spent.Seconds())
	}
	if l.why != "" {
		text += " (" + l.why + ")"
	}
	return text
}

// elapsedLocked is the time l's visits have taken, the one running too.
func (l *line) elapsedLocked(now time.Time) time.Duration {
	d := l.spent
	if l.visiting {
		d += now.Sub(l.visit)
	}
	return d
}

// eraseGroupLocked clears the block, leaving the cursor where it started.
func (r *writer) eraseGroupLocked() {
	if g := r.group; g != nil && g.drawn > 0 {
		fmt.Fprintf(r.w, "\x1b[%dA\r\x1b[J", g.drawn)
		g.drawn = 0
	}
}

// flushGroupLocked prints the lines that ended during a hold, and closes the
// group once none is left.
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

func (l *line) Start() {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.ended || l.visiting {
		return
	}
	l.visiting, l.visit, l.why = true, r.now(), ""
	if r.group == l.g {
		r.drawGroupLocked()
	}
}

func (l *line) Pause(why string) {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.ended {
		return
	}
	l.stopLocked()
	l.why, l.progress, l.nested = why, "", ""
	if r.group == l.g {
		r.drawGroupLocked()
	}
}

// stopLocked ends the visit that runs, if one does.
func (l *line) stopLocked() {
	if l.visiting {
		l.spent += l.r.now().Sub(l.visit)
		l.visiting = false
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

// timed is result, or for none the time the line's visits took when that
// is a second or more, as a step's.
func (l *line) timed(result string) string {
	if result != "" {
		return result
	}
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	if d := l.elapsedLocked(l.r.now()); d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return ""
}

// End ends the line with its mark and result. A line of an unordered group
// is printed whole at once, leaving the block, which is drawn again below
// it; one of an ordered group is printed once every line above it has
// ended, and until then shows its mark in its row. During a hold the
// printing waits for the hold to be resumed.
func (l *line) End(m Mark, result string) {
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	g := l.g
	if l.ended || g.closed {
		return
	}
	l.ended, l.mark, l.result = true, m, result
	var ready []*line
	if g.ordered {
		l.stopLocked()
		for len(g.running) > 0 && g.running[0].ended {
			ready = append(ready, g.running[0])
			g.running = g.running[1:]
		}
	} else {
		for i, other := range g.running {
			if other == l {
				g.running = append(g.running[:i:i], g.running[i+1:]...)
				break
			}
		}
		ready = []*line{l}
	}
	r.printLinesLocked(g, ready)
}

// done prints every line of an ordered group not yet printed, as it
// stands, and closes the group.
func (g *lineGroup) done() {
	r := g.r
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
	r.printLinesLocked(g, ready)
}

// printLinesLocked prints ready, lines that have left g's block, each
// whole: its mark and result, or a line that has not ended as waiting, then
// its details, advice and notes. On a terminal the block is drawn again
// below them; during a hold they wait for the hold to be resumed.
func (r *writer) printLinesLocked(g *lineGroup, ready []*line) {
	if len(g.running) == 0 && g.stop != nil {
		close(g.stop)
		g.stop = nil
	}
	if len(ready) == 0 {
		if r.group == g {
			r.drawGroupLocked()
		}
		return
	}
	commit := func() {
		for _, l := range ready {
			if l.ended {
				fmt.Fprintln(r.w, r.endLine(l.mark, l.text()))
			} else {
				fmt.Fprintln(r.w, r.endLine(Waiting, l.waitingText()))
			}
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
