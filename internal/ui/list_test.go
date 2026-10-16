package ui_test

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// vt is a terminal of a fixed size for a test: text wraps at its width, a
// newline at its last row scrolls the top row off into the history, and a
// cursor move cannot leave the visible rows, as on a real terminal. A
// redraw that tries to reach a row scrolled off lands on the wrong row,
// which a test then sees. Colour is dropped.
type vt struct {
	width, height int
	// lines is every row ever drawn, the history first; top is the first
	// visible one.
	lines    [][]rune
	top      int
	row, col int
	// wrap is a pending wrap: the last column was written, and the next
	// rune goes on the next row.
	wrap bool
}

func newVT(width, height int) *vt { return &vt{width: width, height: height} }

var vtCSI = regexp.MustCompile(`^\x1b\[([0-9;?]*)([A-Za-z])`)

func (v *vt) Write(p []byte) (int, error) {
	in := string(p)
	for len(in) > 0 {
		if m := vtCSI.FindStringSubmatch(in); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n == 0 {
				n = 1
			}
			switch m[2] {
			case "A":
				v.row = max(v.top, v.row-n)
				v.wrap = false
			case "B":
				v.row = min(v.top+v.height-1, v.row+n)
				v.wrap = false
			case "C":
				v.col = min(v.width-1, v.col+n)
				v.wrap = false
			case "K":
				v.cut(v.row, v.col)
			case "J":
				v.cut(v.row, v.col)
				for r := v.row + 1; r < len(v.lines); r++ {
					v.lines[r] = nil
				}
			}
			in = in[len(m[0]):]
			continue
		}
		r := []rune(in)[0]
		in = in[len(string(r)):]
		switch r {
		case '\r':
			v.col, v.wrap = 0, false
		case '\n':
			v.newline()
		default:
			if v.wrap {
				v.newline()
			}
			v.put(r)
		}
	}
	return len(p), nil
}

func (v *vt) newline() {
	v.row++
	v.col, v.wrap = 0, false
	if v.row >= v.top+v.height {
		v.top = v.row - v.height + 1
	}
	v.grow(v.row)
}

func (v *vt) grow(row int) {
	for len(v.lines) <= row {
		v.lines = append(v.lines, nil)
	}
}

func (v *vt) cut(row, col int) {
	v.grow(row)
	if col < len(v.lines[row]) {
		v.lines[row] = v.lines[row][:col]
	}
}

func (v *vt) put(r rune) {
	v.grow(v.row)
	line := v.lines[v.row]
	for len(line) < v.col {
		line = append(line, ' ')
	}
	if v.col < len(line) {
		line[v.col] = r
	} else {
		line = append(line, r)
	}
	v.lines[v.row] = line
	if v.col == v.width-1 {
		v.wrap = true
	} else {
		v.col++
	}
}

// all is every row, the history with the visible ones, without the empty
// rows at the end.
func (v *vt) all() []string {
	var out []string
	for _, l := range v.lines {
		out = append(out, string(l))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func sizedReporter(v *vt, verbose bool) ui.Reporter {
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	return ui.NewForTestSize(v, verbose, v.width, v.height, func() time.Time { return at })
}

func listRows(titles ...string) []ui.ListRow {
	var rows []ui.ListRow
	for _, t := range titles {
		rows = append(rows, ui.ListRow{Title: t, Mark: ui.Pending})
	}
	return rows
}

// Off a terminal both lists are printed, each under a label saying which
// it is: the plan, of which nothing is done yet, then the run.
func TestAListOffATerminalLabelsBothLists(t *testing.T) {
	var b strings.Builder
	r := ui.NewPlain(&b, false)
	rows := listRows("make the user", "write the table")
	rows[1].Result = "runs once the user exists"
	l := ui.NewList(r, rows, []ui.ListWarning{{Hint: "ufw is off"}})
	l.Start(0, "make the user").Done("")
	l.Start(1, "write the table for 1001").End(ui.OK, "done already")
	l.Close()
	want := "  plan, nothing done yet:\n" +
		"  todo make the user\n" +
		"  todo write the table: runs once the user exists\n" +
		"  WARN ufw is off\n" +
		"  running:\n" +
		"  ok   make the user\n" +
		"  ok   write the table for 1001: done already\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

// A reporter that draws no list of its own reports the rows as steps, then
// each run as a step.
func TestAListOnARecorderIsSteps(t *testing.T) {
	rec := &ui.Recorder{}
	l := ui.NewList(rec, listRows("one"), []ui.ListWarning{{Hint: "careful"}})
	l.Start(0, "one, filled").Done("")
	want := "step: one \npending: one \nwarn: careful \nstep: one, filled \ndone: one, filled \n"
	if rec.Lines() != want {
		t.Errorf("got:\n%s\nwant:\n%s", rec.Lines(), want)
	}
}

// At a terminal that holds it, the plan's block is drawn once and its rows
// are updated in place as they run: a spinner, then the mark and result,
// with a title the run fills in drawn as filled. What is said while the
// rows run, Eg: a failure's explanation, goes below the block.
func TestAListAtATerminalUpdatesItsRowsInPlace(t *testing.T) {
	v := newVT(60, 20)
	r := sizedReporter(v, false)
	r.Section("admit paisans-f2a9 on vm")
	rows := listRows("make the user", "write the table for <uid>", "start the manager")
	rows[1].Result = "runs once the user exists"
	l := ui.NewList(r, rows, []ui.ListWarning{{Hint: "ufw is off"}})
	s := l.Start(0, "make the user")
	ui.Frame(r)
	sameRows(t, v.all(), []string{
		"admit paisans-f2a9 on vm",
		"  ⠙ make the user",
		"  ○ write the table for <uid>: runs once the user exists",
		"  ○ start the manager",
		"  ! ufw is off",
	})
	s.Done("")
	l.Start(1, "write the table for 1001").End(ui.OK, "done already")
	l.Start(2, "start the manager").Fail(errors.New("boom"))
	r.Explain("the manager did not start")
	l.Close()
	sameRows(t, v.all(), []string{
		"admit paisans-f2a9 on vm",
		"  ✓ make the user",
		"  ✓ write the table for 1001: done already",
		"  ✗ start the manager",
		"  ! ufw is off",
		"       the manager did not start",
	})
}

// Lines said between rows, below the block, are counted, so a row is still
// found after them, also when one wraps.
func TestAListFindsItsRowsPastWhatIsSaidBelowIt(t *testing.T) {
	v := newVT(30, 20)
	r := sizedReporter(v, false)
	l := ui.NewList(r, listRows("one", "a title long enough to wrap at thirty", "three"), nil)
	l.Start(0, "one").Done("")
	r.Warn("a warning long enough that the terminal wraps it", "")
	l.Start(1, "a title long enough to wrap at thirty").Done("")
	r.Warn("another", "")
	l.Start(2, "three").Done("2.0s")
	got := v.all()
	want := []string{
		"  ✓ one",
		"  ✓ a title long enough to wra",
		"p at thirty",
		"  ✓ three: 2.0s",
	}
	sameRows(t, got[:4], want)
	if !strings.Contains(strings.Join(got[4:], "\n"), "another") {
		t.Errorf("the warnings are not below the block:\n%s", strings.Join(got, "\n"))
	}
}

// Once something else has used the terminal between the plan and the run,
// Eg: a prompt, the block above it cannot be found reliably: the run draws
// it again below, under its label, and updates that one in place.
func TestAListIsDrawnAgainAfterAHold(t *testing.T) {
	v := newVT(60, 20)
	r := sizedReporter(v, false)
	l := ui.NewList(r, listRows("one", "two"), nil)
	resume := ui.Hold(r)
	v.Write([]byte("\nType the digest: sha256:abc\n"))
	resume()
	l.Start(0, "one").Done("")
	l.Start(1, "two").Done("")
	sameRows(t, v.all(), []string{
		"  ○ one",
		"  ○ two",
		"",
		"Type the digest: sha256:abc",
		"  running:",
		"  ✓ one",
		"  ✓ two",
	})
}

// A hold while a row runs, Eg: ssh asking for a password, draws the block
// again below the prompt as it stands, and carries on there.
func TestAListIsDrawnAgainAfterAHoldMidRun(t *testing.T) {
	v := newVT(60, 20)
	r := sizedReporter(v, false)
	l := ui.NewList(r, listRows("one", "two"), nil)
	s := l.Start(0, "one")
	resume := ui.Hold(r)
	v.Write([]byte("password: \n"))
	resume()
	s.Done("")
	l.Start(1, "two").Done("")
	got := v.all()
	want := []string{"  running:", "  ✓ one", "  ✓ two"}
	sameRows(t, got[len(got)-3:], want)
}

// A block taller than the terminal cannot be reached once its top has
// scrolled off, so the plan and the run are two lists, each labelled.
func TestAListTallerThanTheTerminalIsTwoLabelledLists(t *testing.T) {
	v := newVT(60, 6)
	r := sizedReporter(v, false)
	titles := []string{"one", "two", "three", "four", "five"}
	l := ui.NewList(r, listRows(titles...), nil)
	for i, title := range titles {
		l.Start(i, title).Done("")
	}
	got := strings.Join(v.all(), "\n")
	want := "  plan, nothing done yet:\n  ○ one\n  ○ two\n  ○ three\n  ○ four\n  ○ five\n" +
		"  running:\n  ✓ one\n  ✓ two\n  ✓ three\n  ✓ four\n  ✓ five"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// When what is said below the block pushes a row off the top of the
// terminal, the block is drawn again below, so no update lands on the
// wrong row.
func TestAListDrawsAgainWhenARowScrollsOff(t *testing.T) {
	v := newVT(60, 8)
	r := sizedReporter(v, false)
	l := ui.NewList(r, listRows("one", "two"), nil)
	l.Start(0, "one").Done("")
	for range 8 {
		r.Warn("noise", "")
	}
	l.Start(1, "two").Done("")
	got := v.all()
	sameRows(t, got[len(got)-3:], []string{"  running:", "  ✓ one", "  ✓ two"})
	sameRows(t, got[:2], []string{"  ✓ one", "  ○ two"})
}

// Under --verbose a row's details are part of the block, drawn once; the
// row above them is still updated in place.
func TestAListKeepsItsDetailsUnderItsRows(t *testing.T) {
	v := newVT(60, 20)
	r := sizedReporter(v, true)
	rows := listRows("one", "two")
	rows[0].Details = []string{"what one does"}
	l := ui.NewList(r, rows, nil)
	l.Start(0, "one").Done("")
	l.Start(1, "two").Done("")
	sameRows(t, v.all(), []string{"  ✓ one", "      what one does", "  ✓ two"})
}

// A row's spinner ticks on its own goroutine; the rows still end as they
// should, and -race finds nothing between the ticks and the run.
func TestAListSpinsWhileARowRuns(t *testing.T) {
	v := &lockedVT{vt: newVT(60, 20)}
	r := ui.NewForTestSizeTick(v, 60, 20, time.Millisecond)
	l := ui.NewList(r, listRows("one", "two"), nil)
	s := l.Start(0, "one")
	time.Sleep(10 * time.Millisecond)
	s.End(ui.OK, "")
	s = l.Start(1, "two")
	time.Sleep(10 * time.Millisecond)
	s.End(ui.OK, "")
	l.Close()
	v.mu.Lock()
	defer v.mu.Unlock()
	sameRows(t, v.vt.all(), []string{"  ✓ one", "  ✓ two"})
}

type lockedVT struct {
	mu sync.Mutex
	vt *vt
}

func (v *lockedVT) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.vt.Write(p)
}
