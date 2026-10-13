package ui_test

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// screen is a terminal for a test: it applies what the reporter writes, the
// carriage returns, newlines, erases and cursor moves a group draws with, and
// keeps the rows as they would be seen. Colour is dropped.
type screen struct {
	rows     []string
	row, col int
}

var csi = regexp.MustCompile(`^\x1b\[([0-9]*)([A-Za-z])`)

func (s *screen) Write(p []byte) (int, error) {
	in := string(p)
	for len(in) > 0 {
		if m := csi.FindStringSubmatch(in); m != nil {
			n, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "A":
				if n == 0 {
					n = 1
				}
				s.row -= n
				if s.row < 0 {
					s.row = 0
				}
			case "K":
				s.cut(s.row, s.col)
			case "J":
				s.cut(s.row, s.col)
				s.rows = s.rows[:min(len(s.rows), s.row+1)]
			}
			in = in[len(m[0]):]
			continue
		}
		r := []rune(in)[0]
		in = in[len(string(r)):]
		switch r {
		case '\r':
			s.col = 0
		case '\n':
			s.row++
			s.col = 0
		default:
			s.put(r)
		}
	}
	return len(p), nil
}

func (s *screen) grow(row int) {
	for len(s.rows) <= row {
		s.rows = append(s.rows, "")
	}
}

func (s *screen) cut(row, col int) {
	s.grow(row)
	line := []rune(s.rows[row])
	if col < len(line) {
		s.rows[row] = string(line[:col])
	}
}

func (s *screen) put(r rune) {
	s.grow(s.row)
	line := []rune(s.rows[s.row])
	for len(line) < s.col {
		line = append(line, ' ')
	}
	if s.col < len(line) {
		line[s.col] = r
	} else {
		line = append(line, r)
	}
	s.rows[s.row] = string(line)
	s.col++
}

// lines is what the screen shows, without the empty row the cursor rests on.
func (s *screen) lines() []string {
	out := append([]string(nil), s.rows...)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func sameRows(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// On a terminal a group is a block of spinner lines, one per title, redrawn
// in place. A line that ends leaves the block for good with its mark, its
// details, its advice and its notes under it, and the rest keep spinning
// below it.
func TestAGroupRedrawsItsBlockInPlace(t *testing.T) {
	s := &screen{}
	r := ui.NewForTest(s, true, true, clock())
	r.Section("prepare")
	lines := ui.Group(r, "home-a", "home-b", "vm")
	ui.Frame(r)
	sameRows(t, s.lines(), []string{"prepare", "  ⠙ home-a: 3s", "  ⠙ home-b: 3s", "  ⠙ vm: 3s"})
	lines[1].Detail("installed docker")
	lines[1].Done("")
	ui.Frame(r)
	if got := s.lines(); len(got) != 5 || got[1] != "  ✓ home-b: 4.5s" || got[2] != "      installed docker" || !strings.HasPrefix(got[3], "  ⠹ home-a: ") || !strings.HasPrefix(got[4], "  ⠹ vm: ") {
		t.Errorf("after home-b:\n%s", strings.Join(got, "\n"))
	}
	lines[2].Explain("Remove it if not in use.")
	lines[2].Note("left for you", "do this")
	lines[2].Fail(errors.New("mesh subnet in use"))
	lines[0].Done("")
	sameRows(t, s.lines(), []string{
		"prepare",
		"  ✓ home-b: 4.5s",
		"      installed docker",
		"  ✗ vm: 9.0s",
		"       Remove it if not in use.",
		"  ! left for you",
		"       do this",
		"  ✓ home-a: 12.0s",
	})
	ended := strings.Join(s.lines(), "\n")
	ui.Frame(r)
	if strings.Join(s.lines(), "\n") != ended {
		t.Errorf("a frame drew after every line ended:\n%s", strings.Join(s.lines(), "\n"))
	}
	r.Result("done")
	if got := s.lines(); got[len(got)-1] != "done" || got[len(got)-2] != "" || len(got) != 10 {
		t.Errorf("after the result:\n%s", strings.Join(got, "\n"))
	}
}

// A line's progress shows on its own spinner line only.
func TestAGroupLineShowsItsProgress(t *testing.T) {
	s := &screen{}
	r := ui.NewForTest(s, false, true, clock())
	lines := ui.Group(r, "home-a", "home-b")
	lines[0].Progress("docker")
	ui.Frame(r)
	got := s.lines()
	if len(got) != 2 || !strings.HasPrefix(got[0], "  ⠙ home-a: docker") || strings.Contains(got[1], "docker") {
		t.Errorf("screen:\n%s", strings.Join(got, "\n"))
	}
	lines[0].Done("")
	lines[1].Done("")
}

// A hold clears the block so a prompt can be read, and a line that ends
// during it is printed only once the hold is resumed, so it cannot land on
// the prompt.
func TestAHeldGroupPrintsNothingUntilResumed(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, true, clock())
	lines := ui.Group(r, "home-a", "home-b")
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
	if got := s.lines(); len(got) != 2 || !strings.HasPrefix(got[0], "  ✓ home-a") || !strings.HasPrefix(got[1], "  ⠙ home-b") {
		t.Errorf("after the resume:\n%s", strings.Join(got, "\n"))
	}
	lines[1].Done("")
}

// Plain output prints each line once, whole, when it ends, in the order the
// lines end, each with its details right under it.
func TestAPlainGroupPrintsWholeLinesAsTheyEnd(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, true, false, clock())
	lines := ui.Group(r, "home-a", "home-b")
	lines[0].Detail("a detail")
	lines[1].Detail("b detail")
	lines[0].Progress("docker")
	if b.String() != "" {
		t.Fatalf("printed before a line ended: %q", b.String())
	}
	lines[1].End(ui.Pending, "docker, firewall")
	lines[0].Explain("why")
	lines[0].Fail(errors.New("boom"))
	want := "  todo home-b: docker, firewall\n      b detail\n  FAIL home-a: 1.5s\n      a detail\n       why\n"
	if b.String() != want {
		t.Errorf("got %q\nwant %q", b.String(), want)
	}
}

// Without -v a line's details are not printed.
func TestAGroupHidesDetailsUnlessVerbose(t *testing.T) {
	var b strings.Builder
	r := ui.NewForTest(&b, false, false, clock())
	l := ui.Group(r, "home-a")[0]
	l.Detail("hidden")
	l.End(ui.OK, "")
	if b.String() != "  ok   home-a\n" {
		t.Errorf("got %q", b.String())
	}
}

// Lines end from goroutines of their own while the spinner draws: nothing
// may race, every line ends whole, and nothing draws after the last.
func TestAGroupIsSafeAcrossGoroutines(t *testing.T) {
	var b syncBuf
	r := ui.NewForTestTick(&b, true, true, time.Now, time.Millisecond)
	titles := []string{"a", "b", "c", "d", "e", "f"}
	lines := ui.Group(r, titles...)
	var wg sync.WaitGroup
	for i, l := range lines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				l.Progress(strconv.Itoa(k))
				l.Detail("%s detail %d", titles[i], k)
			}
			resume := ui.Hold(r)
			resume()
			l.Done("")
		}()
	}
	wg.Wait()
	assertSilent(t, &b, "after every line ended")
	s := &screen{}
	s.Write([]byte(b.String()))
	got := s.lines()
	if len(got) != len(titles)*51 {
		t.Fatalf("%d rows:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i := 0; i < len(got); i += 51 {
		name := strings.TrimPrefix(got[i], "  ✓ ")
		for k := 0; k < 50; k++ {
			if want := "      " + name + " detail " + strconv.Itoa(k); got[i+1+k] != want {
				t.Fatalf("row %d is %q, want %q", i+1+k, got[i+1+k], want)
			}
		}
	}
}

// A reporter that draws no group of its own, such as a Recorder, still gets
// each line whole when it ends: its step, its details, its end, then its
// advice and notes.
func TestAGroupOnARecorderReportsEachLineWhole(t *testing.T) {
	rec := &ui.Recorder{}
	lines := ui.Group(rec, "home-a", "home-b")
	lines[0].Detail("a detail")
	lines[1].Note("left", "do")
	lines[1].Done("")
	lines[0].Explain("why")
	lines[0].Fail(errors.New("boom"))
	want := "step: home-b \ndone: home-b \nnote: left do\nstep: home-a \ndetail: a detail home-a\nfail: home-a boom\nexplain: why \n"
	if rec.Lines() != want {
		t.Errorf("got:\n%s\nwant:\n%s", rec.Lines(), want)
	}
}
