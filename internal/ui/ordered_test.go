package ui_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// manual is a clock that moves only when the test moves it.
type manual struct {
	mu sync.Mutex
	at time.Time
}

func newManual() *manual { return &manual{at: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)} }

func (m *manual) now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at
}

func (m *manual) add(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at = m.at.Add(d)
}

// An ordered group draws one row per title, in the order given, and keeps
// each in place: a dim · until its first visit, a spinner while a visit
// runs, a dim · with the time it has worked and why between visits, and
// its mark once it ends. Its time is what its visits took together.
func TestAnOrderedGroupKeepsItsLinesInPlace(t *testing.T) {
	s := &screen{}
	c := newManual()
	r := ui.NewForTest(s, true, true, c.now)
	r.Section("install")
	lines, done := ui.Ordered(r, "home-a", "home-b", "storage")
	sameRows(t, s.lines(), []string{"install", "  · home-a", "  · home-b", "  · storage"})

	lines[0].Start()
	c.add(22 * time.Second)
	ui.Frame(r)
	sameRows(t, s.lines(), []string{"install", "  ⠙ home-a: 22s", "  · home-b", "  · storage"})
	lines[0].Detail("✓ deployment apply --site home-a: paused until home-b runs etcd")
	lines[0].Pause("paused until home-b runs etcd")
	c.add(5 * time.Second)
	ui.Frame(r)
	sameRows(t, s.lines(), []string{"install", "  · home-a: 22.0s, paused until home-b runs etcd", "  · home-b", "  · storage"})

	lines[1].Start()
	c.add(3 * time.Second)
	lines[1].Pause("paused until storage is done")
	lines[2].Start()
	c.add(4 * time.Second)
	lines[2].Done("")
	// storage ended, but a line above it has not: it stays in its row.
	sameRows(t, s.lines(), []string{"install", "  · home-a: 22.0s, paused until home-b runs etcd", "  · home-b: 3.0s, paused until storage is done", "  ✓ storage: 4.0s"})

	lines[0].Start()
	c.add(48 * time.Second)
	ui.Frame(r)
	sameRows(t, s.lines(), []string{"install", "  ⠸ home-a: 70s", "  · home-b: 3.0s, paused until storage is done", "  ✓ storage: 4.0s"})
	lines[0].Detail("✓ deployment apply --site home-a: 48.0s")
	lines[0].Done("")
	sameRows(t, s.lines(), []string{
		"install",
		"  ✓ home-a: 70.0s",
		"      ✓ deployment apply --site home-a: paused until home-b runs etcd",
		"      ✓ deployment apply --site home-a: 48.0s",
		"  · home-b: 3.0s, paused until storage is done",
		"  ✓ storage: 4.0s",
	})
	lines[1].Start()
	c.add(time.Second)
	lines[1].Done("")
	done()
	sameRows(t, s.lines(), []string{
		"install",
		"  ✓ home-a: 70.0s",
		"      ✓ deployment apply --site home-a: paused until home-b runs etcd",
		"      ✓ deployment apply --site home-a: 48.0s",
		"  ✓ home-b: 4.0s",
		"  ✓ storage: 4.0s",
	})
	r.Result("done")
	if got := s.lines(); got[len(got)-1] != "done" {
		t.Errorf("after the result:\n%s", strings.Join(got, "\n"))
	}
}

// A line that fails is marked in its row; done leaves the lines that did
// not end as they stand, each with its details and notes, and draws nothing
// more.
func TestAnOrderedGroupThatStopsLeavesTheRestWaiting(t *testing.T) {
	var b syncBuf
	c := newManual()
	r := ui.NewForTestTick(&b, false, true, c.now, time.Millisecond)
	lines, done := ui.Ordered(r, "home-a", "home-b", "home-c")
	lines[0].Start()
	c.add(2 * time.Second)
	lines[0].Note("secrets: smtp.password is owed", "paisans secrets set smtp.password < value")
	lines[0].Pause("paused until home-b runs etcd")
	lines[1].Start()
	c.add(3 * time.Second)
	lines[1].Explain("Mount it, then apply.")
	lines[1].Fail(errors.New("boom"))
	done()
	assertSilent(t, &b, "after done")
	s := &screen{}
	s.Write([]byte(b.String()))
	sameRows(t, s.lines(), []string{
		"  · home-a: 2.0s, paused until home-b runs etcd",
		"  ! secrets: smtp.password is owed",
		"       paisans secrets set smtp.password < value",
		"  ✗ home-b: 3.0s",
		"       Mount it, then apply.",
		"  · home-c",
	})
	done()
	lines[2].Done("")
	if s2 := (&screen{}); true {
		s2.Write([]byte(b.String()))
		sameRows(t, s2.lines(), s.lines())
	}
}

// A line's nested step shows on its row while it runs, and the row of a
// line between visits shows none.
func TestAnOrderedLineShowsItsNestedStep(t *testing.T) {
	s := &screen{}
	c := newManual()
	r := ui.NewForTest(s, false, true, c.now)
	lines, done := ui.Ordered(r, "home-a", "home-b")
	lines[0].Start()
	inner, finish := ui.Nest(lines[0], ui.Discard)
	inner.Step("wait for Patroni primary")
	c.add(2 * time.Second)
	ui.Frame(r)
	sameRows(t, s.lines(), []string{"  ⠙ home-a: 2s (wait for Patroni primary)", "  · home-b"})
	finish()
	lines[0].Pause("paused until storage is done")
	sameRows(t, s.lines(), []string{"  · home-a: 2.0s, paused until storage is done", "  · home-b"})
	lines[0].Done("")
	lines[1].Done("")
	done()
}

// A paused line shows why after its time, or after its title alone when
// its visits took under a second.
func TestAPausedLineShowsWhyAfterItsTime(t *testing.T) {
	s := &screen{}
	c := newManual()
	r := ui.NewForTest(s, false, true, c.now)
	lines, done := ui.Ordered(r, "home-a", "home-b", "storage")
	lines[0].Start()
	c.add(39200 * time.Millisecond)
	lines[0].Pause("paused until storage and home-b are done")
	lines[1].Start()
	c.add(500 * time.Millisecond)
	lines[1].Pause("paused until storage is done")
	sameRows(t, s.lines(), []string{
		"  · home-a: 39.2s, paused until storage and home-b are done",
		"  · home-b: paused until storage is done",
		"  · storage",
	})
	done()
}

// A hold clears the block, and what ends during it prints once it is
// resumed, in place.
func TestAHeldOrderedGroupPrintsNothingUntilResumed(t *testing.T) {
	var b strings.Builder
	c := newManual()
	r := ui.NewForTest(&b, false, true, c.now)
	lines, done := ui.Ordered(r, "home-a", "home-b")
	lines[0].Start()
	resume := ui.Hold(r)
	held := b.String()
	if !strings.HasSuffix(held, "\x1b[2A\r\x1b[J") {
		t.Errorf("the hold did not clear the block: %q", held)
	}
	ui.Frame(r)
	lines[0].Done("")
	if b.String() != held {
		t.Errorf("drew during the hold: %q", b.String()[len(held):])
	}
	resume()
	s := &screen{}
	s.Write([]byte(b.String()))
	sameRows(t, s.lines(), []string{"  ✓ home-a", "  · home-b"})
	lines[1].Done("")
	done()
}

// Plain output prints each line once, whole, when it and every line above
// it have ended, so the lines keep their order; done prints the rest as
// they stand, with no progress.
func TestAPlainOrderedGroupPrintsLinesInOrder(t *testing.T) {
	var b strings.Builder
	c := newManual()
	r := ui.NewForTest(&b, true, false, c.now)
	lines, done := ui.Ordered(r, "home-a", "home-b", "storage", "home-c")
	lines[0].Start()
	c.add(2 * time.Second)
	lines[0].Detail("a detail")
	lines[0].Progress("docker")
	lines[0].Pause("paused until storage is done")
	lines[2].Start()
	c.add(time.Second)
	lines[2].Done("")
	if b.String() != "" {
		t.Fatalf("printed before the first line ended: %q", b.String())
	}
	lines[0].Start()
	c.add(time.Second)
	lines[0].Done("")
	lines[1].Start()
	c.add(4 * time.Second)
	lines[1].Fail(errors.New("boom"))
	done()
	want := "  ok   home-a: 3.0s\n      a detail\n  FAIL home-b: 4.0s\n  ok   storage: 1.0s\n  wait home-c\n"
	if b.String() != want {
		t.Errorf("got %q\nwant %q", b.String(), want)
	}
}

// A reporter that draws no group of its own gets each line whole, in the
// order given, as a step opened and ended at once; done reports the rest as
// waiting.
func TestAnOrderedGroupOnARecorderKeepsTheOrder(t *testing.T) {
	rec := &ui.Recorder{}
	lines, done := ui.Ordered(rec, "home-a", "home-b", "home-c")
	lines[0].Start()
	lines[0].Pause("paused until home-b runs etcd")
	lines[1].Start()
	lines[1].Done("")
	lines[0].Start()
	lines[0].Detail("a detail")
	lines[0].Note("left", "do")
	lines[0].Done("")
	done()
	want := "step: home-a \ndetail: a detail home-a\ndone: home-a \nnote: left do\nstep: home-b \ndone: home-b \nstep: home-c \nwaiting: home-c \n"
	if rec.Lines() != want {
		t.Errorf("got:\n%s\nwant:\n%s", rec.Lines(), want)
	}
}

// Lines are visited and ended from another goroutine while the spinner
// draws: nothing may race, and nothing draws once done returns.
func TestAnOrderedGroupIsSafeAcrossGoroutines(t *testing.T) {
	var b syncBuf
	r := ui.NewForTestTick(&b, true, true, time.Now, time.Millisecond)
	lines, done := ui.Ordered(r, "a", "b", "c")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, l := range lines {
			l.Start()
			l.Progress("x")
			l.Detail("d")
			time.Sleep(2 * time.Millisecond)
			l.Pause("later")
		}
		for _, l := range lines {
			l.Start()
			l.Done("")
		}
	}()
	wg.Wait()
	done()
	assertSilent(t, &b, "after done")
}
