package ui_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// An open step's progress is drawn after its title on the spinner line, and
// the finished line is the title and the result alone.
func TestProgressShowsOnTheSpinnerLine(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, true, clock())
	s := r.Step("home-a, home-b")
	s.Progress("home-a")
	ui.Frame(r)
	s.Progress("home-b")
	ui.Frame(r)
	s.Done("")
	out := b.String()
	for _, want := range []string{"home-a, home-b: home-a: ", "home-a, home-b: home-b: "} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
	last := out[strings.LastIndex(out, "\r\x1b[K"):]
	if want := "\r\x1b[K  \x1b[32m✓\x1b[0m home-a, home-b: "; !strings.HasPrefix(last, want) || strings.Contains(last, "home-b: home-b") || !strings.HasSuffix(last, "s\n") {
		t.Errorf("finished line %q", last)
	}
}

// Plain output draws nothing between lines, so progress prints nothing: one
// line when the step ends.
func TestPlainProgressPrintsNothing(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, false, clock())
	s := r.Step("home-a, home-b")
	s.Progress("home-a")
	s.Progress("home-b")
	if b.String() != "" {
		t.Fatalf("progress printed: %q", b.String())
	}
	s.Done("")
	if want := "  ok   home-a, home-b: 1.5s\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// Progress on an ended step changes nothing, and a held spinner stays
// cleared until it is resumed.
func TestProgressAfterEndOrDuringAHoldDrawsNothing(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, true, clock())
	s := r.Step("sites")
	resume := ui.Hold(r)
	before := b.String()
	s.Progress("home-a")
	if b.String() != before {
		t.Errorf("progress drew during a hold: %q", b.String()[len(before):])
	}
	resume()
	if !strings.Contains(b.String(), "sites: home-a") {
		t.Errorf("resume did not draw the progress: %q", b.String())
	}
	s.Done("")
	ended := b.String()
	s.Progress("home-b")
	if b.String() != ended {
		t.Errorf("progress after the end drew: %q", b.String()[len(ended):])
	}
}

// Progress is set from the run while the spinner's goroutine draws: neither
// may race the other.
func TestProgressIsSafeBesideTheSpinner(t *testing.T) {
	var b syncBuf
	r := ui.NewForTestTick(&b, false, true, time.Now, time.Millisecond)
	s := r.Step("sites")
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.Progress(fmt.Sprintf("site-%d", i))
			}
		}()
	}
	wg.Wait()
	waitFor(t, "a drawn progress", func() bool { return strings.Contains(b.String(), "sites: site-") })
	s.Done("")
	assertSilent(t, &b, "after Done")
}

// The Recorder keeps each progress as an event of its step.
func TestRecorderKeepsProgress(t *testing.T) {
	rec := &ui.Recorder{}
	s := rec.Step("sites")
	s.Progress("home-a")
	s.Done("")
	if i := rec.Index("progress", "home-a"); i != 1 || rec.Events[i].Extra != "sites" {
		t.Errorf("events:\n%s", rec.Lines())
	}
}
