package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	green  = "\x1b[32m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	ital   = "\x1b[3m"
	reset  = "\x1b[0m"
	clear  = "\r\x1b[K"
)

type writer struct {
	mu sync.Mutex
	// cols is how wide prose is wrapped: the terminal's width when it is
	// known, so a wrapped line keeps its indent, and 0, not wrapped, when it
	// is not.
	cols int
	// termCols is the terminal's own width, 0 when it is not known. A hint
	// wraps only when it is narrower than the hint's line.
	termCols int
	w        io.Writer
	verbose  bool
	terminal bool
	now      func() time.Time
	tick     time.Duration

	open *step
	// group is the group of lines running at once, while one is.
	group  *lineGroup
	frame_ int
	// held counts the holds in place (see Hold). While any is, nothing is
	// drawn, so a prompt on the terminal stays readable.
	held int
	// termRows is the terminal's height, 0 when it is not known.
	termRows int
	// list is the list whose block is redrawn in place, while one is.
	list *liveList
}

// setSize sets the terminal's size, cols wide and rows high, 0 for what is
// not known.
func (r *writer) setSize(cols, rows int) {
	r.termCols, r.cols, r.termRows = cols, cols, rows
	if t, ok := r.w.(*tracked); ok {
		t.cols = cols
	}
}

func newWriter(w io.Writer, verbose, terminal bool) *writer {
	return &writer{w: &tracked{w: w}, verbose: verbose, terminal: terminal, now: time.Now, tick: 100 * time.Millisecond}
}

type step struct {
	r       *writer
	title   string
	started time.Time
	details []string
	// progress is what the step is working on now, drawn after its title
	// while it is open. The writer's mutex guards it, since the spinner's
	// goroutine reads it.
	progress string
	// nested is the step a command nested in this one has open (see Nest),
	// drawn after the elapsed time; the writer's mutex guards it too.
	nested string
	ended  bool
	// stop closes when this step's spinner must stop. It lives on the step,
	// not the writer, so ending one step cannot stop another step's spinner.
	stop chan struct{}
}

func (r *writer) Verbose() bool { return r.verbose }

func (r *writer) Section(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	if t, ok := r.w.(*tracked); ok && t.wrote {
		// A blank line sets the section apart from what came before.
		fmt.Fprintln(r.w)
	}
	fmt.Fprintf(r.w, "%s\n", title)
}

// tracked is the reporter's output, and whether anything has been written
// to it yet, so the first section of a run is not preceded by a blank line.
type tracked struct {
	w     io.Writer
	wrote bool
	// rows counts the rows of the terminal the lines written so far take,
	// wrapped at cols, and partial is the line not yet ended (see List).
	cols    int
	rows    int
	partial string
}

// Write is where every line the reporter draws leaves it, so each is
// redacted here (see Secret).
func (t *tracked) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.wrote = true
	}
	text := Redact(string(p))
	t.count(text)
	if _, err := io.WriteString(t.w, text); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *writer) Step(title string) Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A step that opens while another is still open has no end to wait for
	// from the caller's side, so the earlier spinner stops here. Two spinners
	// drawing one line would interleave; the earlier step still prints its
	// own line when it ends.
	if prev := r.open; prev != nil && prev.stop != nil {
		close(prev.stop)
		prev.stop = nil
	}
	s := &step{r: r, title: title, started: r.now()}
	r.open = s
	if r.terminal {
		r.frame_ = 0
		r.drawLocked()
		if r.tick > 0 {
			s.stop = make(chan struct{})
			go r.spin(s, s.stop)
		}
	}
	return s
}

func (r *writer) spin(s *step, stop chan struct{}) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.spinFrame(s, stop)
		}
	}
}

// spinFrame draws a tick only while its own step is still the open one. A
// tick can be waiting on the mutex when its step ends, and it must not draw
// over whatever step came next.
func (r *writer) spinFrame(s *step, stop chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open != s || s.stop != stop || !r.terminal {
		return
	}
	r.frame_++
	r.drawLocked()
}

// frame draws the next spinner frame for the open step, if there is one.
func (r *writer) frame() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.terminal {
		return
	}
	if l := r.list; l != nil && l.running != nil && r.open == nil && r.group == nil {
		r.frame_++
		l.drawRowLocked(l.running)
		return
	}
	if r.open == nil && r.group == nil {
		return
	}
	r.frame_++
	r.drawLocked()
}

// drawLocked draws the open step's spinner line, unless a hold is in place.
func (r *writer) drawLocked() {
	if r.group != nil {
		r.drawGroupLocked()
		return
	}
	s := r.open
	if s == nil || r.held > 0 {
		return
	}
	fmt.Fprint(r.w, clear+r.spinnerLine(s.title, s.progress, s.nested, r.now().Sub(s.started)))
}

// spinnerLine is a running line as it is drawn: the spinner and title, then
// what it is working on, its elapsed time once it is a second or more, and
// the step a command nested in it has open, in parentheses. It is cut to the
// terminal's width, so it never wraps and a redraw stays on its row.
func (r *writer) spinnerLine(title, progress, nested string, elapsed time.Duration) string {
	text := "  " + frames[r.frame_%len(frames)] + " " + title
	if progress != "" {
		text += ": " + progress
	}
	if elapsed >= time.Second {
		text += fmt.Sprintf(": %ds", int(elapsed.Seconds()))
	}
	if nested != "" {
		text += " (" + nested + ")"
	}
	// Redacted before it is cut, so a secret at the edge is not left in
	// part.
	text = Redact(text)
	if r.termCols > 1 {
		if runes := []rune(text); len(runes) > r.termCols-1 {
			text = string(runes[:r.termCols-1])
		}
	}
	return text
}

func (s *step) Detail(format string, args ...any) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.r.verbose && !s.ended {
		s.details = append(s.details, fmt.Sprintf(format, args...))
	}
}

// Progress replaces what the open step shows it is working on, and redraws
// its spinner line at once, unless a hold is in place. An ended step keeps
// the line it ended with.
func (s *step) Progress(text string) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ended {
		return
	}
	s.progress = text
	if r.terminal && r.open == s {
		r.drawLocked()
	}
}

// showNested replaces the nested step the open step shows, as Progress
// replaces its progress.
func (s *step) showNested(text string) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ended || s.nested == text {
		return
	}
	s.nested = text
	if r.terminal && r.open == s {
		r.drawLocked()
	}
}

func (s *step) Done(result string) { s.End(OK, s.timed(result)) }

func (s *step) Fail(err error) { s.End(Failed, s.timed("")) }

// timed is result, or for none the elapsed time when the step took a second
// or more. Only a step that did work is timed: End is also a dry run's
// status line, where the time it took to check says nothing.
func (s *step) timed(result string) string {
	if result != "" {
		return result
	}
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if d := s.r.now().Sub(s.started); d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return ""
}

func (s *step) End(m Mark, result string) {
	r := s.r
	r.mu.Lock()
	if s.ended {
		r.mu.Unlock()
		return
	}
	s.ended = true
	stop := s.stop
	s.stop = nil
	if r.open == s {
		r.open = nil
	}
	text := s.title
	if result != "" {
		text += ": " + result
	}
	line := r.endLine(m, text)
	if r.terminal {
		line = clear + line
	}
	fmt.Fprintln(r.w, line)
	for _, d := range s.details {
		r.detailLocked(d)
	}
	r.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// endLine is a finished step's line: its mark, then text.
func (r *writer) endLine(m Mark, text string) string {
	if r.terminal && m == Waiting {
		// A step that waits is dimmed whole: it is not this run's to plan.
		return "  " + dim + "· " + text + reset
	}
	return "  " + r.mark(m) + " " + text
}

func (r *writer) mark(m Mark) string {
	if r.terminal {
		switch m {
		case OK:
			return green + "✓" + reset
		case Pending:
			return yellow + "○" + reset
		case Waiting:
			return dim + "·" + reset
		case Warned:
			return yellow + "!" + reset
		default:
			return red + "✗" + reset
		}
	}
	switch m {
	case OK:
		return "ok  "
	case Pending:
		return "todo"
	case Waiting:
		return "wait"
	case Warned:
		return "WARN"
	default:
		return "FAIL"
	}
}

func (r *writer) Warn(hint, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	r.warnLocked(hint, detail)
}

func (r *writer) warnLocked(hint, detail string) {
	r.hintLocked(false, hint)
	if r.verbose && detail != "" {
		r.detailLocked(strings.Join(Wrap(detail, r.cols-len(detailIndent)), "\n"))
	}
}

func (r *writer) Note(hint, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	r.hintLocked(false, hint)
	r.explainLocked(detail)
}

func (r *writer) Explain(text string) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	r.explainLocked(text)
}

func (r *writer) Refuse(hint, explanation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	r.hintLocked(true, hint)
	r.explainLocked(explanation)
}

// hintLocked prints a warning's or a refusal's hint behind its mark. It is
// one line, unless the terminal is known to be narrower than that line: then
// it wraps at spaces, each continuation under the hint's text, rather than
// letting the terminal break it mid-word.
func (r *writer) hintLocked(refusal bool, hint string) {
	mark, plain := yellow+"!"+reset, "WARN"
	if refusal {
		mark, plain = red+"✗"+reset, "FAIL"
	}
	lead, width := "  "+plain+" ", len(plain)+3
	if r.terminal {
		lead, width = "  "+mark+" ", 4
	}
	lines := []string{hint}
	if r.termCols > 0 && width+len(hint) > r.termCols {
		lines = Wrap(hint, r.termCols-width)
	}
	for i, line := range lines {
		if i > 0 {
			lead = strings.Repeat(" ", width)
		}
		fmt.Fprintf(r.w, "%s%s\n", lead, line)
	}
}

// explainIndent aligns the lines that belong to a hint under the hint's text
// rather than under its mark; detailIndent sets a verbose line under what it
// belongs to.
const (
	explainIndent = "       "
	detailIndent  = "      "
)

// explainLocked prints the lines that belong to a hint at every verbosity,
// wrapped to the width.
func (r *writer) explainLocked(text string) {
	for _, line := range Wrap(text, r.cols-len(explainIndent)) {
		fmt.Fprintf(r.w, "%s%s\n", explainIndent, line)
	}
}

// Hold clears the spinner line and draws nothing until every hold is
// resumed, then draws the open step again. Plain output draws nothing between
// lines, so there it does nothing. Each resume releases its own hold once,
// however often it is called, so a deferred resume beside an explicit one
// cannot release a hold that belongs to someone else.
func (r *writer) Hold() (resume func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.terminal {
		return func() {}
	}
	r.held++
	r.interruptLocked()
	if r.list != nil {
		r.list.heldLocked()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.held--
			if r.held == 0 {
				r.flushGroupLocked()
				if r.list != nil {
					r.list.resumedLocked()
				}
			}
			r.drawLocked()
		})
	}
}

func (r *writer) Detail(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.verbose {
		return
	}
	text := fmt.Sprintf(format, args...)
	if r.open != nil {
		r.open.details = append(r.open.details, text)
		return
	}
	r.interruptLocked()
	r.detailLocked(text)
}

func (r *writer) Trace(label, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.verbose {
		return
	}
	text := label + ":"
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		text += "\n| " + line
	}
	if r.open != nil {
		r.open.details = append(r.open.details, text)
		return
	}
	r.interruptLocked()
	r.detailLocked(text)
}

// Result is the run's last word, set off by a blank line from anything
// drawn above it.
func (r *writer) Result(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interruptLocked()
	if t, ok := r.w.(*tracked); ok && t.wrote {
		fmt.Fprintln(r.w)
	}
	fmt.Fprintf(r.w, format+"\n", args...)
}

// detailLocked prints a verbose line indented under what it belongs to.
func (r *writer) detailLocked(text string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(r.w, "%s%s\n", detailIndent, line)
	}
}

// interruptLocked ends a drawn spinner line before something else is
// printed, so a warning said mid-step does not land on the spinner's line.
// The spinner redraws on its next tick.
func (r *writer) interruptLocked() {
	if r.terminal && r.open != nil {
		fmt.Fprint(r.w, clear)
	}
	if r.terminal {
		r.eraseGroupLocked()
	}
}
