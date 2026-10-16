package ui

import (
	"io"
	"time"
)

// NewForTest builds a reporter with terminal rendering forced on or off and
// a clock the test controls, so spinner frames and elapsed times are fixed.
func NewForTest(w io.Writer, verbose, terminal bool, clock func() time.Time) Reporter {
	return NewForTestTick(w, verbose, terminal, clock, 0)
}

// NewForTestTick is NewForTest with a real spinner tick. A zero tick starts
// no goroutine; a small one exercises the spinner's concurrency under -race.
func NewForTestTick(w io.Writer, verbose, terminal bool, clock func() time.Time, tick time.Duration) Reporter {
	r := newWriter(w, verbose, terminal)
	r.now = clock
	r.tick = tick
	return r
}

// Frame draws one spinner frame for the open step, as the goroutine would.
func Frame(r Reporter) { r.(*writer).frame() }

// PrintErrorForTest is PrintError with the rendering and the terminal's width
// chosen by the test.
func PrintErrorForTest(w io.Writer, err error, verbose, terminal bool, width int) {
	printError(w, err, verbose, terminal, width)
}

// ShortPathFrom is ShortPath from a given directory and home.
var ShortPathFrom = shortPath

// NewForTestWidth is NewForTest on a terminal of cols columns, as New would
// find it; 0 is a width that is not known.
func NewForTestWidth(w io.Writer, terminal bool, cols int, clock func() time.Time) Reporter {
	return NewForTestVerboseWidth(w, false, terminal, cols, clock)
}

// NewForTestVerboseWidth is NewForTestWidth with verbose chosen by the test.
func NewForTestVerboseWidth(w io.Writer, verbose, terminal bool, cols int, clock func() time.Time) Reporter {
	r := NewForTest(w, verbose, terminal, clock).(*writer)
	r.termCols, r.cols = cols, cols
	return r
}

// ItalicFor is Italic with the terminal chosen by the test.
var ItalicFor = italic

// DangerFor is Danger with the terminal chosen by the test.
var DangerFor = danger

// NewForTestSize is NewForTest on a terminal cols wide and rows high.
func NewForTestSize(w io.Writer, verbose bool, cols, rows int, clock func() time.Time) Reporter {
	r := NewForTest(w, verbose, true, clock).(*writer)
	r.setSize(cols, rows)
	return r
}

// NewForTestSizeTick is NewForTestSize with a real spinner tick.
func NewForTestSizeTick(w io.Writer, cols, rows int, tick time.Duration) Reporter {
	r := NewForTestSize(w, false, cols, rows, time.Now).(*writer)
	r.tick = tick
	return r
}
