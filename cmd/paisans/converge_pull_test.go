package main

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// pullAtOnce is allAtOnce for a test about the pulls: every host prepares
// at once too, as a real run does, and prepares nothing.
func pullAtOnce(t *testing.T) {
	t.Helper()
	allAtOnce(t)
	convergePrepare = func(prepareEnv, []string) error { return nil }
}

// gib is n GiB in bytes.
func gib(n float64) int64 { return int64(n * (1 << 30)) }

// toPull is the plan of a site whose apply would pull refs, with free bytes
// on Docker's data root against the default threshold.
func toPull(site string, free int64, refs ...string) *apply.Plan {
	return &apply.Plan{Site: site, Disk: &apply.DiskCheck{Dir: "/var/lib/docker", Free: free, Need: apply.DefaultMinFree, Pulls: refs}}
}

// fakePulls has each site's pull plan be plans[site], or nothing to pull,
// and pulls an image by calling pull, which may be nil to pull every one. It
// returns each image pulled, as "site ref", guarded for pulls at once.
func fakePulls(t *testing.T, plans map[string]*apply.Plan, pull func(site, ref string) (string, error)) func() []string {
	t.Helper()
	var mu sync.Mutex
	var pulled []string
	convergePullPlan = func(_ *config.Config, _ *config.Secrets, site string, _ apply.Transport, _ ...apply.Option) (*apply.Plan, error) {
		if p, ok := plans[site]; ok {
			return p, nil
		}
		return &apply.Plan{Site: site}, nil
	}
	convergePullImage = func(tr apply.Transport, ref string) (string, error) {
		site := tr.(apply.SSHTransport).Site
		mu.Lock()
		pulled = append(pulled, site+" "+ref)
		mu.Unlock()
		if pull == nil {
			return "", nil
		}
		return pull(site, ref)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(pulled)
		slices.Sort(out)
		return out
	}
}

// ended is how each line of a section ended, as "kind site: result".
func ended(rec *ui.Recorder, section string) []string {
	var out []string
	for _, e := range inSection(rec, section) {
		switch e.Kind {
		case "done", "fail", "pending", "waiting":
			line := e.Kind + " " + e.Text
			if e.Extra != "" {
				line += ": " + e.Extra
			}
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return out
}

// With --execute every host pulls at the same time, after prepare and before
// dns: each fake host waits until all of them are pulling.
func TestApplyPullsEveryHostAtOnce(t *testing.T) {
	ran := fakeConverge(t, nil)
	pullAtOnce(t)
	rec := recordConverge(t)
	wait := barrier(t, len(preparedSites))
	plans := map[string]*apply.Plan{}
	for _, s := range preparedSites {
		plans[s] = toPull(s, gib(20), "ghcr.io/example/"+s+":1")
	}
	pulled := fakePulls(t, plans, func(string, string) (string, error) {
		wait()
		return "", nil
	})
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, s := range preparedSites {
		want = append(want, s+" ghcr.io/example/"+s+":1")
	}
	sameLines(t, pulled(), want)
	sameLines(t, ended(rec, "pull"), []string{"done home-a: 1 image pulled", "done home-b: 1 image pulled", "done vm: 1 image pulled", "done watch: 1 image pulled"})
	sections := []string{}
	for _, e := range rec.Events {
		if e.Kind == "section" && !slices.Contains(sections, e.Text) {
			sections = append(sections, e.Text)
		}
	}
	if p, d := slices.Index(sections, "pull"), slices.Index(sections, "dns"); p < 0 || d < p || p < slices.Index(sections, "prepare") {
		t.Errorf("sections in the order %v, want prepare, pull, dns", sections)
	}
	if len(*ran) == 0 {
		t.Error("nothing ran after the pulls")
	}
}

// One host pulls a few images at a time, not one after another and not all
// at once.
func TestAHostPullsAFewImagesAtOnce(t *testing.T) {
	fakeConverge(t, nil)
	recordConverge(t)
	var refs []string
	for _, r := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		refs = append(refs, "ghcr.io/example/"+r+":1")
	}
	var inside, most atomic.Int32
	fakePulls(t, map[string]*apply.Plan{"home-a": toPull("home-a", gib(20), refs...)}, func(string, string) (string, error) {
		n := inside.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inside.Add(-1)
		return "", nil
	})
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if most.Load() != pullsAtOnce {
		t.Errorf("%d pulls at once on one host, want %d", most.Load(), pullsAtOnce)
	}
}

// A host that fails does not stop the others: every host pulls, each
// failure is marked on its line and named in the error, and the run stops
// before dns and install.
func TestAFailedPullLetsTheOthersFinishAndStopsBeforeDNS(t *testing.T) {
	ran := fakeConverge(t, nil)
	pullAtOnce(t)
	rec := recordConverge(t)
	plans := map[string]*apply.Plan{}
	for _, s := range preparedSites {
		plans[s] = toPull(s, gib(20), "ghcr.io/example/"+s+":1")
	}
	pulled := fakePulls(t, plans, func(site, ref string) (string, error) {
		if site == "vm" {
			return "Error response from daemon: manifest unknown\n", errors.New("exit status 1")
		}
		time.Sleep(20 * time.Millisecond)
		return "", nil
	})
	saved := convergePullPlan
	convergePullPlan = func(cfg *config.Config, secrets *config.Secrets, site string, tr apply.Transport, o ...apply.Option) (*apply.Plan, error) {
		if site == "home-a" {
			return nil, errors.New("deploy@home-a: ssh could not connect")
		}
		return saved(cfg, secrets, site, tr, o...)
	}
	err := converge(t, "--execute")
	if err == nil {
		t.Fatal("no error")
	}
	sameLines(t, pulled(), []string{"home-b ghcr.io/example/home-b:1", "vm ghcr.io/example/vm:1", "watch ghcr.io/example/watch:1"})
	got := ended(rec, "pull")
	for _, want := range []string{"fail home-a", "fail vm", "done home-b", "done watch"} {
		if !slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, want) }) {
			t.Errorf("no %s line: %v", want, got)
		}
	}
	if len(*ran) != 0 || rec.Has("section", "dns") || rec.Has("section", "install") {
		t.Errorf("ran %v after a failed pull:\n%s", *ran, rec.Lines())
	}
	hint, explain := ui.Describe(err)
	if hint != "apply stopped at pull on home-a, vm" {
		t.Errorf("hint %q", hint)
	}
	for _, want := range []string{"ssh could not connect", "docker pull ghcr.io/example/vm:1 failed: Error response from daemon: manifest unknown.", resumeAdvice()} {
		if !strings.Contains(explain, want) {
			t.Errorf("explanation does not say %q:\n%s", want, explain)
		}
	}
}

// A host short of space for its images is refused with the words its apply
// would use, before it pulls anything, and the run stops at its pull.
func TestAShortDiskStopsThePullWithApplysRefusal(t *testing.T) {
	fakeConverge(t, nil)
	recordConverge(t)
	pulled := fakePulls(t, map[string]*apply.Plan{
		"vm": toPull("vm", gib(1), "ghcr.io/example/caddy:2", "ghcr.io/example/talk:1"),
	}, nil)
	err := converge(t, "--execute")
	if len(pulled()) != 0 {
		t.Errorf("pulled %v on a short disk", pulled())
	}
	hint, explain := ui.Describe(err)
	if hint != "apply stopped at pull --site vm" {
		t.Errorf("hint %q", hint)
	}
	for _, want := range []string{"too little free space to pull 2 images", "vm: 1.0 GiB free on /var/lib/docker, and this apply needs at least 3.0 GiB", "--min-free", resumeAdvice()} {
		if !strings.Contains(explain, want) {
			t.Errorf("explanation does not say %q:\n%s", want, explain)
		}
	}
}

// A site with nothing to pull ends its line at once, and pulls nothing.
func TestNothingToPullEndsTheLine(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	pulled := fakePulls(t, nil, nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(pulled()) != 0 {
		t.Errorf("pulled %v", pulled())
	}
	sameLines(t, ended(rec, "pull"), []string{"done home-a: nothing to pull", "done home-b: nothing to pull", "done vm: nothing to pull", "done watch: nothing to pull"})
}

// A dry run's pull line says how many images each site would pull and the
// space they land in, or that it has nothing to pull, and marks a short disk
// with the refusal --execute would stop at. It pulls nothing.
func TestADryRunSaysWhatEachSiteWouldPull(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	pulled := fakePulls(t, map[string]*apply.Plan{
		"home-a": toPull("home-a", gib(12.4), "a:1", "b:1", "c:1"),
		"home-b": toPull("home-b", gib(9), "a:1"),
		"vm":     toPull("vm", gib(1), "caddy:2"),
	}, nil)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(pulled()) != 0 {
		t.Errorf("a dry run pulled %v", pulled())
	}
	sameLines(t, ended(rec, "pull"), []string{
		"done watch: nothing to pull",
		"fail vm: too little free space to pull 1 image",
		"pending home-a: 3 images to pull, 12.4 GiB free",
		"pending home-b: 1 image to pull, 9.0 GiB free",
	})
	events := inSection(rec, "pull")
	if !slices.ContainsFunc(events, func(e ui.Event) bool {
		return e.Kind == "explain" && strings.Contains(e.Text, "vm: 1.0 GiB free on /var/lib/docker")
	}) {
		t.Errorf("the short disk's refusal is not shown:\n%s", rec.Lines())
	}
	if !slices.ContainsFunc(events, func(e ui.Event) bool {
		return e.Kind == "detail" && e.Extra == "home-a" && strings.Contains(e.Text, "before pulling 3 image(s) (a:1, b:1, c:1)")
	}) {
		t.Errorf("home-a's images are not listed under its line:\n%s", rec.Lines())
	}
}

// A pull waits on its own site's prepare, and on nothing else.
func TestAPullWaitsOnItsPrepare(t *testing.T) {
	cfg := fixture(t)
	steps := convergePlan(cfg, convergeState{})
	for i, s := range steps {
		if s.Stage == stagePull {
			sameLines(t, convergeWaits(cfg, steps, i), []string{"site prepare --site " + stepSite(s)})
		}
	}
}

// A registry that refuses an image says it is private, with what to do.
func TestAPullTheRegistryRefusesSaysTheImageIsPrivate(t *testing.T) {
	out := "Error response from daemon: Head \"https://ghcr.io/v2/example/caddy/manifests/2\": denied\n"
	err := pullFailure("vm", "ghcr.io/example/caddy:2", out, errors.New("exit status 1"))
	hint, explain := ui.Describe(err)
	if hint != "could not pull ghcr.io/example/caddy:2" {
		t.Errorf("hint %q", hint)
	}
	if !strings.Contains(explain, "the image is private: make it public, or log the host in to that registry") {
		t.Errorf("explanation: %s", explain)
	}
	plain := pullFailure("vm", "a:1", "Error response from daemon: manifest unknown\n", errors.New("exit status 1"))
	if _, explain := ui.Describe(plain); strings.Contains(explain, "private") {
		t.Errorf("an unknown manifest reads as a private image: %s", explain)
	}
}

// The pull stage and every apply after it plan Caddy at the one build the
// run first resolved its tag to, though the tag moves in between.
func TestThePullAndTheInstallShareOneDigest(t *testing.T) {
	fakeConverge(t, nil)
	recordConverge(t)
	savedDigests := imageDigests
	t.Cleanup(func() { imageDigests = savedDigests })
	var asked atomic.Int32
	imageDigests = movingTag{&asked}
	var mu sync.Mutex
	seen := map[string]bool{}
	resolve := func() {
		d, err := imageDigests.Digest("ghcr.io/paisans-software/caddy:2")
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		seen[d] = true
		mu.Unlock()
	}
	convergePullPlan = func(_ *config.Config, _ *config.Secrets, site string, _ apply.Transport, _ ...apply.Option) (*apply.Plan, error) {
		resolve()
		return &apply.Plan{Site: site}, nil
	}
	run := convergeRun
	convergeRun = func(args []string) error {
		if isApply(args) {
			resolve()
		}
		return run(args)
	}
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || asked.Load() != 1 {
		t.Errorf("the run planned Caddy at %v, asking the registry %d times", seen, asked.Load())
	}
	if imageDigests != (movingTag{&asked}) {
		t.Error("the run left its cache in place")
	}
}

// movingTag is a registry whose tag names a new build each time it is asked.
type movingTag struct{ asked *atomic.Int32 }

func (m movingTag) Digest(string) (string, error) {
	return "sha256:" + strings.Repeat("0", 63) + string(rune('0'+m.asked.Add(1)%10)), nil
}

// The real pull stage, every site at once through a fake ssh whose hosts
// have every image: each site plans as its apply would and has nothing to
// pull. Under -race this is what proves the sites' plans share nothing
// unguarded.
func TestRealPullPlansRunTogether(t *testing.T) {
	quietSSH(t)
	var b strings.Builder
	r := ui.NewPlain(&lockedWriter{w: &b}, true)
	lines := ui.Group(r, preparedSites...)
	p := newPuller(fixture(t), convergeOptions{Config: fixtureConfig(), Secrets: fixtureSecretsPath()})
	var wg sync.WaitGroup
	for k, site := range preparedSites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.pullSite(lines[k], step(stagePull, "pull", "--site", site)); err != nil {
				t.Errorf("%s: %v", site, err)
			}
		}()
	}
	wg.Wait()
	for _, site := range preparedSites {
		if !strings.Contains(b.String(), site+": nothing to pull") {
			t.Errorf("%s did not end with nothing to pull:\n%s", site, b.String())
		}
	}
}

// progressLine is a ui.Line that keeps what its spinner would show.
type progressLine struct {
	mu       sync.Mutex
	progress []string
	result   string
}

func (l *progressLine) Done(result string)           { l.result = result }
func (l *progressLine) Fail(error)                   {}
func (l *progressLine) End(_ ui.Mark, result string) { l.result = result }
func (l *progressLine) Detail(string, ...any)        {}
func (l *progressLine) Explain(string)               {}
func (l *progressLine) Note(string, string)          {}
func (l *progressLine) Progress(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.progress = append(l.progress, text)
}

// A site's pull line counts the images pulled while they pull, so a long
// pull shows it is moving and not only how long it has run.
func TestAPullLineCountsTheImagesPulled(t *testing.T) {
	fakeConverge(t, nil)
	refs := []string{"ghcr.io/example/a:1", "ghcr.io/example/b:1", "ghcr.io/example/c:1", "ghcr.io/example/d:1"}
	fakePulls(t, map[string]*apply.Plan{"home-a": toPull("home-a", gib(20), refs...)}, nil)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	p := newPuller(cfg, convergeOptions{})
	p.once.Do(func() {})
	var line progressLine
	if _, err := p.pullSite(&line, step(stagePull, "pull", "--site", "home-a")); err != nil {
		t.Fatal(err)
	}
	want := []string{"0 of 4 images pulled", "1 of 4 images pulled", "2 of 4 images pulled", "3 of 4 images pulled", "4 of 4 images pulled"}
	if !slices.Equal(line.progress, want) || line.result != "4 images pulled" {
		t.Errorf("progress %q, result %q", line.progress, line.result)
	}
}

// moving is a pin of app's image whose build the apply moves, from the
// build behind old to the one behind new.
func moving(app, old, new string) apply.ImagePin {
	ref := "ghcr.io/example/" + app + ":1"
	return apply.ImagePin{Stack: app, App: app, Services: []string{"app"}, Image: ref, Deployed: ref + "@sha256:" + strings.Repeat(old, 64), Reference: ref + "@sha256:" + strings.Repeat(new, 64), DeployedVersion: "1.0.0", Version: "1.1.0"}
}

// explainedAfter is what is explained under site's line in section: every
// explain event after the line ends and before the next line's.
func explainedAfter(rec *ui.Recorder, section, site string) []string {
	var out []string
	under := false
	for _, e := range inSection(rec, section) {
		switch e.Kind {
		case "done", "fail", "pending", "waiting":
			under = e.Text == site
		case "explain":
			if under {
				out = append(out, e.Text)
			}
		}
	}
	return out
}

// Each image whose build a site's apply moves is named under the site's pull
// line, with the builds it goes between, in a dry run and with --execute
// alike, whether or not the host has the image to pull. A pin that stays on
// its build is not named.
func TestThePullLineNamesEachImageThatMoves(t *testing.T) {
	for _, execute := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry run", true: "execute"}[execute], func(t *testing.T) {
			fakeConverge(t, nil)
			pullAtOnce(t)
			rec := recordConverge(t)
			pulling := toPull("home-a", gib(20), "ghcr.io/example/account:1")
			pulling.Pins = []apply.ImagePin{moving("account", "a", "b")}
			present := &apply.Plan{Site: "home-b", Pins: []apply.ImagePin{moving("talk", "c", "d")}}
			still := moving("blog", "e", "e")
			kept := &apply.Plan{Site: "vm", Pins: []apply.ImagePin{still}}
			fakePulls(t, map[string]*apply.Plan{"home-a": pulling, "home-b": present, "vm": kept}, nil)
			args := []string{}
			if execute {
				args = append(args, "--execute")
			}
			if err := converge(t, args...); err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{
				"home-a": {"account  app  ghcr.io/example/account:1  1.0.0 (sha256:aaaaaaaaaaaa) -> 1.1.0 (sha256:bbbbbbbbbbbb)"},
				"home-b": {"talk  app  ghcr.io/example/talk:1  1.0.0 (sha256:cccccccccccc) -> 1.1.0 (sha256:dddddddddddd)"},
				"vm":     nil,
			}
			for site, w := range want {
				if got := explainedAfter(rec, "pull", site); !slices.Equal(got, w) {
					t.Errorf("%s's pull line explains %q, want %q\n%s", site, got, w, rec.Lines())
				}
			}
		})
	}
}
