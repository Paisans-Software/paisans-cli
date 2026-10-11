package ui_test

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// lastRow is the row of the screen a spinner line is drawn on.
func lastRow(s *screen) string {
	got := s.lines()
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1]
}

// A group line shows the step a nested reporter has open, in parentheses
// after the elapsed time: the innermost open one, the enclosing one again
// once it ends, nothing once the nested reporter is done, and nothing on the
// line that ends.
func TestANestedStepShowsOnItsGroupLine(t *testing.T) {
	s := &screen{}
	r := ui.NewForTest(s, false, true, clock())
	lines := ui.Group(r, "home-a", "home-b")
	inner, done := ui.Nest(lines[0], ui.Discard)
	inner.Step("write 14 files")
	ui.Frame(r)
	if got := s.lines(); len(got) != 2 || !regexp.MustCompile(`^  ⠙ home-a: \d+s \(write 14 files\)$`).MatchString(got[0]) || strings.Contains(got[1], "(") {
		t.Errorf("outer step:\n%s", strings.Join(got, "\n"))
	}
	deep := inner.Step("start talk")
	ui.Frame(r)
	if got := s.lines()[0]; !strings.HasSuffix(got, "s (start talk)") {
		t.Errorf("innermost step: %q", got)
	}
	deep.Done("")
	ui.Frame(r)
	if got := s.lines()[0]; !strings.HasSuffix(got, "s (write 14 files)") {
		t.Errorf("after the innermost ended: %q", got)
	}
	done()
	ui.Frame(r)
	if got := s.lines()[0]; strings.Contains(got, "(") {
		t.Errorf("after the nested reporter was done: %q", got)
	}
	inner2, done2 := ui.Nest(lines[0], ui.Discard)
	inner2.Step("start docker")
	lines[0].Done("")
	done2()
	lines[1].Done("")
	if got := strings.Join(s.lines(), "\n"); strings.Contains(got, "start docker") || strings.Contains(got, "write 14 files") {
		t.Errorf("an ended line kept its nested step:\n%s", got)
	}
}

// The single spinner line shows a nested step after its progress and its
// elapsed time, and its finished line does not.
func TestANestedStepShowsOnTheSpinnerLine(t *testing.T) {
	s := &screen{}
	r := ui.NewForTest(s, false, true, clock())
	line := r.Step("vm, home-a, home-b")
	line.Progress("home-b")
	inner, done := ui.Nest(line, ui.Discard)
	st := inner.Step("wait for Patroni primary")
	ui.Frame(r)
	if got := lastRow(s); !regexp.MustCompile(`^  ⠙ vm, home-a, home-b: home-b: \d+s \(wait for Patroni primary\)$`).MatchString(got) {
		t.Errorf("spinner line: %q", got)
	}
	st.Progress("pg-1")
	if got := lastRow(s); !strings.HasSuffix(got, "s (wait for Patroni primary: pg-1)") {
		t.Errorf("a nested step's own progress: %q", got)
	}
	st.Fail(errors.New("boom"))
	done()
	line.Done("")
	if got := lastRow(s); !regexp.MustCompile(`^  ✓ vm, home-a, home-b: [\d.]+s$`).MatchString(got) {
		t.Errorf("finished line: %q", got)
	}
}

// A nested step goes on to the reporter it wraps, which -v prints from.
func TestANestedReporterPassesItsStepsOn(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, true, clock())
	line := r.Step("sites")
	rec := &ui.Recorder{}
	inner, done := ui.Nest(line, rec)
	s := inner.Step("start docker")
	s.Detail("why")
	s.Done("")
	done()
	if want := "step: start docker \ndetail: why start docker\ndone: start docker \n"; rec.Lines() != want {
		t.Errorf("got:\n%s\nwant:\n%s", rec.Lines(), want)
	}
	line.Done("")
}

// A hold clears a line with a nested step and resume draws it again.
func TestAHeldNestedStepIsDrawnAgainOnResume(t *testing.T) {
	s := &screen{}
	r := ui.NewForTest(s, false, true, clock())
	line := r.Step("sites")
	inner, done := ui.Nest(line, ui.Discard)
	inner.Step("install packages: ufw")
	resume := ui.Hold(r)
	if got := lastRow(s); got != "" {
		t.Errorf("held line: %q", got)
	}
	resume()
	if got := lastRow(s); !strings.HasSuffix(got, "(install packages: ufw)") {
		t.Errorf("resumed line: %q", got)
	}
	done()
	line.Done("")
}

// Plain output draws nothing between lines, so a nested step prints nothing
// on the line: it is the line alone, when it ends.
func TestPlainOutputShowsNoNestedStep(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, false, clock())
	lines := ui.Group(r, "home-a")
	inner, done := ui.Nest(lines[0], ui.Discard)
	inner.Step("install packages: ufw")
	one := r.Step("vm, home-a")
	inner2, done2 := ui.Nest(one, ui.Discard)
	inner2.Step("start talk")
	if b.String() != "" {
		t.Fatalf("printed before a line ended: %q", b.String())
	}
	done()
	lines[0].Done("")
	done2()
	one.Done("")
	if want := "  ok   home-a: 3.0s\n  ok   vm, home-a: 3.0s\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// A spinner line with a nested step is cut to the terminal's width, as a
// group's lines are, so it never wraps.
func TestANestedStepIsCutToTheWidth(t *testing.T) {
	s := &screen{}
	r := ui.NewForTestWidth(s, true, 30, clock())
	line := r.Step("vm, home-a, home-b")
	inner, done := ui.Nest(line, ui.Discard)
	inner.Step("install packages: wireguard-tools, ufw, docker-ce")
	ui.Frame(r)
	if got := lastRow(s); len([]rune(got)) != 29 {
		t.Errorf("row of %d runes: %q", len([]rune(got)), got)
	}
	done()
	line.Done("")
}

// Nested steps open and end from their own goroutines while the group's
// spinner draws: nothing may race.
func TestNestedStepsAreSafeBesideTheSpinner(t *testing.T) {
	var b syncBuf
	r := ui.NewForTestTick(&b, false, true, time.Now, time.Millisecond)
	lines := ui.Group(r, "a", "b", "c", "d")
	var wg sync.WaitGroup
	for _, l := range lines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inner, done := ui.Nest(l, ui.Discard)
			for k := 0; k < 100; k++ {
				s := inner.Step(fmt.Sprintf("step %d", k))
				s.Progress("p")
				s.Done("")
			}
			done()
			l.Done("")
		}()
	}
	wg.Wait()
	one := r.Step("sites")
	inner, done := ui.Nest(one, ui.Discard)
	for k := 0; k < 100; k++ {
		inner.Step("x").Done("")
	}
	done()
	one.Done("")
	assertSilent(t, &b, "after every line ended")
}
