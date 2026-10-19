package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// inSection is the events between section's header and the next header.
func inSection(rec *ui.Recorder, section string) []ui.Event {
	var out []ui.Event
	in := false
	for _, e := range rec.Events {
		if e.Kind == "section" {
			in = e.Text == section
			continue
		}
		if in {
			out = append(out, e)
		}
	}
	return out
}

// lines is events as "kind text: extra", leaving out details.
func lines(events []ui.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == "detail" {
			continue
		}
		line := e.Kind + " " + e.Text
		if e.Extra != "" {
			line += ": " + e.Extra
		}
		out = append(out, line)
	}
	return out
}

// sections is the section headers rec saw, in order.
func sections(rec *ui.Recorder) []string {
	var out []string
	for _, e := range rec.Events {
		if e.Kind == "section" {
			out = append(out, e.Text)
		}
	}
	return out
}

// The dry run's plan is three sections, prepare, dns and install, and none
// of the order's own stages is a heading. dns comes before install: the
// mesh's endpoint names must resolve before any site's wg-quick starts.
func TestConvergeDryRunShowsPrepareDNSAndInstall(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, map[string]any{"site prepare --site home-b": "docker, firewall"})
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	got := sections(rec)
	for _, gone := range []string{"hosts", "founding", "joining", "other sites", "storage", "pass two"} {
		if slices.Contains(got, gone) {
			t.Errorf("a %s section: %v", gone, got)
		}
	}
	p, i, d := slices.Index(got, "prepare"), slices.Index(got, "install"), slices.Index(got, "dns")
	if p < 0 || !(p < d && d < i) {
		t.Errorf("sections %v", got)
	}
	sameLines(t, lines(inSection(rec, "prepare")), []string{
		"step home-a", "done home-a",
		"step home-b", "pending home-b: docker, firewall",
		"step vm", "done vm",
		"step watch", "done watch",
	})
	sameLines(t, lines(inSection(rec, "dns"))[:2], []string{"step dns init", "done dns init"})
}

// installSites is the install lines' titles for the fixture: every site in
// the order the run first applies it, the witness first and the monitor
// last, then storage.
var installSites = []string{"vm", "home-a", "home-b", "watch", "storage"}

// each is a step line and its end for every install line, the end being
// end of the title.
func each(end func(title string) string) []string {
	var out []string
	for _, s := range installSites {
		out = append(out, "step "+s, end(s))
	}
	return out
}

// The install section is a line per site, and one for storage, each marked
// with what that line's inner steps found together: up to date, work to do,
// waiting on prepare or init, or a refusal, which says why below its line.
func TestConvergeDryRunInstallIsALinePerSite(t *testing.T) {
	refusal := &ui.Problem{Hint: "vm's plan would remove a volume", Explain: "Mount it, then apply."}
	// Work on vm leaves every line after it pending: each waits on vm.
	pendingAfterVM := func(rest map[string]string) func(string) string {
		return func(s string) string {
			if line, ok := rest[s]; ok {
				return line
			}
			return "pending " + s
		}
	}
	for _, c := range []struct {
		name  string
		init  bool
		found map[string]any
		want  []string
	}{
		{"up to date", false, nil, each(func(s string) string { return "done " + s })},
		{"work to do", false, map[string]any{"deployment apply --site vm": "5 changes"}, each(pendingAfterVM(map[string]string{"vm": "pending vm: 5 changes"}))},
		{"after prepare", false, map[string]any{"site prepare --site home-b": "docker", "deployment apply --site vm": "5 changes"}, each(pendingAfterVM(map[string]string{
			"vm": "pending vm: 5 changes", "home-b": "waiting home-b: after prepare", "storage": "waiting storage: after prepare",
		}))},
		{"after init", true, nil, each(func(s string) string { return "waiting " + s + ": after init" })},
		{"refusal", false, map[string]any{"deployment apply --site vm": refusal}, slices.Insert(each(pendingAfterVM(map[string]string{
			"vm": "fail vm: vm's plan would remove a volume",
		})), 2, "explain Mount it, then apply.")},
		{"refusal while waiting", false, map[string]any{"site prepare --site home-b": "docker", "deployment apply --site vm": refusal}, slices.Insert(each(pendingAfterVM(map[string]string{
			"vm": "fail vm: vm's plan would remove a volume", "home-b": "waiting home-b: after prepare", "storage": "waiting storage: after prepare",
		})), 2, "explain Mount it, then apply.")},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeConverge(t, nil)
			fakeChecks(t, c.found)
			if c.init {
				saved := convergeRead
				convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
					cfg, err := config.Load(fixtureConfig())
					return cfg, convergeState{NeedsInit: true, InitWhy: "no secrets file yet"}, err
				}
				t.Cleanup(func() { convergeRead = saved })
			}
			rec := recordConverge(t)
			if err := converge(t); err != nil {
				t.Fatal(err)
			}
			got := slices.DeleteFunc(lines(inSection(rec, "install")), func(l string) bool { return strings.HasPrefix(l, "result ") })
			sameLines(t, got, c.want)
		})
	}
}

// --execute draws each prepare line as its step runs, then dns, then a line
// per site and one for storage, each ending done once its last inner step
// has. The steps run in the plan's order all the same.
func TestConvergeRunInstallIsALinePerSite(t *testing.T) {
	ran := fakeConverge(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, *ran, commands(blankPlan()))
	sameLines(t, lines(inSection(rec, "prepare")), []string{
		"step home-a", "done home-a", "step home-b", "done home-b", "step vm", "done vm", "step watch", "done watch",
	})
	sameLines(t, lines(inSection(rec, "install")), each(func(s string) string { return "done " + s }))
	if i, d := slices.Index(sections(rec), "install"), slices.Index(sections(rec), "dns"); d < 0 || i < d {
		t.Errorf("sections %v", sections(rec))
	}
	if d, a := slices.Index(*ran, "dns init"), slices.Index(*ran, "deployment apply --site vm"); d < 0 || a < d {
		t.Errorf("dns init ran after the first install step: %v", *ran)
	}
}

// A DNS conflict stops the run before install: no site's wg-quick starts
// on names that may not resolve.
func TestConvergeRunDNSConflictStopsBeforeInstall(t *testing.T) {
	conflict := fmt.Errorf("dns: 1 conflicting record(s), listed above: %w", dns.ErrConflict)
	ran := fakeConverge(t, map[string]error{"dns init": conflict})
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if !errors.Is(err, dns.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(sections(rec), "install") || (*ran)[len(*ran)-1] != "dns init" {
		t.Errorf("ran on past the conflict: %v\n%v", *ran, sections(rec))
	}
}

// A failed inner step fails its site's line, and the run stops with the
// step's own problem and how to resume. A line it had visited and one it
// had not reached are left waiting.
func TestConvergeRunInstallFailureFailsItsSite(t *testing.T) {
	ran := fakeConverge(t, map[string]error{"deployment apply --site home-a": errors.New("home-a: boom")})
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "deployment apply --site home-a") || !strings.Contains(err.Error(), "home-a: boom") || !strings.Contains(err.Error(), "Run paisans deployment apply --execute again to resume") {
		t.Fatalf("err = %v", err)
	}
	sameLines(t, lines(inSection(rec, "install")), []string{
		"step vm", "waiting vm: paused until home-a, home-b, watch and storage are done",
		"step home-a", "fail home-a: home-a: boom",
		"step home-b", "waiting home-b",
		"step watch", "waiting watch",
		"step storage", "waiting storage",
	})
	if (*ran)[len(*ran)-1] != "deployment apply --site home-a" {
		t.Errorf("ran on past the failure: %v", *ran)
	}
}

// screen is a terminal for a test: it applies the carriage returns,
// newlines, erases and cursor moves a reporter draws with, and keeps the
// rows as they would be seen. Colour is dropped.
type screen struct {
	mu       sync.Mutex
	rows     []string
	row, col int
}

var csiSeq = regexp.MustCompile(`^\x1b\[([0-9]*)([A-Za-z])`)

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := string(p)
	for len(in) > 0 {
		if m := csiSeq.FindStringSubmatch(in); m != nil {
			n, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "A":
				s.row = max(s.row-max(n, 1), 0)
			case "K":
				s.cut()
			case "J":
				s.cut()
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
			s.grow()
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
	}
	return len(p), nil
}

func (s *screen) grow() {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, "")
	}
}

func (s *screen) cut() {
	s.grow()
	if line := []rune(s.rows[s.row]); s.col < len(line) {
		s.rows[s.row] = string(line[:s.col])
	}
}

// section is the rows under title's header, up to the blank row after them.
func (s *screen) section(title string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := slices.Index(s.rows, title)
	if start < 0 {
		return nil
	}
	var out []string
	for _, row := range s.rows[start+1:] {
		if row == "" {
			break
		}
		out = append(out, row)
	}
	return out
}

// onATerminal draws the run on a screen, by a clock that moves only while a
// step runs, ten seconds for each, and calls at with each step's command
// while it runs.
func onATerminal(t *testing.T, verbose bool, at func(title string)) *screen {
	t.Helper()
	s := &screen{}
	var mu sync.Mutex
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	saved, savedNow := reporterOverride, convergeNow
	reporterOverride = ui.NewTerminalForTest(s, verbose, clock)
	convergeNow = clock
	savedRun := convergeRun
	convergeRun = func(args []string) error {
		mu.Lock()
		now = now.Add(10 * time.Second)
		mu.Unlock()
		title := strings.Join(args, " ")
		title, _, _ = strings.Cut(title, " --config")
		at(title)
		return savedRun(args)
	}
	t.Cleanup(func() { reporterOverride, convergeNow, convergeRun = saved, savedNow, savedRun })
	return s
}

// On a terminal each install line keeps its row. A site visited twice, its
// founding stopped and resumed in pass two, waits between its visits with
// the time it has taken and why, and ends once, with the time both visits
// took together.
func TestConvergeInstallRevisitsASiteOnATerminal(t *testing.T) {
	fakeConverge(t, map[string]error{"deployment apply --site home-a": foundingWait("home-a", "home-b")})
	var atStorage, atPassTwo []string
	visits := 0
	var s *screen
	s = onATerminal(t, false, func(title string) {
		switch title {
		case "storage add":
			reporterOverride.Step("apply the layout")
			atStorage = s.section("install")
		case "deployment apply --site home-a":
			if visits++; visits == 2 {
				reporterOverride.Step("wait for Patroni primary")
				atPassTwo = s.section("install")
			}
		}
	})
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, atStorage, []string{
		"  · vm: 10.0s, paused until home-a, home-b, watch and storage are done",
		"  · home-a: 10.0s, paused until home-b runs etcd",
		"  · home-b: 10.0s, paused until watch, storage and home-a are done",
		"  · watch: 10.0s, paused until storage, home-a, home-b and vm are done",
		"  ⠋ storage: 10s (apply the layout)",
	})
	sameLines(t, atPassTwo, []string{
		"  · vm: 10.0s, paused until home-a, home-b, watch and storage are done",
		"  ⠋ home-a: 20s (wait for Patroni primary)",
		"  · home-b: 10.0s, paused until watch, storage and home-a are done",
		"  · watch: 10.0s, paused until storage, home-a, home-b and vm are done",
		"  ✓ storage: 10.0s",
	})
	sameLines(t, s.section("install"), []string{
		"  ✓ vm: 20.0s",
		"  ✓ home-a: 20.0s",
		"  ✓ home-b: 20.0s",
		"  ✓ watch: 20.0s",
		"  ✓ storage: 10.0s",
	})
}

// On a terminal a failure marks its site's row, and the rows it did not
// reach, or will not come back to, are left as they stand.
func TestConvergeInstallFailureOnATerminal(t *testing.T) {
	fakeConverge(t, map[string]error{"deployment apply --site home-b": errors.New("home-b: boom")})
	s := onATerminal(t, false, func(string) {})
	if err := converge(t, "--execute"); err == nil {
		t.Fatal("no error")
	}
	sameLines(t, s.section("install"), []string{
		"  · vm: 10.0s, paused until home-a, home-b, watch and storage are done",
		"  · home-a: 10.0s, paused until home-b, watch and storage are done",
		"  ✗ home-b: 10.0s",
		"  · watch",
		"  · storage",
	})
}

// A replica's stop for the app databases pauses its line until the leader's
// site has created them, and a founding stop until every member it waits on
// runs etcd.
func TestAnInstallLinePausesUntilWhatItWaitsOn(t *testing.T) {
	fakeConverge(t, map[string]error{"deployment apply --site home-a": foundingWait("home-a", "home-b", "vm")})
	waitsFor(t, "home-b", "home-a", 1)
	var atStorage []string
	var s *screen
	s = onATerminal(t, false, func(title string) {
		if title == "storage add" {
			atStorage = s.section("install")
		}
	})
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, atStorage, []string{
		"  · vm: 10.0s, paused until home-a, home-b, watch and storage are done",
		"  · home-a: 10.0s, paused until home-b and vm run etcd",
		"  · home-b: 10.0s, paused until home-a creates its databases",
		"  · watch: 10.0s, paused until storage, home-a, home-b and vm are done",
		"  ⠋ storage",
	})
}

// A pause names places as a sentence lists them, and past four only the
// first three and how many more.
func TestAndList(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"vm"}, "vm"},
		{[]string{"vm", "storage"}, "vm and storage"},
		{[]string{"vm", "home-a", "storage"}, "vm, home-a and storage"},
		{[]string{"vm", "home-a", "home-b", "storage"}, "vm, home-a, home-b and storage"},
		{[]string{"vm", "home-a", "home-b", "watch", "storage"}, "vm, home-a, home-b and 2 more"},
	} {
		if got := andList(c.names); got != c.want {
			t.Errorf("andList(%q) = %q, want %q", c.names, got, c.want)
		}
	}
}

// A prepare step that fails fails its own line, naming nothing more.
func TestConvergeRunPrepareFailureFailsItsLine(t *testing.T) {
	fakeConverge(t, map[string]error{"site prepare --site vm": errors.New("vm: no apt")})
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err == nil || !strings.Contains(err.Error(), "site prepare --site vm") {
		t.Fatalf("err = %v", err)
	}
	if !rec.Has("fail", "vm") || slices.Contains(sections(rec), "install") {
		t.Errorf("events:\n%s", rec.Lines())
	}
}

// What an inner command reports while it runs does not reach the run's
// output: a note shows after its site's install line, at every verbosity,
// and a prompt holds the install lines. With -v, the command's own output is
// kept as details of its site's line, under its inner step.
func TestConvergeRunIsQuietAndKeepsDiagnosticsForVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, nil)
		saved := convergeRun
		convergeRun = func(args []string) error {
			if isApply(args) && args[3] == "vm" {
				r := reporterOverride
				r.Section("vm (ubuntu@192.0.2.10)")
				r.Step("write 8 files").Done("")
				r.Note("secrets: smtp.password is owed", "paisans secrets set smtp.password < value")
				holdOutput()()
			}
			return saved(args)
		}
		t.Cleanup(func() { convergeRun = saved })
		h := recordHolds(t)
		var args []string
		if verbose {
			args = append(args, "-v")
		}
		if err := converge(t, append(args, "--execute")...); err != nil {
			t.Fatal(err)
		}
		if h.Has("section", "vm (ubuntu") || h.Has("step", "write 8 files") {
			t.Errorf("verbose %t: the inner command's output reached the run's:\n%s", verbose, h.Lines())
		}
		// vm's line prints once its last inner step, in pass two, has run.
		install := h.Index("section", "install")
		exact := func(kind, text string) int {
			return slices.IndexFunc(h.Events[install:], func(e ui.Event) bool { return e.Kind == kind && e.Text == text })
		}
		step, done := exact("step", "vm"), exact("done", "vm")
		hold, note := h.Index("hold", "")-install, h.Index("note", "smtp.password is owed")-install
		if !(0 < hold && hold < step && step < done && done < note) {
			t.Errorf("verbose %t: hold %d, step %d, done %d, note %d:\n%s", verbose, hold, step, done, note, h.Lines())
		}
		inner := slices.IndexFunc(h.Events, func(e ui.Event) bool {
			return e.Kind == "detail" && e.Extra == "vm" && strings.HasPrefix(e.Text, "✓ deployment apply --site vm")
		})
		output := slices.IndexFunc(h.Events, func(e ui.Event) bool {
			return e.Kind == "detail" && e.Extra == "vm" && strings.Contains(e.Text, "write 8 files")
		})
		if inner < 0 || (output >= 0) != verbose || (verbose && output < inner) {
			t.Errorf("verbose %t: inner %d, output %d:\n%s", verbose, inner, output, h.Lines())
		}
	}
}

// An apply a step leaves owed on another site is not noted under converge,
// whose pass two applies every site again: it would read as a second line
// for that site. The step's other notes still show after its line.
func TestConvergeLeavesOutAppliesItsPassTwoMakes(t *testing.T) {
	owed := "home-a: run `paisans deployment apply --site home-a` when acceptable. Its apps are out of date in 1 file, /srv/paisans/f2a9/talk/.env: it differs from the render, and the apply brings it up to date and recreates its app"
	kept := "smoke test skipped: no app serves objects publicly. There is no bucket to put a probe through"
	report := func() {
		r := reporterOverride
		reportRemains(r, []string{kept})
		reportOwedApplies(r, []string{owed})
	}
	fakeConverge(t, nil)
	saved := convergeRun
	convergeRun = func(args []string) error {
		if args[0] == "storage" {
			report()
		}
		return saved(args)
	}
	t.Cleanup(func() { convergeRun = saved })
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if rec.Has("note", "home-a: run") || !rec.Has("note", "smoke test skipped") {
		t.Errorf("--execute:\n%s", rec.Lines())
	}

	// A dry run's check runs the step under the same reporter.
	dry := &ui.Recorder{}
	_, reports, err := checkStep(dry, []string{"storage", "add"}, func([]string) error { report(); dryRunFound("", nil); return nil })
	if err != nil || len(reports) != 1 || reports[0].Text != "smoke test skipped: no app serves objects publicly" {
		t.Errorf("dry run: %v, %+v", err, reports)
	}

	// Run by hand, the command notes it.
	alone := &ui.Recorder{}
	reportOwedApplies(alone, []string{owed})
	if !alone.Has("note", "home-a: run `paisans deployment apply --site home-a` when acceptable") {
		t.Errorf("by hand:\n%s", alone.Lines())
	}
}

// As printed without a terminal: a line per site and one for storage, and
// -v lists each line's inner steps under it, each with its own mark and
// result.
func TestConvergeInstallAsPrinted(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, nil)
		fakeChecks(t, map[string]any{"deployment apply --site vm": "5 changes"})
		var b strings.Builder
		saved := reporterOverride
		reporterOverride = ui.NewPlain(&b, verbose)
		t.Cleanup(func() { reporterOverride = saved })
		args := []string{}
		if verbose {
			args = append(args, "-v")
		}
		if err := converge(t, args...); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		quiet := "\ninstall\n  todo vm: 5 changes\n  todo home-a\n  todo home-b\n  todo watch\n  todo storage\n"
		listed := "\ninstall\n  todo vm: 5 changes\n" +
			"      ○ deployment apply --site vm: 5 changes\n" +
			"      · deployment apply --site vm: after the earlier deployment apply --site vm\n" +
			"  todo home-a\n" +
			"      · deployment apply --site home-a: after deployment apply --site vm\n" +
			"      · deployment apply --site home-a: after the earlier deployment apply --site home-a\n" +
			"  todo home-b\n"
		if strings.Contains(out, quiet) == verbose || strings.Contains(out, listed) != verbose || strings.Contains(out, "✓ deployment apply --site watch") != verbose {
			t.Errorf("verbose %t:\n%s", verbose, out)
		}
	}
}

// --execute without a terminal prints each install line once, when it has
// ended, in the order the run first reached it; a founding stop is no
// failure, and with -v it is listed under its site's line with its reason.
func TestConvergeRunInstallAsPrinted(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, map[string]error{"deployment apply --site home-a": foundingWait("home-a", "home-b")})
		var b strings.Builder
		saved := reporterOverride
		reporterOverride = ui.NewPlain(&b, verbose)
		t.Cleanup(func() { reporterOverride = saved })
		args := []string{"--execute"}
		if verbose {
			args = append(args, "-v")
		}
		if err := converge(t, args...); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		quiet := "\ninstall\n  ok   vm\n  ok   home-a\n  ok   home-b\n  ok   watch\n  ok   storage\n\npocket id\n"
		listed := "  ok   home-a\n" +
			"      ✓ deployment apply --site home-a: paused until home-b runs etcd\n" +
			"        home-a is waiting on home-b to start etcd.\n" +
			"      ✓ deployment apply --site home-a\n" +
			"  ok   home-b\n"
		if strings.Contains(out, quiet) == verbose || strings.Contains(out, listed) != verbose {
			t.Errorf("verbose %t:\n%s", verbose, out)
		}
	}
}

// On a terminal each prepare line, and each install line, shows the inner
// step its command has open, in parentheses after the elapsed time: the
// innermost, the enclosing one again once that ends. The finished lines do
// not.
func TestConvergeLinesShowTheirInnerStep(t *testing.T) {
	fakeConverge(t, nil)
	var b strings.Builder
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	saved := reporterOverride
	reporterOverride = ui.NewTerminalForTest(&lockedWriter{w: &b}, false, func() time.Time { at = at.Add(1500 * time.Millisecond); return at })
	t.Cleanup(func() { reporterOverride = saved })
	savedRun := convergeRun
	convergeRun = func(args []string) error {
		r := reporterOverride
		switch {
		case isPrepare(args) && siteOf(args) == "home-b":
			outer := r.Step("install packages: wireguard-tools, ufw")
			r.Step("start docker").Done("")
			outer.Done("")
		case isApply(args) && siteOf(args) == "home-b":
			r.Step("start talk")
			r.Step("wait for Patroni primary").Done("")
		}
		return savedRun(args)
	}
	t.Cleanup(func() { convergeRun = savedRun })
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`  ⠋ home-b: \d+s \(install packages: wireguard-tools, ufw\)`,
		`  ⠋ home-b: \d+s \(start docker\)`,
		`  ⠋ home-b: \d+s \(start talk\)`,
		`  ⠋ home-b: \d+s \(wait for Patroni primary\)`,
	} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("no %q in:\n%q", want, out)
		}
	}
	// The install line goes back to the step enclosing the one that ended,
	// and no other site's line shows home-b's steps.
	if !regexp.MustCompile(`(?s)\(wait for Patroni primary\).*⠋ home-b: \d+s \(start talk\)`).MatchString(out) {
		t.Errorf("the enclosing step was not shown again:\n%q", out)
	}
	if regexp.MustCompile(`(vm|home-a|watch|storage)[^\n\r]*\((start talk|wait for Patroni primary)\)`).MatchString(out) {
		t.Errorf("another site shows home-b's step:\n%q", out)
	}
	for _, ended := range regexp.MustCompile(`✓\x1b\[0m [^\n]*`).FindAllString(out, -1) {
		// The deployment section's paisans line names its build's commit
		// in parentheses, which is no step.
		if strings.Contains(ended, "(") && !strings.Contains(ended, " paisans: ") {
			t.Errorf("a finished line shows an inner step: %q", ended)
		}
	}
}

// A failed inner step lists its error under it, as a founding stop lists its
// reason, so -v shows why the line failed where it failed.
func TestAFailedInnerStepShowsItsError(t *testing.T) {
	fakeConverge(t, map[string]error{"deployment apply --site home-a": &ui.Problem{Hint: "the configuration changed after the run published it", Explain: "Nothing was changed on home-a by this step."}})
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err == nil {
		t.Fatal("no error")
	}
	i := slices.IndexFunc(rec.Events, func(e ui.Event) bool {
		return e.Kind == "detail" && e.Extra == "home-a" && strings.HasPrefix(e.Text, "✗ deployment apply --site home-a")
	})
	if i < 0 || rec.Events[i+1].Text != "  the configuration changed after the run published it.\n  Nothing was changed on home-a by this step." {
		t.Errorf("the failure's error is not under its inner step:\n%s", rec.Lines())
	}
}
