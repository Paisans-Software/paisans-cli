package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// fixtureSites is the install line's title for the fixture: every site in
// the order the run first applies it, the witness first and the monitor last.
const fixtureSites = "vm, home-a, home-b, watch"

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
	fakeChecks(t, map[string]any{"host prepare --site home-b": "docker, firewall"})
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

// The install line is one, for every site, and its mark is what its inner
// steps found: up to date, work to do, waiting on prepare or init, or a
// refusal, which names its site and says why below the line.
func TestConvergeDryRunInstallIsOneLine(t *testing.T) {
	refusal := &ui.Problem{Hint: "vm's plan would remove a volume", Explain: "Mount it, then apply."}
	for _, c := range []struct {
		name  string
		init  bool
		found map[string]any
		want  []string
	}{
		{"up to date", false, nil, []string{"step " + fixtureSites, "done " + fixtureSites}},
		{"work to do", false, map[string]any{"apply --site vm": "5 changes"}, []string{"step " + fixtureSites, "pending " + fixtureSites}},
		{"after prepare", false, map[string]any{"host prepare --site home-b": "docker", "apply --site vm": "5 changes"}, []string{"step " + fixtureSites, "waiting " + fixtureSites + ": after prepare"}},
		{"after init", true, nil, []string{"step " + fixtureSites, "waiting " + fixtureSites + ": after init"}},
		{"refusal", false, map[string]any{"apply --site vm": refusal}, []string{"step " + fixtureSites, "fail " + fixtureSites + ": vm", "explain vm's plan would remove a volume.\nMount it, then apply."}},
		{"refusal while waiting", false, map[string]any{"host prepare --site home-b": "docker", "apply --site vm": refusal}, []string{"step " + fixtureSites, "fail " + fixtureSites + ": vm", "explain vm's plan would remove a volume.\nMount it, then apply."}},
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
			got := slices.DeleteFunc(lines(inSection(rec, "install")), func(l string) bool {
				return strings.HasPrefix(l, "progress ") || strings.HasPrefix(l, "result ")
			})
			sameLines(t, got, c.want)
		})
	}
}

// While the install line checks its inner steps, its spinner names the site
// it has reached, once each time that changes.
func TestConvergeDryRunInstallNamesTheSiteItChecks(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range inSection(rec, "install") {
		if e.Kind == "progress" {
			got = append(got, e.Text)
		}
	}
	sameLines(t, got, []string{"vm", "home-a", "home-b", "watch", "storage", "home-a", "home-b", "vm", "watch"})
}

// --execute draws each prepare line as its step runs, then dns, then one
// install line for every inner step, whose spinner names the site it has
// reached, ending done.
func TestConvergeRunInstallIsOneLine(t *testing.T) {
	ran := fakeConverge(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, *ran, commands(blankPlan()))
	sameLines(t, lines(inSection(rec, "prepare")), []string{
		"step home-a", "done home-a", "step home-b", "done home-b", "step vm", "done vm", "step watch", "done watch",
	})
	sameLines(t, lines(inSection(rec, "install")), []string{
		"step " + fixtureSites,
		"progress vm: " + fixtureSites, "progress home-a: " + fixtureSites, "progress home-b: " + fixtureSites,
		"progress watch: " + fixtureSites, "progress storage: " + fixtureSites,
		"progress home-a: " + fixtureSites, "progress home-b: " + fixtureSites, "progress vm: " + fixtureSites,
		"progress watch: " + fixtureSites,
		"done " + fixtureSites,
	})
	if i, d := slices.Index(sections(rec), "install"), slices.Index(sections(rec), "dns"); d < 0 || i < d {
		t.Errorf("sections %v", sections(rec))
	}
	if d, a := slices.Index(*ran, "dns init"), slices.Index(*ran, "apply --site vm"); d < 0 || a < d {
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

// A failed inner step fails the install line naming its site, and the run
// stops with the step's own problem and how to resume.
func TestConvergeRunInstallFailureNamesTheSite(t *testing.T) {
	ran := fakeConverge(t, map[string]error{"apply --site home-a": errors.New("home-a: boom")})
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "apply --site home-a") || !strings.Contains(err.Error(), "home-a: boom") || !strings.Contains(err.Error(), "Run paisans apply --execute again to resume") {
		t.Fatalf("err = %v", err)
	}
	if !rec.Has("fail", fixtureSites) || rec.Events[rec.Index("fail", fixtureSites)].Extra != "home-a" {
		t.Errorf("install line:\n%s", rec.Lines())
	}
	if (*ran)[len(*ran)-1] != "apply --site home-a" {
		t.Errorf("ran on past the failure: %v", *ran)
	}
}

// A prepare step that fails fails its own line, naming nothing more.
func TestConvergeRunPrepareFailureFailsItsLine(t *testing.T) {
	fakeConverge(t, map[string]error{"host prepare --site vm": errors.New("vm: no apt")})
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err == nil || !strings.Contains(err.Error(), "host prepare --site vm") {
		t.Fatalf("err = %v", err)
	}
	if !rec.Has("fail", "vm") || slices.Contains(sections(rec), "install") {
		t.Errorf("events:\n%s", rec.Lines())
	}
}

// What an inner command reports while it runs does not reach the run's
// output: a note shows after the install line, at every verbosity, and a
// prompt holds the install line's spinner. With -v, the command's own output
// is kept as details of the line, under its inner step.
func TestConvergeRunIsQuietAndKeepsDiagnosticsForVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, nil)
		saved := convergeRun
		convergeRun = func(args []string) error {
			if args[0] == "apply" && args[2] == "vm" {
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
		step, done := h.Index("step", fixtureSites), h.Index("done", fixtureSites)
		hold, note := h.Index("hold", ""), h.Index("note", "smtp.password is owed")
		if !(step < hold && hold < done && done < note) {
			t.Errorf("verbose %t: step %d, hold %d, done %d, note %d:\n%s", verbose, step, hold, done, note, h.Lines())
		}
		inner := slices.IndexFunc(h.Events, func(e ui.Event) bool {
			return e.Kind == "detail" && e.Extra == fixtureSites && strings.HasPrefix(e.Text, "✓ apply --site vm")
		})
		output := slices.IndexFunc(h.Events, func(e ui.Event) bool {
			return e.Kind == "detail" && e.Extra == fixtureSites && strings.Contains(e.Text, "write 8 files")
		})
		if inner < 0 || (output >= 0) != verbose || (verbose && output < inner) {
			t.Errorf("verbose %t: inner %d, output %d:\n%s", verbose, inner, output, h.Lines())
		}
	}
}

// As drawn: the install line is one line, and -v lists its inner steps under
// it, each with its own mark and result.
func TestConvergeInstallAsPrinted(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, nil)
		fakeChecks(t, map[string]any{"apply --site vm": "5 changes"})
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
		line := "\ninstall\n  todo " + fixtureSites + "\n"
		inner := "      ○ apply --site vm: 5 changes\n      · apply --site home-a: after apply --site vm\n"
		if !strings.Contains(out, line) || strings.Contains(out, inner) != verbose || strings.Contains(out, "✓ apply --site watch") != verbose {
			t.Errorf("verbose %t:\n%s", verbose, out)
		}
	}
}

// On a terminal each prepare line, and the install line, shows the inner
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
		case args[0] == "apply" && siteOf(args) == "home-b":
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
		`  ⠋ ` + fixtureSites + `: home-b: \d+s \(start talk\)`,
		`  ⠋ ` + fixtureSites + `: home-b: \d+s \(wait for Patroni primary\)`,
	} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("no %q in:\n%q", want, out)
		}
	}
	// The install line goes back to the step enclosing the one that ended,
	// and past home-b shows none, since apply left it open.
	if !regexp.MustCompile(`\(wait for Patroni primary\)[^\n]*\r\x1b\[K  ⠋ ` + fixtureSites + `: home-b: \d+s \(start talk\)`).MatchString(out) {
		t.Errorf("the enclosing step was not shown again:\n%q", out)
	}
	if regexp.MustCompile(fixtureSites + `: watch[^\n\r]*\(`).MatchString(out) {
		t.Errorf("a later site shows home-b's step:\n%q", out)
	}
	for _, ended := range regexp.MustCompile(`✓\x1b\[0m [^\n]*`).FindAllString(out, -1) {
		if strings.Contains(ended, "(") {
			t.Errorf("a finished line shows an inner step: %q", ended)
		}
	}
}
