package main

import (
	"errors"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// preparedSites are the fixture's sites, each with a prepare line, in name order.
var preparedSites = []string{"home-a", "home-b", "vm", "watch"}

// allAtOnce lets prepare run every host at once, as a real run does;
// fakeConverge runs them one at a time, in order, for tests about order.
func allAtOnce(t *testing.T) {
	t.Helper()
	saved := prepareAtOnce
	prepareAtOnce = defaultPrepareAtOnce
	t.Cleanup(func() { prepareAtOnce = saved })
}

// barrier is a fake host's wait for every other host: it returns once n
// callers are inside it, and fails the test if they never all are, which is
// what a run one host at a time would do.
func barrier(t *testing.T, n int) func() {
	var mu sync.Mutex
	inside := 0
	all := make(chan struct{})
	return func() {
		mu.Lock()
		inside++
		if inside == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			t.Error("the hosts were not prepared at the same time")
		}
	}
}

// siteOf is the site a step's arguments name with --site.
func siteOf(args []string) string {
	if i := slices.Index(args, "--site"); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// With --execute every host is prepared at the same time: each fake host
// waits until all of them are being prepared.
func TestApplyPreparesEveryHostAtOnce(t *testing.T) {
	ran := fakeConverge(t, nil)
	allAtOnce(t)
	recordConverge(t)
	wait := barrier(t, len(preparedSites))
	var mu sync.Mutex
	var prepared []string
	convergePrepare = func(env prepareEnv, args []string) error {
		wait()
		mu.Lock()
		prepared = append(prepared, siteOf(args))
		mu.Unlock()
		return nil
	}
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	slices.Sort(prepared)
	sameLines(t, prepared, preparedSites)
	if len(*ran) == 0 {
		t.Error("nothing ran after prepare")
	}
}

// A dry run checks every host at the same time too.
func TestADryRunChecksEveryHostAtOnce(t *testing.T) {
	fakeConverge(t, nil)
	allAtOnce(t)
	rec := recordConverge(t)
	wait := barrier(t, len(preparedSites))
	convergeCheck = func(_ ui.Reporter, args []string) (string, []checkReport, error) {
		if isPrepare(args) {
			wait()
			return "docker", nil, nil
		}
		return "", nil, nil
	}
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	for _, site := range preparedSites {
		i := slices.IndexFunc(rec.Events, func(e ui.Event) bool { return e.Kind == "pending" && e.Text == site })
		if i < 0 || rec.Events[i].Extra != "docker" {
			t.Errorf("no pending line for %s:\n%s", site, rec.Lines())
		}
	}
}

// A host that fails does not stop the others: every host is prepared, each
// failure is marked on its line and named in the error, and the run stops
// before install.
func TestAFailedHostLetsTheOthersFinishAndStopsBeforeInstall(t *testing.T) {
	ran := fakeConverge(t, nil)
	allAtOnce(t)
	rec := recordConverge(t)
	var mu sync.Mutex
	var prepared []string
	convergePrepare = func(env prepareEnv, args []string) error {
		site := siteOf(args)
		mu.Lock()
		prepared = append(prepared, site)
		mu.Unlock()
		switch site {
		case "home-a":
			return &ui.Problem{Hint: "mesh subnet 10.44.0.0/24 is in use", Explain: "Remove it if not in use."}
		case "vm":
			return errors.New("vm: apt-get install failed")
		}
		// The others finish after the failures.
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	err := converge(t, "--execute")
	if err == nil {
		t.Fatal("no error")
	}
	slices.Sort(prepared)
	sameLines(t, prepared, preparedSites)
	for _, l := range []string{"fail home-a", "fail vm", "done home-b", "done watch"} {
		kind, site, _ := strings.Cut(l, " ")
		if !rec.Has(kind, site) {
			t.Errorf("no %s line:\n%s", l, rec.Lines())
		}
	}
	if len(*ran) != 0 {
		t.Errorf("ran after a failed prepare: %v", *ran)
	}
	if rec.Has("section", "install") {
		t.Errorf("install started:\n%s", rec.Lines())
	}
	hint, explain := ui.Describe(err)
	for _, want := range []string{"home-a", "vm"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not name %s", hint, want)
		}
	}
	for _, want := range []string{"mesh subnet 10.44.0.0/24 is in use", "Remove it if not in use.", "apt-get install failed", resumeAdvice} {
		if !strings.Contains(explain, want) {
			t.Errorf("explanation does not say %q:\n%s", want, explain)
		}
	}
}

// One failed host stops the run as it always has: at that host's prepare.
func TestOneFailedHostStopsAtItsPrepare(t *testing.T) {
	fakeConverge(t, nil)
	allAtOnce(t)
	recordConverge(t)
	convergePrepare = func(env prepareEnv, args []string) error {
		if siteOf(args) == "vm" {
			return errors.New("apt-get install failed")
		}
		return nil
	}
	err := converge(t, "--execute")
	if hint, _ := ui.Describe(err); hint != "apply stopped at site prepare --site vm" {
		t.Errorf("hint %q", hint)
	}
}

// plainPrepare runs apply --execute on plain output, every host at once,
// each fake host saying things while it works, and returns the prepare
// section's lines.
func plainPrepare(t *testing.T, verbose bool) []string {
	t.Helper()
	fakeConverge(t, nil)
	allAtOnce(t)
	var b strings.Builder
	saved := reporterOverride
	reporterOverride = ui.NewPlain(&lockedWriter{w: &b}, verbose)
	t.Cleanup(func() { reporterOverride = saved })
	var turn atomic.Int32
	convergePrepare = func(env prepareEnv, args []string) error {
		site := siteOf(args)
		for k := 0; k < 5; k++ {
			env.r.Result("%s said %d", site, k)
			env.r.Warn(fmt.Sprintf("%s warned %d", site, k), "")
			turn.Add(1)
			time.Sleep(time.Millisecond)
		}
		if site == "vm" {
			return errors.New("apt-get install failed")
		}
		return nil
	}
	args := []string{"--execute"}
	if verbose {
		args = append(args, "-v")
	}
	if err := converge(t, args...); err == nil {
		t.Fatal("no error")
	}
	out := b.String()
	start := strings.Index(out, "\nprepare\n")
	if start < 0 {
		t.Fatalf("no prepare section:\n%s", out)
	}
	section := strings.TrimSuffix(out[start+len("\nprepare\n"):], "\n")
	if i := strings.Index(section, "\n\n"); i >= 0 {
		section = section[:i]
	}
	return strings.Split(section, "\n")
}

var prepareLine = regexp.MustCompile(`^  (ok  |FAIL) (home-a|home-b|vm|watch)(: [0-9.]+s)?$`)

// Plain output is one whole line per host, printed when it ends, with
// nothing of another host's between.
func TestPlainPrepareIsOneWholeLinePerHost(t *testing.T) {
	lines := plainPrepare(t, false)
	var sites []string
	for _, l := range lines {
		m := prepareLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("not a whole prepare line: %q in\n%s", l, strings.Join(lines, "\n"))
		}
		sites = append(sites, m[2])
	}
	slices.Sort(sites)
	sameLines(t, sites, preparedSites)
}

// With -v each host's own output and warnings print right under its line,
// all of them together, never between another host's.
func TestVerbosePrepareGroupsEachHostsDetails(t *testing.T) {
	lines := plainPrepare(t, true)
	current := ""
	seen := map[string]bool{}
	for _, l := range lines {
		if m := prepareLine.FindStringSubmatch(l); m != nil {
			current = m[2]
			if seen[current] {
				t.Fatalf("%s has two lines:\n%s", current, strings.Join(lines, "\n"))
			}
			seen[current] = true
			continue
		}
		if current == "" || !strings.Contains(l, current+" said") && !strings.Contains(l, current+" warned") {
			t.Fatalf("%q is not under its host's line:\n%s", l, strings.Join(lines, "\n"))
		}
	}
	if len(seen) != len(preparedSites) {
		t.Errorf("lines for %v:\n%s", seen, strings.Join(lines, "\n"))
	}
	for _, site := range preparedSites {
		for k := 0; k < 5; k++ {
			for _, what := range []string{"said", "warned"} {
				if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, fmt.Sprintf("%s %s %d", site, what, k)) }) {
					t.Errorf("no %q under %s", fmt.Sprintf("%s %d", what, k), site)
				}
			}
		}
	}
}

// Two hosts that ask for a sudo password at the same time are asked one
// after the other, so the prompts cannot share the terminal.
func TestSudoPromptsAreAskedOneAtATime(t *testing.T) {
	saved := readPassword
	t.Cleanup(func() { readPassword = saved })
	var asking, most atomic.Int32
	readPassword = func(host string) (string, error) {
		n := asking.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		asking.Add(-1)
		return "pw", nil
	}
	var wg sync.WaitGroup
	for _, site := range preparedSites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			promptOnTerminal(site)
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Errorf("%d prompts at once, want 1", most.Load())
	}
}

// lockedWriter is a writer several goroutines may write to.
type lockedWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// The real site prepare, run on every host at once through a fake ssh, as a
// dry run and with --execute: under -race this is what proves the hosts'
// runs share nothing unguarded.
func TestRealHostPreparesRunTogether(t *testing.T) {
	quietSSH(t)
	// As apply with no --site runs them: after its review, which approved
	// every site's keys.
	fakeGrants(t, func(string) *grant.Record { return nil })
	cfg := fixture(t)
	approved := map[string]grant.Review{}
	for _, site := range preparedSites {
		g, _ := grant.For(cfg, nil, site)
		approved[site] = grant.Compare(site, g, grant.Record{}, grant.PrepareScope)
	}
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	for _, execute := range []bool{false, true} {
		reviewedByRun = &runReview{approved: approved}
		var b strings.Builder
		r := ui.NewPlain(&lockedWriter{w: &b}, true)
		routeRetries(r)
		routeHolds(r)
		t.Cleanup(func() { routeRetries(ui.Discard); routeHolds(ui.Discard) })
		o := convergeOptions{Config: fixtureConfig(), Execute: execute, Verbose: true}
		var wg sync.WaitGroup
		for _, site := range preparedSites {
			wg.Add(1)
			go func() {
				defer wg.Done()
				output, _, found, _, _, err := prepareRun(r, nil, convergeFlags([]string{"site", "prepare", "--site", site}, o), true)
				switch {
				case err != nil:
					t.Errorf("execute %v: %s: %v", execute, site, err)
				case !execute && !found:
					t.Errorf("%s: the dry run found nothing", site)
				case execute && !strings.Contains(output, "Prepared "+site+"."):
					t.Errorf("%s: not prepared:\n%s", site, output)
				}
			}()
		}
		wg.Wait()
	}
}
