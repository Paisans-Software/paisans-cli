package ui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The labels of a list printed twice, once as a plan and once as it runs,
// so the first is not read as work left undone.
const (
	PlanLabel = "plan, nothing done yet"
	RunLabel  = "running"
)

// ListRow is one row of a list as it is first drawn: a title, its mark and
// result, and the lines --verbose shows under it.
type ListRow struct {
	Title   string
	Mark    Mark
	Result  string
	Details []string
}

// ListWarning is a warning drawn under a list's rows, as Warn draws one.
type ListWarning struct {
	Hint, Detail string
}

// List is a block of rows drawn once, a plan, whose rows then run in
// place. Start runs row i under title, the row's title as the run knows
// it, Eg: with a value filled in that the plan could not know yet, and
// details, the lines --verbose shows under the run's line where the run is
// printed after the plan; a block redrawn in place keeps the details it
// was drawn with. The step it returns ends the row. Close ends the list
// once the run is over, however it ended.
type List interface {
	Start(i int, title string, details ...string) Step
	Close()
}

// Lister is a reporter that draws a list itself.
type Lister interface {
	List(rows []ListRow, warnings []ListWarning) List
}

// NewList draws rows, then warnings, as a list. On a terminal tall enough
// to hold the whole block, each row is then redrawn in place as it runs:
// its spinner, then its mark and result. Whatever is said while the rows
// run is printed below the block, and the block is found again past it.
// When something else has used the terminal in between (see Hold), or a
// row has scrolled out of reach, the block is drawn again below, under
// RunLabel, and updated there.
//
// Anywhere else, off a terminal or on one too short for the block, the
// rows are printed under PlanLabel, and the run is printed after them as
// steps, under RunLabel. A reporter that draws no list of its own reports
// the rows as steps ended at once, and each run as a step.
func NewList(r Reporter, rows []ListRow, warnings []ListWarning) List {
	if l, ok := r.(Lister); ok {
		return l.List(rows, warnings)
	}
	for _, row := range rows {
		s := r.Step(row.Title)
		for _, d := range row.Details {
			s.Detail("%s", d)
		}
		s.End(row.Mark, row.Result)
	}
	for _, w := range warnings {
		r.Warn(w.Hint, w.Detail)
	}
	return &stepList{r: r}
}

// stepList is the list of a reporter that draws none: each run a step.
type stepList struct {
	r Reporter
}

func (l *stepList) Start(_ int, title string, details ...string) Step {
	s := l.r.Step(title)
	for _, d := range details {
		s.Detail("%s", d)
	}
	return s
}

func (l *stepList) Close() {}

// listMargin is the rows of the terminal a block leaves free: the line the
// cursor rests on, and one to spare.
const listMargin = 2

// liveList is a writer's list.
type liveList struct {
	r    *writer
	rows []*liveRow
	// inPlace is a block redrawn in place; otherwise the run is printed
	// after it, and labelled is whether its label has been.
	inPlace, labelled bool
	// height is how many rows of the terminal the block takes, and base
	// the count of rows the writer had written (see tracked) once it was
	// drawn: what is printed below it since is the difference.
	height, base int
	// stale is a block something else has written below without the
	// writer counting it (see Hold): the next drawing draws it again.
	stale bool
	// begun is set by the first Start: a block gone stale after it is
	// drawn again as soon as the terminal is free.
	begun   bool
	running *liveRow
	stop    chan struct{}
	closed  bool
}

type liveRow struct {
	ListRow
	// top is the row of the block its line starts on, h how many rows of
	// the terminal that line takes.
	top, h  int
	running bool
	started time.Time
	// progress is what it is working on while it runs; details are those
	// given while it runs, printed below the block when it ends.
	progress string
	details  []string
	ended    bool
}

// List draws rows and warnings (see NewList).
func (r *writer) List(rows []ListRow, warnings []ListWarning) List {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	l := &liveList{r: r}
	for _, row := range rows {
		l.rows = append(l.rows, &liveRow{ListRow: row})
	}
	lines := l.layoutLocked()
	for _, w := range warnings {
		lines = append(lines, r.captureLocked(func() { r.warnLocked(w.Hint, w.Detail) })...)
	}
	l.height = r.heightOf(lines)
	l.inPlace = r.terminal && r.termCols > 0 && r.termRows > 0 && l.height+listMargin <= r.termRows
	if !l.inPlace {
		r.labelLocked(PlanLabel)
	}
	for _, line := range lines {
		fmt.Fprintln(r.w, line)
	}
	if l.inPlace {
		l.base = r.counted()
		if r.list != nil {
			r.list.closeLocked()
		}
		r.list = l
	}
	return l
}

// layoutLocked is the rows' lines as they stand, each row's details under
// it when verbose, with each row's place in the block set.
func (l *liveList) layoutLocked() []string {
	r := l.r
	var lines []string
	at := 0
	for _, row := range l.rows {
		line := r.endLine(row.Mark, row.text())
		row.top, row.h = at, r.rowsOf(line)
		lines = append(lines, line)
		at += row.h
		if r.verbose {
			for _, d := range row.Details {
				for _, dl := range strings.Split(d, "\n") {
					dl = detailIndent + dl
					lines = append(lines, dl)
					at += r.rowsOf(dl)
				}
			}
		}
	}
	return lines
}

func (row *liveRow) text() string {
	if row.Result == "" {
		return row.Title
	}
	return row.Title + ": " + row.Result
}

// captureLocked is what f writes, as lines.
func (r *writer) captureLocked(f func()) []string {
	saved := r.w
	var b strings.Builder
	r.w = &b
	f()
	r.w = saved
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

// labelLocked prints a list's label.
func (r *writer) labelLocked(label string) {
	fmt.Fprintf(r.w, "  %s:\n", italic(r.terminal, label))
}

func (l *liveList) Start(i int, title string, details ...string) Step {
	r := l.r
	if !l.inPlace {
		r.mu.Lock()
		if !l.labelled {
			l.labelled = true
			r.interruptLocked()
			r.labelLocked(RunLabel)
		}
		r.mu.Unlock()
		s := r.Step(title)
		for _, d := range details {
			s.Detail("%s", d)
		}
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l.stopLocked()
	row := l.rows[i]
	row.Title, row.running, row.started, row.ended, row.progress = title, true, r.now(), false, ""
	l.begun, l.running = true, row
	r.frame_ = 0
	l.drawRowLocked(row)
	if r.tick > 0 && !l.closed {
		l.stop = make(chan struct{})
		go l.spin(row, l.stop)
	}
	return &listStep{l: l, row: row}
}

func (l *liveList) spin(row *liveRow, stop chan struct{}) {
	t := time.NewTicker(l.r.tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			l.r.mu.Lock()
			if l.stop == stop && l.running == row {
				l.r.frame_++
				l.drawRowLocked(row)
			}
			l.r.mu.Unlock()
		}
	}
}

func (l *liveList) stopLocked() {
	if l.stop != nil {
		close(l.stop)
		l.stop = nil
	}
}

func (l *liveList) Close() {
	l.r.mu.Lock()
	defer l.r.mu.Unlock()
	l.closeLocked()
}

func (l *liveList) closeLocked() {
	l.stopLocked()
	l.closed, l.running = true, nil
	if l.r.list == l {
		l.r.list = nil
	}
}

// rowLineLocked is row's line as it is drawn now, cut to the rows of the
// terminal it was given, so a redraw never takes another's.
func (l *liveList) rowLineLocked(row *liveRow) string {
	r := l.r
	if row.running {
		return r.spinnerLine(row.Title, row.progress, "", r.now().Sub(row.started))
	}
	text := Redact(row.text())
	if limit := row.h*r.termCols - 5; limit > 0 {
		if runes := []rune(text); len(runes) > limit {
			text = string(runes[:limit])
		}
	}
	return r.endLine(row.Mark, text)
}

// drawRowLocked redraws row in its place, moving up to it from the cursor
// and back, past whatever has been printed below the block since. A block
// gone stale, or a row scrolled out of reach, is drawn again whole below.
// Nothing is drawn during a hold.
func (l *liveList) drawRowLocked(row *liveRow) {
	r := l.r
	if r.held > 0 || l.closed {
		return
	}
	t, ok := r.w.(*tracked)
	if !ok {
		return
	}
	up := l.upTo(row)
	if l.stale || up > r.termRows-1 {
		l.redrawLocked()
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[%dA%s", up, clear)
	for k := 1; k < row.h; k++ {
		b.WriteString("\x1b[1B" + clear)
	}
	if row.h > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", row.h-1)
	}
	line := l.rowLineLocked(row)
	b.WriteString(line)
	wraps := 0
	if w := visibleWidth(Redact(line)); w > 0 {
		wraps = (w - 1) / r.termCols
	}
	fmt.Fprintf(&b, "\r\x1b[%dB", up-wraps)
	if col := visibleWidth(t.partial) % r.termCols; col > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", col)
	}
	t.raw(b.String())
}

// upTo is how many rows of the terminal row's line is above the cursor.
func (l *liveList) upTo(row *liveRow) int {
	return l.r.counted() - l.base + l.height - row.top
}

// redrawLocked draws the rows again below, under RunLabel, as they stand,
// and updates them there from now on.
func (l *liveList) redrawLocked() {
	r := l.r
	l.stale = false
	r.interruptLocked()
	r.labelLocked(RunLabel)
	lines := l.layoutLocked()
	for i, row := range l.rows {
		if row.running {
			lines[l.lineOf(i)] = l.rowLineLocked(row)
		}
	}
	for _, line := range lines {
		fmt.Fprintln(r.w, line)
	}
	l.height = r.heightOf(lines)
	l.base = r.counted()
}

// lineOf is the index in layoutLocked's lines of row i's own line.
func (l *liveList) lineOf(i int) int {
	n := 0
	for _, row := range l.rows[:i] {
		n++
		if l.r.verbose {
			for _, d := range row.Details {
				n += strings.Count(d, "\n") + 1
			}
		}
	}
	return n
}

// heldLocked is called by Hold, before the hold is in place: whatever
// uses the terminal now is not counted. A row running is drawn once more,
// as continued below, so the block left above the prompt shows no frozen
// spinner; the run goes on in the block drawn again below.
func (l *liveList) heldLocked() {
	if row := l.running; row != nil && !l.stale && l.r.held == 0 && l.upTo(row) <= l.r.termRows-1 {
		row.running = false
		mark, result := row.Mark, row.Result
		row.Mark, row.Result = Waiting, "continued below"
		l.drawRowLocked(row)
		row.Mark, row.Result, row.running = mark, result, true
	}
	l.stale = true
}

// resumedLocked is called once every hold is resumed: a run that has begun
// is drawn again at once, so its rows are not left as they were.
func (l *liveList) resumedLocked() {
	if l.stale && l.begun && !l.closed {
		l.redrawLocked()
	}
}

type listStep struct {
	l   *liveList
	row *liveRow
}

func (s *listStep) Detail(format string, args ...any) {
	r := s.l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbose && !s.row.ended {
		s.row.details = append(s.row.details, fmt.Sprintf(format, args...))
	}
}

func (s *listStep) Progress(text string) {
	r := s.l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.row.ended {
		return
	}
	s.row.progress = text
	s.l.drawRowLocked(s.row)
}

func (s *listStep) Done(result string) { s.End(OK, s.timed(result)) }

func (s *listStep) Fail(error) { s.End(Failed, s.timed("")) }

func (s *listStep) timed(result string) string {
	if result != "" {
		return result
	}
	r := s.l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.now().Sub(s.row.started); d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return ""
}

func (s *listStep) End(m Mark, result string) {
	l, row := s.l, s.row
	r := l.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if row.ended {
		return
	}
	row.ended, row.running, row.Mark, row.Result, row.progress = true, false, m, result, ""
	if l.running == row {
		l.stopLocked()
		l.running = nil
	}
	l.drawRowLocked(row)
	for _, d := range row.details {
		r.detailLocked(d)
	}
}

// tracked counts what it writes in rows of the terminal, wrapped at cols,
// so a list finds its block again past what was printed below it.

// raw writes s, redacted, without counting it: a list's cursor moves and
// redrawn rows, which print nothing new.
func (t *tracked) raw(s string) {
	fmt.Fprint(t.w, Redact(s))
}

// count adds the rows s takes to the count, keeping the line not yet ended.
func (t *tracked) count(s string) {
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			t.partial += s
			// Only what follows the last carriage return is on the
			// line, so a spinner's frames are not kept.
			if j := strings.LastIndexByte(t.partial, '\r'); j > 0 {
				t.partial = t.partial[j:]
			}
			return
		}
		t.rows += lineRows(t.partial+s[:i], t.cols)
		t.partial = ""
		s = s[i+1:]
	}
}

// counted is the rows the writer has written.
func (r *writer) counted() int {
	if t, ok := r.w.(*tracked); ok {
		return t.rows
	}
	return 0
}

func (r *writer) rowsOf(line string) int { return lineRows(Redact(line), r.termCols) }

func (r *writer) heightOf(lines []string) int {
	n := 0
	for _, line := range lines {
		n += r.rowsOf(line)
	}
	return n
}

// lineRows is how many rows of a terminal cols wide line takes.
func lineRows(line string, cols int) int {
	w := visibleWidth(line)
	if cols <= 0 || w <= cols {
		return 1
	}
	return (w-1)/cols + 1
}

var escape = regexp.MustCompile(`\x1b(\[[0-9;?]*[A-Za-z]|[78])`)

// visibleWidth is the columns line takes once drawn: what follows its last
// carriage return, without escape sequences.
func visibleWidth(line string) int {
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	return utf8.RuneCountInString(escape.ReplaceAllString(line, ""))
}
