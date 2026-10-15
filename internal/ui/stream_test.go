package ui_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// A stream's lines join it as they start: on a terminal a line spins only
// once it is added, prints whole when it ends, and the lines still running
// stay a block below the ended ones.
func TestStreamLinesJoinAsTheyStart(t *testing.T) {
	now := time.Unix(0, 0)
	var s screen
	r := ui.NewForTest(&s, false, true, func() time.Time { return now })
	stream := ui.Stream(r)
	a := stream.Add("check reach home-a")
	if got := strings.Join(s.rows, "\n"); !strings.Contains(got, "check reach home-a") || strings.Contains(got, "home-b") {
		t.Fatalf("only the added line spins:\n%s", got)
	}
	b := stream.Add("check reach home-b")
	b.Explain("Start the host or fix the route to it.")
	b.End(ui.Failed, "ssh to ubuntu@home-b did not answer")
	a.End(ui.OK, "ubuntu@home-a answers")
	c := stream.Add("check mesh home-a")
	c.End(ui.Warned, "could not read psns-f2a9")
	stream.Close()

	want := []string{
		"  ✗ check reach home-b: ssh to ubuntu@home-b did not answer",
		"       Start the host or fix the route to it.",
		"  ✓ check reach home-a: ubuntu@home-a answers",
		"  ! check mesh home-a: could not read psns-f2a9",
	}
	got := strings.Join(s.rows, "\n")
	if strings.TrimRight(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("screen:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// Off a terminal each line prints whole when it ends, with nothing drawn
// while it runs.
func TestStreamPlainPrintsEachLineWhenItEnds(t *testing.T) {
	var out bytes.Buffer
	r := ui.NewForTest(&out, false, false, func() time.Time { return time.Unix(0, 0) })
	stream := ui.Stream(r)
	a := stream.Add("check reach home-a")
	b := stream.Add("check reach home-b")
	if out.Len() != 0 {
		t.Fatalf("a running line drew something: %q", out.String())
	}
	b.End(ui.OK, "answers")
	a.End(ui.OK, "answers")
	stream.Close()
	want := "  ok   check reach home-b: answers\n  ok   check reach home-a: answers\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// A reporter that draws no group reports each line as a step opened and
// ended at once, its advice after it.
func TestStreamOnARecorderIsAStepPerLine(t *testing.T) {
	rec := &ui.Recorder{}
	stream := ui.Stream(rec)
	a := stream.Add("check etcd")
	b := stream.Add("check reach home-b")
	b.Explain("Start the host.")
	b.End(ui.Failed, "did not answer")
	a.End(ui.OK, "quorum")
	stream.Close()
	got := rec.Lines()
	want := "step: check reach home-b \nfail: check reach home-b did not answer\nexplain: Start the host. \nstep: check etcd \ndone: check etcd quorum\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A stream that empties and fills again keeps drawing: the block comes back
// below the lines printed while it was empty.
func TestStreamRefillsAfterEmptying(t *testing.T) {
	now := time.Unix(0, 0)
	var s screen
	r := ui.NewForTest(&s, false, true, func() time.Time { return now })
	stream := ui.Stream(r)
	stream.Add("check reach vm").End(ui.OK, "answers")
	c := stream.Add("check clocks vm")
	if !strings.Contains(s.rows[len(s.rows)-1]+strings.Join(s.rows, "\n"), "check clocks vm") {
		t.Fatalf("the second line is not drawn:\n%s", strings.Join(s.rows, "\n"))
	}
	c.End(ui.OK, "0.01 s behind")
	stream.Close()
	got := strings.TrimRight(strings.Join(s.rows, "\n"), "\n")
	want := "  ✓ check reach vm: answers\n  ✓ check clocks vm: 0.01 s behind"
	if got != want {
		t.Errorf("screen:\n%s\nwant:\n%s", got, want)
	}
}

// Lines added and ended from several goroutines print whole, never mid line,
// with a real spinner running under -race.
func TestStreamConcurrentLinesPrintWhole(t *testing.T) {
	var mu sync.Mutex
	var out lockedBuffer
	out.mu = &mu
	r := ui.NewForTestTick(&out, false, false, time.Now, time.Millisecond)
	stream := ui.Stream(r)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := stream.Add("check site-" + string(rune('a'+i)))
			l.End(ui.OK, "fine")
		}()
	}
	wg.Wait()
	stream.Close()
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 20 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "  ok   check site-") || !strings.HasSuffix(l, ": fine") {
			t.Errorf("line not whole: %q", l)
		}
	}
}

type lockedBuffer struct {
	mu *sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
