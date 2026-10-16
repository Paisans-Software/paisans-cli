package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/storageadd"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// stageLabels names each stage for a test's plan lines. The run shows none
// of them: its sections are prepare, dns and install.
var stageLabels = map[stage]string{
	stageInit:     "configuration",
	stagePrepare:  "hosts",
	stagePull:     "pull",
	stageFounding: "founding",
	stageJoining:  "joining",
	stageOthers:   "other sites",
	stageStorage:  "storage",
	stagePassTwo:  "pass two",
	stageDNS:      "dns",
}

// titles is the plan as "stage: title" lines.
func titles(steps []convergeStep) []string {
	var out []string
	for _, s := range steps {
		out = append(out, stageLabels[s.Stage]+": "+s.Title)
	}
	return out
}

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// samePlan fails t unless steps are want, as "phase: title" lines, exactly
// and in order.
func samePlan(t *testing.T, steps []convergeStep, want ...string) {
	t.Helper()
	sameLines(t, titles(steps), want)
}

func sameLines(t *testing.T, got, want []string) {
	t.Helper()
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		t.Errorf("got:\n%s\nwant:\n%s", g, w)
	}
}

// hostsAndDNS, foundingPhase and the rest are the fixture's plan, phase by
// phase, where a test does not change them. Each site's pull follows the
// hosts, and dns follows the pulls, before any apply: the mesh's endpoint
// names must resolve before wg-quick starts.
var (
	hostsAndDNS = []string{
		"hosts: host prepare --site home-a",
		"hosts: host prepare --site home-b",
		"hosts: host prepare --site vm",
		"hosts: host prepare --site watch",
		"pull: pull --site home-a",
		"pull: pull --site home-b",
		"pull: pull --site vm",
		"pull: pull --site watch",
		"dns: dns init",
	}
	foundingPhase = []string{
		"founding: apply --site vm",
		"founding: apply --site home-a",
		"founding: apply --site home-b",
	}
	passTwoPhase = []string{
		"pass two: apply --site home-a",
		"pass two: apply --site home-b",
		"pass two: apply --site vm",
		"pass two: apply --site watch",
	}
)

// blankPlan is the fixture's plan with no member founded.
func blankPlan() []string {
	return slices.Concat(hostsAndDNS, foundingPhase, []string{"other sites: apply --site watch", "storage: storage add"}, passTwoPhase)
}

// commands is plan lines as the commands a run runs: no phase, no init,
// which runs before the plan is read again, and no pull, which the run does
// itself rather than through a command.
func commands(lines []string) []string {
	var out []string
	for _, l := range lines {
		stage, title, _ := strings.Cut(l, ": ")
		if title != "init" && stage != "pull" {
			out = append(out, title)
		}
	}
	return out
}

func TestConvergeFoundsABlankDeploymentWitnessFirst(t *testing.T) {
	samePlan(t, convergePlan(fixture(t), convergeState{}), blankPlan()...)
}

func initial(names ...string) render.EtcdInitial {
	var pairs []string
	for i, n := range names {
		pairs = append(pairs, fmt.Sprintf("%s=http://10.44.0.%d:2380", n, i+1))
	}
	return render.EtcdInitial{State: "new", Cluster: strings.Join(pairs, ",")}
}

// A member the founded cluster's initial-cluster does not list joins it.
func TestConvergeJoinsAMemberTheClusterDoesNotList(t *testing.T) {
	steps := convergePlan(fixture(t), convergeState{Initial: map[string]render.EtcdInitial{"home-a": initial("home-a", "vm"), "vm": initial("home-a", "vm")}})
	samePlan(t, steps, slices.Concat(hostsAndDNS, []string{"joining: site add home-b", "other sites: apply --site watch", "storage: storage add"}, passTwoPhase)...)
}

// One data site runs, founded alone, and the configuration adds a witness and
// a second data site: the documented way from one voter to three. The data
// site's site add joins both, the witness first, so the witness gets no site
// add of its own, which would refuse it as not a data site and stop the run.
func TestConvergeJoinsAWitnessThroughTheDataSitesSiteAdd(t *testing.T) {
	steps := convergePlan(fixture(t), convergeState{Initial: map[string]render.EtcdInitial{"home-a": initial("home-a")}})
	samePlan(t, steps, slices.Concat(hostsAndDNS, []string{"joining: site add home-b", "other sites: apply --site watch", "storage: storage add"}, passTwoPhase)...)
}

// A witness joining with no data site beside it is still planned, as its own
// site add, so the run stops there with site add's refusal, which says what to
// do, rather than leaving the witness out of the cluster without a word.
func TestConvergePlansAWitnessJoiningAloneAsItsSiteAdd(t *testing.T) {
	founded := initial("home-a", "home-b")
	steps := convergePlan(fixture(t), convergeState{Initial: map[string]render.EtcdInitial{"home-a": founded, "home-b": founded}})
	samePlan(t, steps, slices.Concat(hostsAndDNS, []string{"joining: site add vm", "other sites: apply --site watch", "storage: storage add"}, passTwoPhase)...)
}

// A member a founding record lists is still being founded, though another
// member has its record: it is applied, not added.
func TestConvergeResumesAFoundingAfterAFailure(t *testing.T) {
	steps := convergePlan(fixture(t), convergeState{Initial: map[string]render.EtcdInitial{"vm": initial("home-a", "home-b", "vm")}})
	samePlan(t, steps, slices.Concat(hostsAndDNS, []string{"other sites: apply --site vm", "founding: apply --site home-a", "founding: apply --site home-b", "other sites: apply --site watch", "storage: storage add"}, passTwoPhase)...)
}

// A witness that has its record, though no data member it lists has one, is
// applied before them: their founding is refused until its etcd runs.
func TestConvergeAppliesARecordedWitnessBeforeTheFoundingMembers(t *testing.T) {
	steps := convergePlan(fixture(t), convergeState{Initial: map[string]render.EtcdInitial{"vm": initial("home-a", "home-b", "vm")}})
	lines := titles(steps)
	witness := slices.IndexFunc(lines, func(l string) bool { return strings.HasSuffix(l, "apply --site vm") })
	for _, data := range []string{"founding: apply --site home-a", "founding: apply --site home-b"} {
		if i := slices.Index(lines, data); i < 0 || witness < 0 || witness > i {
			t.Errorf("the witness's apply does not come before %q:\n%s", data, strings.Join(lines, "\n"))
		}
	}
	if steps[witness].Stage == stageFounding {
		t.Errorf("a recorded witness is applied as founding: %s", lines[witness])
	}
}

func TestConvergeRunsInitFirstWhenItHasWork(t *testing.T) {
	samePlan(t, convergePlan(fixture(t), convergeState{NeedsInit: true}), append([]string{"configuration: init"}, blankPlan()...)...)
}

func TestConvergeAppliesEachSiteOnceBeforePassTwo(t *testing.T) {
	steps := convergePlan(fixture(t), convergeState{})
	seen := map[string]int{}
	for _, s := range steps {
		if s.Stage != stagePassTwo && len(s.Args) > 0 && s.Args[0] == "apply" {
			seen[s.Title]++
		}
	}
	for title, n := range seen {
		if n != 1 {
			t.Errorf("%s applied %d times before pass two", title, n)
		}
	}
}

func TestConvergeWithoutEtcdAppliesEverySiteInPhaseThree(t *testing.T) {
	cfg := fixture(t)
	cfg.Etcd.Members = nil
	samePlan(t, convergePlan(cfg, convergeState{}), slices.Concat(hostsAndDNS, []string{
		"other sites: apply --site home-a",
		"other sites: apply --site home-b",
		"other sites: apply --site vm",
		"other sites: apply --site watch",
		"storage: storage add",
	}, passTwoPhase)...)
}

func TestConvergeStorageFollowsTheGarageSites(t *testing.T) {
	cfg := fixture(t)
	before := slices.Concat(hostsAndDNS, foundingPhase, []string{"other sites: apply --site watch"})
	cfg.Storage.Garage.Sites = []string{"home-a"}
	samePlan(t, convergePlan(cfg, convergeState{}), slices.Concat(before, []string{"storage: storage init --site home-a"}, passTwoPhase)...)
	cfg.Storage.Garage.Sites = nil
	samePlan(t, convergePlan(cfg, convergeState{}), slices.Concat(before, passTwoPhase)...)
}

// fakeConverge replaces each etcd record's read and the step runner; fail maps a
// step's title to the error it returns the first time it runs.
func fakeConverge(t *testing.T, fail map[string]error) *[]string {
	t.Helper()
	ran, _ := fakeConvergeArgs(t, fail)
	return ran
}

// fakeConvergeArgs is fakeConverge, and each step's arguments in full.
func fakeConvergeArgs(t *testing.T, fail map[string]error) (*[]string, *[][]string) {
	t.Helper()
	var ran []string
	var full [][]string
	savedRun, savedRead, savedCheck := convergeRun, convergeReadInitial, convergeCheck
	savedPrepare, savedAtOnce := convergePrepare, prepareAtOnce
	savedPullPlan, savedPull := convergePullPlan, convergePullImage
	// Each site's pull reads the plan its apply would make, which reaches a
	// host; here every image is present. fakePulls says otherwise.
	convergePullPlan = func(_ *config.Config, _ *config.Secrets, site string, _ apply.Transport, _ ...apply.Option) (*apply.Plan, error) {
		return &apply.Plan{Site: site}, nil
	}
	convergePullImage = func(_ apply.Transport, ref string) (string, error) {
		t.Errorf("pulled %s with every image present", ref)
		return "", nil
	}
	// Each host's prepare goes through the step runner below, one host at
	// a time and in order, reporting as a step under apply does; allAtOnce
	// runs them together.
	prepareAtOnce = 1
	convergePrepare = func(env prepareEnv, args []string) error {
		saved := reporterOverride
		reporterOverride = env.r
		defer func() { reporterOverride = saved }()
		return convergeRun(args)
	}
	// A dry run's checks are each step's own dry run, which reaches a host;
	// here every step is up to date. fakeChecks says otherwise.
	convergeCheck = func(ui.Reporter, []string) (string, []checkReport, error) { return "", nil, nil }
	// The run reads each gateway's deployment record once; here none has
	// one yet. fakeGatewayReads says otherwise.
	savedRegistry := registryHost
	registryHost = func(string, config.Site, string) registry.Runner { return noRecord{} }
	// Making each declared operator reaches a host too; here every one has
	// registered. fakeDeclaredOperators says otherwise.
	fakeDeclaredOperators(t, registered)
	fakeEmailFailures(t, nil, nil)
	// An app's web server is checked from this machine; here every check
	// passes. fakeWebServerCheck says otherwise.
	fakeWebServerCheck(t, nil)
	convergeRun = func(args []string) error {
		full = append(full, args)
		title := strings.Join(args, " ")
		for _, f := range []string{" --config", " --secrets", " --execute"} {
			if i := strings.Index(title, f); i >= 0 {
				title = title[:i]
			}
		}
		ran = append(ran, title)
		err := fail[title]
		delete(fail, title)
		return err
	}
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) {
		return render.EtcdInitial{}, false, nil
	}
	// The run's review reads each site's grant record; here each already
	// holds what the configuration grants, so nothing asks for approval.
	// fakeRunGrants says otherwise.
	savedRecord, savedGrantHost := convergeRecord, convergeGrantHost
	convergeRecord = func(_ *config.Config, _ string, g grant.Grant) (grant.Record, error) {
		return grant.Update(grant.Record{}, g, grant.AllScope, time.Unix(0, 0), ""), nil
	}
	convergeGrantHost = func(*config.Config, string) registry.Runner { return &memGrant{} }
	// The review renders what the run applies, from secrets init may have
	// generated; here init is a fake that generates nothing, so the render
	// is one with no files, and so no images.
	savedRender := convergeRender
	convergeRender = func(*config.Config, *config.Secrets) (*render.Plan, error) { return &render.Plan{}, nil }
	// The run reads each site's architecture through the host its steps
	// reach; here every host is amd64, and everything else goes on to the
	// host the test set.
	savedHost := convergeHost
	convergeHost = func(name string, site config.Site) apply.Transport {
		return archTransport{savedHost(name, site), "amd64"}
	}
	t.Cleanup(func() {
		convergeHost = savedHost
		convergeRun, convergeReadInitial, convergeCheck = savedRun, savedRead, savedCheck
		convergePrepare, prepareAtOnce = savedPrepare, savedAtOnce
		convergePullPlan, convergePullImage = savedPullPlan, savedPull
		registryHost = savedRegistry
		convergeRecord, convergeGrantHost, convergeRender = savedRecord, savedGrantHost, savedRender
	})
	return &ran, &full
}

func converge(t *testing.T, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, extra...), strings.NewReader(""), io.Discard)
	})
	return err
}

func TestConvergeRunsEveryStepInOrder(t *testing.T) {
	ran := fakeConverge(t, nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, *ran, commands(blankPlan()))
}

func TestConvergeDryRunRunsNothing(t *testing.T) {
	ran := fakeConverge(t, nil)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 0 {
		t.Errorf("a dry run ran %v", *ran)
	}
}

func TestTheFoundingStopDoesNotStopTheRun(t *testing.T) {
	ran := fakeConverge(t, map[string]error{"apply --site home-a": foundingWait("home-a", "home-b")})
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("the run stopped at the founding stop: %v", err)
	}
	sameLines(t, *ran, commands(blankPlan()))
}

// foundingWait is a founding member's stop for the others' etcd, as apply
// returns it.
func foundingWait(site string, unstarted ...string) error {
	return &ui.Problem{
		Hint:  site + " is waiting on " + strings.Join(unstarted, ", ") + " to start etcd.",
		Cause: &apply.FoundingWaitError{Site: site, Unstarted: unstarted},
	}
}

// databasesWait is a replica's stop for the app databases, as apply returns
// it.
func databasesWait(site, leader string) error {
	return &ui.Problem{
		Hint:  site + " is waiting on " + leader + " to create databases auth, talk",
		Cause: &apply.DatabasesWaitError{Site: site, Leader: leader, Missing: []string{"auth", "talk"}},
	}
}

// waitsFor makes the run's apply of site stop for the app databases on its
// first n runs.
func waitsFor(t *testing.T, site, leader string, n int) {
	t.Helper()
	saved := convergeRun
	runs := 0
	convergeRun = func(args []string) error {
		if err := saved(args); err != nil {
			return err
		}
		if title := strings.Join(args, " "); strings.HasPrefix(title, "apply --site "+site+" ") {
			if runs++; runs <= n {
				return databasesWait(site, leader)
			}
		}
		return nil
	}
	t.Cleanup(func() { convergeRun = saved })
}

// The first deployment's order: home-a's founding apply stops for etcd, so
// home-b's finds Patroni a replica of home-a with no database yet. Neither
// stop ends the run, and pass two, home-a first, creates the databases and
// then starts home-b's apps.
func TestTheDatabasesWaitDoesNotStopTheRun(t *testing.T) {
	ran := fakeConverge(t, map[string]error{"apply --site home-a": foundingWait("home-a", "home-b")})
	waitsFor(t, "home-b", "home-a", 1)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("the run stopped at the databases wait: %v", err)
	}
	sameLines(t, *ran, commands(blankPlan()))
	if !rec.Has("detail", "✓ apply --site home-b: paused until home-a creates its databases") || rec.Has("fail", "") || !rec.Has("done", "home-b") {
		t.Errorf("the databases wait read as a failure:\n%s", rec.Lines())
	}
}

// A replica still waiting on its last visit, pass two having reached it
// before the leader's site, is applied once more after every other step,
// and its line ends done then.
func TestTheDatabasesWaitOnTheLastVisitIsAppliedAgain(t *testing.T) {
	ran := fakeConverge(t, nil)
	waitsFor(t, "home-a", "home-b", 2)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("the run stopped at the databases wait: %v", err)
	}
	sameLines(t, *ran, append(commands(blankPlan()), "apply --site home-a"))
	if rec.Has("fail", "") || !rec.Has("done", "home-a") {
		t.Errorf("home-a's line did not end done:\n%s", rec.Lines())
	}
}

// A replica still waiting when applied once more, after every other step,
// has a leader that did not create the databases, and that ends the run.
func TestTheDatabasesWaitAfterEveryStepStopsTheRun(t *testing.T) {
	ran := fakeConverge(t, nil)
	waitsFor(t, "home-a", "home-b", 3)
	err := converge(t, "--execute")
	if !errors.Is(err, apply.ErrDatabasesWait) || !strings.Contains(err.Error(), "apply --site home-a") || !strings.Contains(err.Error(), "Run paisans apply --execute again to resume") {
		t.Fatalf("err = %v", err)
	}
	sameLines(t, *ran, append(commands(blankPlan()), "apply --site home-a"))
}

func TestAFailingStepStopsTheRun(t *testing.T) {
	ran := fakeConverge(t, map[string]error{"apply --site vm": errors.New("vm: boom")})
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "apply --site vm") || !strings.Contains(err.Error(), "Run paisans apply --execute again to resume") {
		t.Fatalf("err = %v", err)
	}
	sameLines(t, *ran, commands(slices.Concat(hostsAndDNS, foundingPhase[:1])))
}

func TestConvergeRefusesSSH(t *testing.T) {
	fakeConverge(t, nil)
	if err := converge(t, "--ssh", "admin@192.0.2.1"); err == nil || !strings.Contains(err.Error(), "--site") {
		t.Errorf("err = %v", err)
	}
}

// A configuration validate refuses is refused before any host is read: a
// typo in etcd.members is reported as one, not as a host that did not answer.
func TestConvergeRefusesBeforeReadingAHost(t *testing.T) {
	ran := fakeConverge(t, nil)
	read := false
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) {
		read = true
		return render.EtcdInitial{}, false, errors.New("no host should be read")
	}
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	body := strings.Replace(string(data), "members: [", "members: [nowhere, ", 1)
	if body == string(data) {
		t.Fatal("the fixture has no etcd.members list to edit")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := recordConverge(t)
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath()}, strings.NewReader(""), io.Discard)
	})
	if err == nil || !strings.Contains(err.Error(), "was refused") {
		t.Errorf("err = %v, want the configuration refused", err)
	}
	// The refusal stops the run, so its count line stays.
	// The deployment and the version open the run, with its five events
	// (the section, and each line's start and end), and the core catalog's
	// two when the process has read it already; the findings follow.
	opened := 5
	if rec.Index("step", "core catalog") == 5 {
		opened = 7
	}
	if rec.Index("section", "deployment") != 0 || rec.Index("section", "configuration") != opened || !rec.Has("result", "1 refusal") {
		t.Errorf("not under configuration, or not counted:\n%s", rec.Lines())
	}
	if read {
		t.Error("a host was read for a refused configuration")
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v", *ran)
	}
}

// noID is the fixture configuration without its deployment id.
func noID(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "id:") {
			kept = append(kept, line)
		}
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// With no id there is nothing on a host to read yet: the plan is init, and
// the rest once it has run.
func TestConvergeWithoutAnIDPlansInit(t *testing.T) {
	fakeConverge(t, nil)
	path := noID(t)
	var err error
	out := captureStdout(t, func() { err = runApply([]string{"--config", path}, strings.NewReader(""), io.Discard) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "init") || !strings.Contains(out, "once init has run") {
		t.Errorf("printed:\n%s", out)
	}
}

// After init, the state is read again and the rest planned from it.
func TestConvergeReplansAfterInit(t *testing.T) {
	ran := fakeConverge(t, nil)
	saved := convergeRead
	reads := 0
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		reads++
		cfg, err := config.Load(fixtureConfig())
		return cfg, convergeState{NeedsInit: reads == 1}, err
	}
	t.Cleanup(func() { convergeRead = saved })
	founded := initial("home-a", "vm")
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		if reads == 1 {
			t.Errorf("%s's record read before init ran", m)
		}
		return founded, m == "home-a" || m == "vm", nil
	}
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	sameLines(t, *ran, append([]string{"init"}, commands(slices.Concat(hostsAndDNS, []string{"joining: site add home-b", "other sites: apply --site watch", "storage: storage add"}, passTwoPhase))...))
}

// The dry run names what is left, the declared operators among it.
func TestConvergeSaysWhatIsLeft(t *testing.T) {
	fakeConverge(t, nil)
	out := ""
	captureStdout(t, func() {})
	out = captureStdout(t, func() {
		if err := runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, strings.NewReader(""), io.Discard); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"alice on auth"} {
		if !strings.Contains(out, want) {
			t.Errorf("does not say %q:\n%s", want, out)
		}
	}
}

// -v, --keep-images and --min-free reach every step whose command takes
// them: -v every step, the other two each apply.
func TestConvergePassesItsFlagsToEachStep(t *testing.T) {
	_, full := fakeConvergeArgs(t, nil)
	if err := converge(t, "--execute", "-v", "--keep-images", "--min-free", "2G"); err != nil {
		t.Fatal(err)
	}
	for _, args := range *full {
		got := strings.Join(args, " ")
		if !slices.Contains(args, "-v") {
			t.Errorf("no -v: %s", got)
		}
		isApply := args[0] == "apply"
		if slices.Contains(args, "--keep-images") != isApply {
			t.Errorf("--keep-images where apply's flags are not, or missing where they are: %s", got)
		}
		if strings.Contains(got, "--min-free 2G") != isApply {
			t.Errorf("--min-free where apply's flags are not, or missing where they are: %s", got)
		}
	}
	if len(*full) == 0 {
		t.Fatal("ran nothing")
	}
}

// A monitor validate lets into etcd.members is founded with the other
// members, since etcd needs it, and is still applied last in pass two, after
// what it checks.
func TestConvergeFoundsAMonitorMemberAndAppliesItLast(t *testing.T) {
	cfg := fixture(t)
	cfg.Etcd.Members = []string{"home-a", "home-b", "vm", "watch"}
	if result := validate.Check(cfg); result.Refused() {
		t.Fatalf("validate refuses a monitor in etcd.members: %v", result.Refusals())
	}
	got := strings.Join(titles(convergePlan(cfg, convergeState{})), "\n")
	want := strings.Join([]string{
		"hosts: host prepare --site home-a",
		"hosts: host prepare --site home-b",
		"hosts: host prepare --site vm",
		"hosts: host prepare --site watch",
		"pull: pull --site home-a",
		"pull: pull --site home-b",
		"pull: pull --site vm",
		"pull: pull --site watch",
		"dns: dns init",
		"founding: apply --site vm",
		"founding: apply --site home-a",
		"founding: apply --site home-b",
		"founding: apply --site watch",
		"storage: storage add",
		"pass two: apply --site home-a",
		"pass two: apply --site home-b",
		"pass two: apply --site vm",
		"pass two: apply --site watch",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A stop running again cannot get past says what can: a conflicting DNS
// record is changed at the provider by hand, and Garage is waited for. Each
// hint stays one short line.
func TestConvergeStopsWithTheHintThatFits(t *testing.T) {
	conflict := fmt.Errorf("dns: 1 conflicting record(s), listed above: %w", dns.ErrConflict)
	waiting := fmt.Errorf("storage add is waiting at stage 3 (sync): blocks are still moving: %w", storageadd.ErrWaiting)
	for _, tc := range []struct {
		step string
		err  error
		want string
	}{
		{"dns init", conflict, "Resolve each DNS conflict above as it says, then run paisans apply --execute again."},
		{"storage add", waiting, "Nothing failed: Garage is still moving data. Run paisans apply --execute again later."},
		{"apply --site vm", errors.New("vm: boom"), "Run paisans apply --execute again to resume."},
	} {
		fakeConverge(t, map[string]error{tc.step: tc.err})
		err := converge(t, "--execute")
		if err == nil {
			t.Fatalf("%s: no error", tc.step)
		}
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: the step's error is not wrapped: %v", tc.step, err)
		}
		lines := strings.Split(err.Error(), "\n")
		if hint := lines[len(lines)-1]; hint != tc.want {
			t.Errorf("%s: hint %q, want %q", tc.step, hint, tc.want)
		}
		if hint := lines[len(lines)-1]; len(hint) > 100 {
			t.Errorf("%s: %d characters: %s", tc.step, len(hint), hint)
		}
	}
}

// apply takes a site only when given --site, so nothing says it needs one:
// not its usage, and not the commands that compare themselves with it.
func TestNothingSaysApplyTakesOneSite(t *testing.T) {
	entry := findCommand("apply").detail
	if !strings.Contains(entry, "Without --site") {
		t.Errorf("apply's usage entry does not say what it does without --site:%s", entry)
	}
	for name, run := range map[string]func([]string) error{"storage init": runStorageInit, "prune": runPrune} {
		var err error
		captureStdout(t, func() { err = run([]string{"--config", fixtureConfig()}) })
		if err == nil || !strings.Contains(err.Error(), "the same reason apply --site takes one") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Every step a real plan holds, with the flags the run adds, is accepted by
// its command's own flag parsing: a flag one command lacks would exit the
// run partway. flagsOnly stops each command once its flags parse, before it
// reads a file or reaches a host.
func TestEveryPlannedStepParses(t *testing.T) {
	cfg := fixture(t)
	steps := convergePlan(cfg, convergeState{NeedsInit: true})
	steps = append(steps, convergePlan(cfg, convergeState{Initial: map[string]render.EtcdInitial{"home-a": initial("home-a", "vm"), "vm": initial("home-a", "vm")}})...)
	one := fixture(t)
	one.Storage.Garage.Sites = []string{"home-a"}
	steps = append(steps, convergePlan(one, convergeState{})...)

	commands := map[string]bool{}
	for _, s := range steps {
		commands[strings.Join(s.Args[:min(2, len(s.Args))], " ")] = true
	}
	// A pull is not a command: the run pulls a site's images itself.
	steps = slices.DeleteFunc(steps, func(s convergeStep) bool { return s.Stage == stagePull })
	for _, want := range []string{"init", "host prepare", "apply --site", "site add", "storage init", "storage add", "dns init"} {
		if !commands[want] {
			t.Fatalf("the plans hold no %s step: %v", want, commands)
		}
	}

	saved := flagsOnly
	flagsOnly = true
	t.Cleanup(func() { flagsOnly = saved })
	for _, o := range []convergeOptions{
		{Config: fixtureConfig()},
		{Config: fixtureConfig(), Secrets: fixtureSecretsPath(), Execute: true, Verbose: true, KeepImages: true, MinFree: "2G"},
	} {
		for _, s := range steps {
			args := convergeFlags(s.Args, o)
			if err := runStep(args); !errors.Is(err, errFlagsOnly) {
				t.Errorf("%s: %v", strings.Join(args, " "), err)
			}
		}
	}
}

// derivedWithNewDataSite is the fixture with cluster.sites and etcd.members
// left out and a site home-c given the data role, so the roles alone make it
// a voter.
func derivedWithNewDataSite(t *testing.T) *config.Config {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := regexp.MustCompile(`(?m)^  (sites|members): \[[^\]]*\]\n`).ReplaceAllString(string(data), "")
	text = strings.Replace(text, "  vm:\n", `  home-c:
    roles: [data]
    address: 10.44.0.5
    endpoint: 198.51.100.5:51820
    ssh:
      host: home-c.local
      user: ubuntu
      keys: [alice]
  vm:
`, 1)
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Etcd.MembersDerived || !slices.Contains(cfg.Etcd.Members, "home-c") {
		t.Fatalf("home-c is not a derived member: %v", cfg.Etcd.Members)
	}
	return cfg
}

// A site given the data role on a founded cluster, with etcd.members left
// out, is a derived member that no founding record lists: it joins through
// site add, as it would written into etcd.members by hand.
func TestConvergeJoinsADerivedMember(t *testing.T) {
	cfg := derivedWithNewDataSite(t)
	founded := initial("home-a", "home-b", "vm")
	steps := convergePlan(cfg, convergeState{Initial: map[string]render.EtcdInitial{"home-a": founded, "home-b": founded, "vm": founded}})
	samePlan(t, steps,
		"hosts: host prepare --site home-a",
		"hosts: host prepare --site home-b",
		"hosts: host prepare --site home-c",
		"hosts: host prepare --site vm",
		"hosts: host prepare --site watch",
		"pull: pull --site home-a",
		"pull: pull --site home-b",
		"pull: pull --site home-c",
		"pull: pull --site vm",
		"pull: pull --site watch",
		"dns: dns init",
		"joining: site add home-c",
		"other sites: apply --site watch",
		"storage: storage add",
		"pass two: apply --site home-a",
		"pass two: apply --site home-b",
		"pass two: apply --site home-c",
		"pass two: apply --site vm",
		"pass two: apply --site watch",
	)
}

// init's line says what it has to do, by name: the subnet, the secrets file,
// or which generated secrets are missing.
func TestConvergeSaysWhatInitIsFor(t *testing.T) {
	fakeConverge(t, nil)
	_, st, err := readConvergeState(fixtureConfig(), filepath.Join(t.TempDir(), "secrets.enc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsInit || st.InitWhy != "no secrets file yet" {
		t.Errorf("NeedsInit %t, why %q", st.NeedsInit, st.InitWhy)
	}
	if got := initWhy(convergeState{NeedsInit: true, InitWhy: "no mesh subnet"}); got != "no mesh subnet" {
		t.Errorf("init's reason %q", got)
	}
}

// Missing generated secrets are named, the first three and a count of the
// rest, never by value.
func TestInitWhyNamesMissingSecrets(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"a"}, "generated secret missing (a)"},
		{[]string{"a", "b", "c"}, "generated secrets missing (a, b, c)"},
		{[]string{"a", "b", "c", "d", "e"}, "generated secrets missing (a, b, c and 2 more)"},
	} {
		if got := missingSecrets(c.names); got != c.want {
			t.Errorf("%v: %q, want %q", c.names, got, c.want)
		}
	}
}

// A path given without --config is refused with the flag to use, not
// ignored for the default paisans-stack.yaml.
func TestApplyRefusesAStrayArgument(t *testing.T) {
	fakeConverge(t, nil)
	err := runApply([]string{"staging/paisans-stack.yaml"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--config staging/paisans-stack.yaml") {
		t.Errorf("err = %v", err)
	}
}

// site remove's usage entry names every flag it takes.
func TestSiteRemoveUsageNamesForceAndSSH(t *testing.T) {
	entry := strings.Join(findCommand("site remove").usage, "\n")
	for _, flag := range []string{"--force", "--ssh"} {
		if !strings.Contains(entry, flag) {
			t.Errorf("site remove's usage leaves out %s:\n%s", flag, entry)
		}
	}
}

// apply --site's usage entry names every flag about one site.
func TestApplySiteUsageNamesEveryFlag(t *testing.T) {
	entry := findCommand("apply").usage[1]
	for _, flag := range []string{"--ssh", "--only", "--overwrite", "--recreate", "--min-free", "--keep-images", "--execute"} {
		if !strings.Contains(entry, flag) {
			t.Errorf("apply --site's usage leaves out %s:\n%s", flag, entry)
		}
	}
}

// `paisans help execute`'s list of the commands that reach a host names secrets prune,
// which reads the gateways' deployment records over ssh.
func TestUsageHostListNamesSecretsPrune(t *testing.T) {
	start := strings.Index(executeTopic, "host prepare, host deployments, apply")
	entry := strings.Join(strings.Fields(executeTopic[start:start+strings.Index(executeTopic[start:], "are the only commands that")]), " ")
	if !strings.Contains(entry, "secrets prune") {
		t.Errorf("the commands that reach a host leave out secrets prune:\n%s", entry)
	}
}

// recordConverge sends the commands' reports to a Recorder for the test.
func recordConverge(t *testing.T) *ui.Recorder {
	t.Helper()
	rec := &ui.Recorder{}
	saved := reporterOverride
	reporterOverride = rec
	t.Cleanup(func() { reporterOverride = saved })
	return rec
}

// Each etcd member's record is read inside a step of its own, so a spinner
// shows while ssh works; the validation findings come first and the plan
// after.
func TestConvergeReadsEachEtcdRecordInAStep(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	var read []string
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		last := rec.Events[len(rec.Events)-1]
		if last.Kind != "step" || last.Text != "read "+m+"'s etcd record" {
			t.Errorf("%s read outside its step; last event %+v", m, last)
		}
		read = append(read, m)
		return render.EtcdInitial{}, false, nil
	}
	for _, args := range [][]string{nil, {"--execute"}} {
		rec.Events = nil
		read = nil
		if err := converge(t, args...); err != nil {
			t.Fatal(err)
		}
		sameLines(t, read, fixture(t).Etcd.Members)
		findings, first := rec.Index("warn", ""), rec.Index("step", "etcd record")
		plan := rec.Index("section", "prepare")
		if findings < 0 || !(findings < first && first < plan) {
			t.Errorf("%v: findings at %d, first read at %d, plan at %d:\n%s", args, findings, first, plan, rec.Lines())
		}
		for _, m := range read {
			if !rec.Has("pending", "read "+m+"'s etcd record") {
				t.Errorf("%v: %s's read step did not end:\n%s", args, m, rec.Lines())
			}
		}
	}
}

// A member with a founding record reads as founded; one without is not a
// success but work the plan will do, so it is marked pending.
func TestEtcdReadsMarkAnUnfoundedMemberPending(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	founded := initial("home-a", "vm")
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		return founded, m == "home-a", nil
	}
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range rec.Events {
		if strings.HasSuffix(e.Text, "etcd record") && e.Kind != "step" {
			got = append(got, e.Kind+" "+e.Text+": "+e.Extra)
		}
	}
	var want []string
	for _, m := range fixture(t).Etcd.Members {
		if m == "home-a" {
			want = append(want, "done read home-a's etcd record: founded")
		} else {
			want = append(want, "pending read "+m+"'s etcd record: not founded yet")
		}
	}
	sameLines(t, got, want)
}

// The run opens with its configuration section: the header once, the
// findings, then init in a dry run when it has work, before the etcd reads.
// The path is not a section, and a warning is not counted.
func TestConvergeFindingsOpenTheConfigurationSection(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	rec := recordConverge(t)
	needsInit := false
	saved := convergeRead
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		cfg, err := config.Load(fixtureConfig())
		return cfg, convergeState{NeedsInit: needsInit, InitWhy: "no secrets file yet"}, err
	}
	t.Cleanup(func() { convergeRead = saved })
	for _, c := range []struct {
		init bool
		args []string
		want []string
	}{
		{true, nil, []string{"section deployment", "step id", "step configuration", "pending configuration", "step core catalog", "section configuration", "warn", "step init", "pending init", "section sites", "step classify the sites", "section architecture", "step read each site's architecture", "step check every image has a build for its site", "section secrets", "step read the deployment record", "section etcd members"}},
		{false, nil, []string{"section deployment", "step id", "step configuration", "pending configuration", "step core catalog", "section configuration", "warn", "section sites", "step classify the sites", "section architecture", "step read each site's architecture", "step check every image has a build for its site", "section secrets", "step read the deployment record", "section mail", "step smtp.example.org:587, starttls, as robot@example.org, from home-a", "step smtp.example.org:587, starttls, as robot@example.org, from home-b", "step smtp.example.org:587, starttls, as robot@example.org, from watch", "section etcd members"}},
		{false, []string{"--execute"}, []string{"section deployment", "step id", "step configuration", "pending configuration", "step core catalog", "section configuration", "warn", "section sites", "step classify the sites", "section architecture", "step read each site's architecture", "step check every image has a build for its site", "section secrets", "step read the deployment record", "section mail", "step smtp.example.org:587, starttls, as robot@example.org, from home-a", "step smtp.example.org:587, starttls, as robot@example.org, from home-b", "step smtp.example.org:587, starttls, as robot@example.org, from watch", "section etcd members"}},
		// init ran first and reported the findings and the secrets itself,
		// under the file's path; they are not reported a second time.
		// The deployment and the version open every run, ahead of both.
		{true, []string{"--execute"}, []string{"section deployment", "step id", "step configuration", "pending configuration", "step core catalog", "section sites", "step classify the sites", "section architecture", "step read each site's architecture", "step check every image has a build for its site", "section mail", "step smtp.example.org:587, starttls, as robot@example.org, from home-a", "step smtp.example.org:587, starttls, as robot@example.org, from home-b", "step smtp.example.org:587, starttls, as robot@example.org, from watch", "section etcd members"}},
	} {
		rec.Events = nil
		needsInit = c.init
		if err := converge(t, c.args...); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range rec.Events {
			line := e.Kind
			switch e.Kind {
			case "warn":
			case "section", "step", "pending":
				line += " " + e.Text
			default:
				continue
			}
			if len(got) > 0 && line == "warn" && got[len(got)-1] == "warn" {
				continue
			}
			got = append(got, line)
			if line == "section etcd members" {
				break
			}
		}
		sameLines(t, got, c.want)
		if n := strings.Count(rec.Lines(), "section: configuration"); n != 1 && c.want[0] == "section configuration" {
			t.Errorf("init %v %v: configuration header %d times:\n%s", c.init, c.args, n, rec.Lines())
		}
		if rec.Has("section", "paisans-stack.yaml") || rec.Has("result", "warning") {
			t.Errorf("init %v %v: the path is a section or a warning is counted:\n%s", c.init, c.args, rec.Lines())
		}
	}
}

// holdRecorder is a Recorder that also records each hold and its resume, as
// a reporter that draws a spinner would pause it.
type holdRecorder struct{ *ui.Recorder }

func (h holdRecorder) Hold() func() {
	h.Events = append(h.Events, ui.Event{Kind: "hold"})
	return func() { h.Events = append(h.Events, ui.Event{Kind: "resume"}) }
}

// recordHolds is recordConverge, with holds recorded.
func recordHolds(t *testing.T) holdRecorder {
	t.Helper()
	h := holdRecorder{&ui.Recorder{}}
	saved := reporterOverride
	reporterOverride = h
	t.Cleanup(func() { reporterOverride = saved })
	return h
}

// A sudo prompt or an ssh host key question during a read pauses the
// spinner of the step that reads.
func TestAHoldDuringAnEtcdReadPausesItsSpinner(t *testing.T) {
	fakeConverge(t, nil)
	h := recordHolds(t)
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		holdOutput()()
		return render.EtcdInitial{}, false, nil
	}
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	for _, m := range fixture(t).Etcd.Members {
		i := h.Index("step", "read "+m+"'s etcd record")
		if i < 0 || i+2 >= len(h.Events) || h.Events[i+1].Kind != "hold" || h.Events[i+2].Kind != "resume" {
			t.Errorf("%s's read was not held around its prompt:\n%s", m, h.Lines())
		}
	}
}

// A member that does not answer stops the plan: founding or joining cannot
// be decided. Its step is marked failed.
func TestAnEtcdMemberThatDoesNotAnswerStopsThePlan(t *testing.T) {
	ran := fakeConverge(t, nil)
	rec := recordConverge(t)
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		return render.EtcdInitial{}, false, errors.New("ssh: connect timed out")
	}
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "connect timed out") {
		t.Fatalf("err = %v", err)
	}
	if !rec.Has("fail", "etcd record") || len(*ran) != 0 {
		t.Errorf("ran %v:\n%s", *ran, rec.Lines())
	}
}

// fakeChecks replaces each step's check with found, by title: a summary, or
// an error. A title found lacks is up to date. It returns the titles checked,
// in order.
func fakeChecks(t *testing.T, found map[string]any) *[]string {
	t.Helper()
	var checked []string
	saved := convergeCheck
	convergeCheck = func(_ ui.Reporter, args []string) (string, []checkReport, error) {
		title := strings.Join(args, " ")
		if i := strings.Index(title, " --config"); i >= 0 {
			title = title[:i]
		}
		if slices.Contains(args, "--execute") {
			t.Errorf("%s checked with --execute", title)
		}
		checked = append(checked, title)
		switch f := found[title].(type) {
		case error:
			return "", nil, f
		case string:
			return f, nil, nil
		}
		return "", nil, nil
	}
	t.Cleanup(func() { convergeCheck = saved })
	return &checked
}

// statuses is each plan step's status as "mark title: result", in order,
// from the events of the dry run's steps but the etcd reads: a prepare line
// as the host prepare it checked, a pull line as its pull, and each install line as the inner steps
// it lists, so the install steps are grouped by line (see byLine).
func statuses(rec *ui.Recorder) []string {
	var out []string
	section := ""
	for _, e := range rec.Events {
		if e.Kind == "section" {
			section = e.Text
			continue
		}
		if section == "install" {
			if e.Kind == "detail" && e.Extra != "" && !strings.HasPrefix(e.Text, " ") {
				out = append(out, e.Text)
			}
			continue
		}
		mark := map[string]string{"done": "✓", "pending": "○", "waiting": "·", "fail": "✗"}[e.Kind]
		if mark == "" || section == "deployment" || section == "sites" || section == "architecture" || section == "pocket id" || section == "secrets" || section == "mail" || section == "review" || strings.Contains(e.Text, "etcd record") || strings.HasSuffix(e.Text, "for an admin") {
			continue
		}
		title := e.Text
		switch section {
		case "prepare":
			title = "host prepare --site " + title
		case "pull":
			title = "pull --site " + title
		}
		line := mark + " " + title
		if e.Extra != "" {
			line += ": " + e.Extra
		}
		out = append(out, line)
	}
	return out
}

// byLine is want, statuses in the plan's order, as the dry run lists them:
// the install steps grouped by the line each is on, the lines in the order
// the plan first reaches them, each line's steps in the plan's order.
func byLine(want []string) []string {
	place := func(l string) string {
		_, title, _ := strings.Cut(l, " ")
		title, _, _ = strings.Cut(title, ":")
		f := strings.Fields(title)
		switch {
		case f[0] == "storage":
			return "storage"
		case f[0] == "apply" || (f[0] == "site" && f[1] == "add"):
			return f[len(f)-1]
		}
		return ""
	}
	var out, places []string
	group := map[string][]string{}
	flush := func() {
		for _, p := range places {
			out = append(out, group[p]...)
		}
		places, group = nil, map[string][]string{}
	}
	for _, l := range want {
		p := place(l)
		if p == "" {
			flush()
			out = append(out, l)
			continue
		}
		if _, ok := group[p]; !ok {
			places = append(places, p)
		}
		group[p] = append(group[p], l)
	}
	flush()
	return out
}

// A deployment that is up to date checks every command once, each line ✓,
// and an apply in pass two takes its earlier apply's status.
func TestConvergeDryRunMarksAnUpToDateDeploymentDone(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeChecks(t, nil)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, l := range blankPlan() {
		stage, title, _ := strings.Cut(l, ": ")
		if stage == "pull" {
			title += ": nothing to pull"
		}
		want = append(want, "✓ "+title)
	}
	sameLines(t, statuses(rec), byLine(want))
	seen := map[string]bool{}
	for _, c := range *checked {
		if seen[c] {
			t.Errorf("%s checked twice", c)
		}
		seen[c] = true
	}
}

// A pending step says what it would change, and what waits on it says so,
// unchecked. A check that fails is marked with its error and the rest are
// still checked.
func TestConvergeDryRunMarksPendingWaitingAndFailed(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeChecks(t, map[string]any{
		"host prepare --site home-b": "docker, firewall",
		"apply --site vm":            "5 changes",
		"host prepare --site watch":  errors.New("ssh: connect to host watch.local: timed out\nmore detail"),
		"dns init":                   "2 records to create",
	})
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	sameLines(t, statuses(rec), byLine([]string{
		"✓ host prepare --site home-a",
		"○ host prepare --site home-b: docker, firewall",
		"✓ host prepare --site vm",
		"✗ host prepare --site watch: ssh: connect to host watch.local: timed out",
		// A pull line waiting on its prepare ends first, unchecked.
		"· pull --site home-b: after host prepare --site home-b",
		"· pull --site watch: after host prepare --site watch",
		"✓ pull --site home-a: nothing to pull",
		"✓ pull --site vm: nothing to pull",
		"○ dns init: 2 records to create",
		"○ apply --site vm: 5 changes",
		"· apply --site home-a: after apply --site vm",
		"· apply --site home-b: after host prepare --site home-b",
		"· apply --site watch: after host prepare --site watch",
		"· storage add: after host prepare --site home-b",
		"· apply --site home-a: after the earlier apply --site home-a",
		"· apply --site home-b: after host prepare --site home-b",
		"· apply --site vm: after the earlier apply --site vm",
		"· apply --site watch: after host prepare --site watch",
	}))
	for _, never := range []string{"apply --site home-a", "apply --site home-b", "apply --site watch", "storage add"} {
		if slices.Contains(*checked, never) {
			t.Errorf("%s was checked while it waits", never)
		}
	}
}

// With init to run, every other step waits on it: it writes the subnet and
// the secrets they read.
func TestConvergeDryRunWaitsOnInit(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeChecks(t, nil)
	rec := recordConverge(t)
	saved := convergeRead
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		cfg, err := config.Load(fixtureConfig())
		return cfg, convergeState{NeedsInit: true, InitWhy: "no secrets file yet"}, err
	}
	t.Cleanup(func() { convergeRead = saved })
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	got := statuses(rec)
	if len(got) == 0 || got[0] != "○ init: no secrets file yet" {
		t.Fatalf("init's line: %v", got)
	}
	for _, l := range got[1:] {
		if !strings.HasPrefix(l, "· ") || !strings.HasSuffix(l, ": after init") {
			t.Errorf("does not wait on init: %s", l)
		}
	}
	if len(*checked) != 0 {
		t.Errorf("checked %v", *checked)
	}
}

// A founded cluster's members are applied first in pass two, so that apply
// is checked there, after storage.
func TestConvergeDryRunChecksAFoundedMemberInPassTwo(t *testing.T) {
	ran := fakeConverge(t, nil)
	_ = ran
	founded := initial("home-a", "home-b", "vm")
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) { return founded, true, nil }
	checked := fakeChecks(t, map[string]any{"storage add": "1 stage"})
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	sameLines(t, statuses(rec), byLine([]string{
		"✓ host prepare --site home-a",
		"✓ host prepare --site home-b",
		"✓ host prepare --site vm",
		"✓ host prepare --site watch",
		"✓ pull --site home-a: nothing to pull",
		"✓ pull --site home-b: nothing to pull",
		"✓ pull --site vm: nothing to pull",
		"✓ pull --site watch: nothing to pull",
		"✓ dns init",
		"✓ apply --site watch",
		"○ storage add: 1 stage",
		"· apply --site home-a: after storage add",
		"· apply --site home-b: after storage add",
		"· apply --site vm: after storage add",
		"· apply --site watch: after storage add",
	}))
	if !slices.Contains(*checked, "storage add") {
		t.Errorf("storage add not checked: %v", *checked)
	}
}

// A step is checked through its own dry run, quietly: nothing it reports
// reaches the run's reporter, a hold for a prompt pauses the run's spinner,
// and once it returns the run's reporter is the commands' again.
func TestCheckStepIsQuietAndHoldsTheRunsSpinner(t *testing.T) {
	h := recordHolds(t)
	routeHolds(h)
	run := func(args []string) error {
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		reporter := commonFlags(fs)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		r := reporter()
		r.Section("its own plan")
		r.Step("write 8 files").End(ui.Pending, "")
		holdOutput()()
		dryRunFound("8 changes", nil)
		r.Result("Nothing changed.")
		return nil
	}
	summary, _, err := checkStep(h, []string{"apply"}, run)
	if err != nil || summary != "8 changes" {
		t.Fatalf("summary %q, err %v", summary, err)
	}
	var kinds []string
	for _, e := range h.Events {
		kinds = append(kinds, e.Kind)
	}
	sameLines(t, kinds, []string{"hold", "resume"})
	if reporterOverride != ui.Reporter(h) {
		t.Errorf("the reporter was not given back: %T", reporterOverride)
	}
	holdOutput()()
	if n := len(h.Events); n != 4 {
		t.Errorf("a hold after the check did not reach the run's reporter:\n%s", h.Lines())
	}
}

// A check fails with the command's error, with what its dry run found
// --execute would stop at, or when the command never said what it would
// change.
func TestCheckStepFails(t *testing.T) {
	recordConverge(t)
	conflict := fmt.Errorf("1 record conflicts with the provider's: %w", dns.ErrConflict)
	for name, run := range map[string]func([]string) error{
		"error":   func([]string) error { return errors.New("ssh: timed out") },
		"stop":    func([]string) error { dryRunFound("", conflict); return nil },
		"silence": func([]string) error { return nil },
	} {
		summary, _, err := checkStep(ui.Discard, []string{"dns", "init"}, run)
		if err == nil || summary != "" {
			t.Errorf("%s: summary %q, err %v", name, summary, err)
		}
	}
	if _, _, err := checkStep(ui.Discard, []string{"dns", "init"}, func([]string) error { dryRunFound("", conflict); return nil }); !errors.Is(err, dns.ErrConflict) {
		t.Errorf("the stop is not the error: %v", err)
	}
}

// Each command's dry run says what it would change: host prepare on an
// empty host, through the fake ssh, names each kind of change it would make,
// in a fixed order, and storage add counts its steps.
func TestHostPrepareDryRunSaysWhatItWouldChange(t *testing.T) {
	recordConverge(t)
	quietSSH(t)
	o := convergeOptions{Config: fixtureConfig(), Secrets: fixtureSecretsPath()}
	for args, want := range map[string]*regexp.Regexp{
		"host prepare --site home-a": regexp.MustCompile(`^docker, ssh keys, configuration store$`),
		"storage add":                regexp.MustCompile(`^[1-9][0-9]* [a-z]+s?$`),
	} {
		summary, _, err := checkStep(ui.Discard, convergeFlags(strings.Fields(args), o), runStep)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !want.MatchString(summary) {
			t.Errorf("%v: summary %q", args, summary)
		}
	}
}

// apply's dry run says what it would change, and a plan --execute would
// refuse, here for an image volume its service does not mount, is the
// check's failure rather than pending work.
func TestApplyDryRunSaysWhatItWouldChangeOrRefuse(t *testing.T) {
	recordConverge(t)
	quietSSH(t)
	o := convergeOptions{Config: fixtureConfig(), Secrets: fixtureSecretsPath()}
	args := []string{"apply", "--site", "watch"}
	summary, _, err := checkStep(ui.Discard, convergeFlags(args, o), runStep)
	if err != nil || !regexp.MustCompile(`^[1-9][0-9]* changes?$`).MatchString(summary) {
		t.Errorf("summary %q, err %v", summary, err)
	}
	unmounted := strings.Replace(quietHost, `echo "volumes $r null"`, `echo "volumes $r {\"/unmounted\":{}}"`, 1)
	if unmounted == quietHost {
		t.Fatal("the fake host no longer answers the volumes probe")
	}
	sshAnswering(t, unmounted)
	summary, _, err = checkStep(ui.Discard, convergeFlags(args, o), runStep)
	if err == nil || summary != "" {
		t.Errorf("a plan --execute refuses was %q, err %v", summary, err)
	}
}

// convergeShaped is converge on the fixture as edit changes it, with every
// etcd member reading as recorded holds it.
func convergeShaped(t *testing.T, edit func(*config.Config), recorded map[string]render.EtcdInitial) error {
	t.Helper()
	saved := convergeRead
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		cfg, err := config.Load(fixtureConfig())
		if err == nil {
			edit(cfg)
			if result := validate.Check(cfg); result.Refused() {
				t.Fatalf("validate refuses the edited fixture: %v", result.Refusals())
			}
		}
		return cfg, convergeState{}, err
	}
	t.Cleanup(func() { convergeRead = saved })
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		in, ok := recorded[m]
		return in, ok, nil
	}
	return converge(t)
}

// What each step waits on, for each shape of deployment: a member joining
// through site add, a witness with its record before the data members are
// founded, one Garage site, no etcd, no Garage, and a monitor in
// etcd.members.
func TestConvergeDryRunWaitsFollowTheDeployment(t *testing.T) {
	founded := initial("home-a", "vm")
	for _, c := range []struct {
		name     string
		edit     func(*config.Config)
		recorded map[string]render.EtcdInitial
		found    map[string]any
		want     []string
	}{
		{
			name:     "joining",
			edit:     func(*config.Config) {},
			recorded: map[string]render.EtcdInitial{"home-a": founded, "vm": founded},
			found:    map[string]any{"site add home-b": "4 stages"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "✓ host prepare --site vm", "✓ host prepare --site watch",
				"✓ dns init",
				"○ site add home-b: 4 stages",
				"✓ apply --site watch",
				"· storage add: after site add home-b",
				"· apply --site home-a: after storage add",
				"· apply --site home-b: after site add home-b",
				"· apply --site vm: after storage add",
				"· apply --site watch: after storage add",
			},
		},
		{
			name:     "a recorded witness",
			edit:     func(*config.Config) {},
			recorded: map[string]render.EtcdInitial{"vm": initial("home-a", "home-b", "vm")},
			found:    map[string]any{"apply --site vm": "1 change"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "✓ host prepare --site vm", "✓ host prepare --site watch",
				"✓ dns init",
				"○ apply --site vm: 1 change",
				"· apply --site home-a: after apply --site vm",
				"· apply --site home-b: after apply --site vm",
				"✓ apply --site watch",
				"· storage add: after apply --site home-a",
				"· apply --site home-a: after the earlier apply --site home-a",
				"· apply --site home-b: after the earlier apply --site home-b",
				"· apply --site vm: after the earlier apply --site vm",
				"· apply --site watch: after storage add",
			},
		},
		{
			name: "one Garage site",
			edit: func(cfg *config.Config) {
				cfg.Storage.Garage.Sites = []string{"home-a"}
				cfg.Storage.Garage.Replication = 1
			},
			found: map[string]any{"apply --site home-b": "2 changes", "apply --site home-a": "3 changes"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "✓ host prepare --site vm", "✓ host prepare --site watch",
				"✓ dns init",
				"✓ apply --site vm",
				"○ apply --site home-a: 3 changes",
				"○ apply --site home-b: 2 changes",
				"✓ apply --site watch",
				"· storage init --site home-a: after apply --site home-a",
				"· apply --site home-a: after the earlier apply --site home-a",
				"· apply --site home-b: after the earlier apply --site home-b",
				"· apply --site vm: after storage init --site home-a",
				"· apply --site watch: after storage init --site home-a",
			},
		},
		{
			name:  "no etcd",
			edit:  func(cfg *config.Config) { cfg.Etcd.Members = nil },
			found: map[string]any{"host prepare --site vm": "ssh keys"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "○ host prepare --site vm: ssh keys", "✓ host prepare --site watch",
				"✓ dns init",
				"✓ apply --site home-a",
				"✓ apply --site home-b",
				"· apply --site vm: after host prepare --site vm",
				"✓ apply --site watch",
				"✓ storage add",
				"✓ apply --site home-a",
				"✓ apply --site home-b",
				"· apply --site vm: after host prepare --site vm",
				"✓ apply --site watch",
			},
		},
		{
			name: "no Garage",
			// With no Garage every app that can use S3 keeps its files on
			// its own disk, which only a pinned app may do.
			edit: func(cfg *config.Config) {
				cfg.Storage.Garage.Sites = nil
				for _, name := range []string{"talk", "wiki"} {
					app := cfg.Apps[name]
					app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
					cfg.Apps[name] = app
				}
			},
			found: map[string]any{"apply --site watch": "1 change"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "✓ host prepare --site vm", "✓ host prepare --site watch",
				"✓ dns init",
				"✓ apply --site vm",
				"✓ apply --site home-a",
				"✓ apply --site home-b",
				"○ apply --site watch: 1 change",
				"✓ apply --site home-a",
				"✓ apply --site home-b",
				"✓ apply --site vm",
				"· apply --site watch: after the earlier apply --site watch",
			},
		},
		{
			name:  "a monitor in etcd.members",
			edit:  func(cfg *config.Config) { cfg.Etcd.Members = []string{"home-a", "home-b", "vm", "watch"} },
			found: map[string]any{"apply --site vm": "6 changes"},
			want: []string{
				"✓ host prepare --site home-a", "✓ host prepare --site home-b", "✓ host prepare --site vm", "✓ host prepare --site watch",
				"✓ dns init",
				"○ apply --site vm: 6 changes",
				"· apply --site home-a: after apply --site vm",
				"· apply --site home-b: after apply --site vm",
				"· apply --site watch: after apply --site vm",
				"· storage add: after apply --site home-a",
				"· apply --site home-a: after the earlier apply --site home-a",
				"· apply --site home-b: after the earlier apply --site home-b",
				"· apply --site vm: after the earlier apply --site vm",
				"· apply --site watch: after the earlier apply --site watch",
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeConverge(t, nil)
			fakeChecks(t, c.found)
			rec := recordConverge(t)
			if err := convergeShaped(t, c.edit, c.recorded); err != nil {
				t.Fatal(err)
			}
			sameLines(t, statuses(rec), byLine(withPulls(c.want)))
		})
	}
}

// withPulls is a dry run's statuses with each site's pull line put in after
// the prepare lines, as the dry run draws them: a pull whose prepare is up
// to date has nothing to pull, here, and one whose prepare is not waits on
// it, unchecked, and so ends first.
func withPulls(want []string) []string {
	var waiting, checked []string
	end := 0
	for i, l := range want {
		mark, title, _ := strings.Cut(l, " ")
		site, isPrepare := strings.CutPrefix(title, "host prepare --site ")
		if !isPrepare {
			continue
		}
		site, _, _ = strings.Cut(site, ":")
		if mark == "✓" {
			checked = append(checked, "✓ pull --site "+site+": nothing to pull")
		} else {
			waiting = append(waiting, "· pull --site "+site+": after host prepare --site "+site)
		}
		end = i + 1
	}
	return slices.Concat(want[:end], waiting, checked, want[end:])
}

// editedFixture is the fixture configuration with each old replaced by its
// new, written where the secrets fixture is still named by path.
func editedFixture(t *testing.T, pairs ...string) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for i := 0; i < len(pairs); i += 2 {
		edited := strings.Replace(text, pairs[i], pairs[i+1], 1)
		if edited == text {
			t.Fatalf("the fixture has no %q", pairs[i])
		}
		text = edited
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// storage init says what it would change, through its own dry run against
// a Garage behind the fake ssh with no layout, key or bucket yet.
func TestStorageInitDryRunSaysWhatItWouldChange(t *testing.T) {
	recordConverge(t)
	garage := strings.Replace(quietHost, `*"getent passwd"*)`, `*" layout show"*) echo "Current cluster layout version: 0";;
*" node id -q"*) echo "0123456789abcdef0123@10.44.0.1:3901";;
*" key info "*) echo "0 matching keys";;
*" bucket info "*) echo "Bucket not found";;
*"getent passwd"*)`, 1)
	sshAnswering(t, garage)
	one := editedFixture(t, "sites: [home-a, home-b]\n    replication: 2", "sites: [home-a]\n    replication: 1")
	o := convergeOptions{Config: one, Secrets: fixtureSecretsPath()}
	summary, _, err := checkStep(ui.Discard, convergeFlags([]string{"storage", "init", "--site", "home-a"}, o), runStep)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[1-9][0-9]* changes?$`).MatchString(summary) {
		t.Errorf("summary %q", summary)
	}
}

// init, run as a step, points the holds at its own reporter. The etcd reads
// after it hold the run's spinner again.
func TestHoldsReturnToTheRunAfterInit(t *testing.T) {
	fakeConverge(t, nil)
	h := recordHolds(t)
	saved, savedRun := convergeRead, convergeRun
	reads := 0
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		reads++
		cfg, err := config.Load(fixtureConfig())
		return cfg, convergeState{NeedsInit: reads == 1}, err
	}
	convergeRun = func(args []string) error {
		if args[0] == "init" {
			routeHolds(ui.Discard)
		}
		return nil
	}
	t.Cleanup(func() { convergeRead, convergeRun = saved, savedRun })
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) {
		holdOutput()()
		return render.EtcdInitial{}, false, nil
	}
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !h.Has("hold", "") {
		t.Errorf("a prompt during an etcd read after init did not hold the run's spinner:\n%s", h.Lines())
	}
}

// What a step's own dry run reports while it is checked is collected rather
// than dropped: its warnings, notes, refusals and ssh retries.
func TestCheckStepCollectsWhatTheDryRunReports(t *testing.T) {
	t.Cleanup(func() { apply.SetRetryLog(nil) })
	rec := &ui.Recorder{Verbose_: true}
	summary, reports, err := checkStep(rec, []string{"apply", "--site", "home-a"}, func([]string) error {
		inner := reporterOverride
		inner.Warn("held back from this apply", "talk's image is newer than its pin")
		inner.Note("secrets: smtp.password is owed", "paisans secrets set smtp.password < value")
		inner.Refuse("garage is not running", "start it first")
		routeRetries(inner)
		retryOnce(t)
		dryRunFound("3 changes", nil)
		return nil
	})
	if err != nil || summary != "3 changes" {
		t.Fatalf("summary %q, err %v", summary, err)
	}
	want := []checkReport{
		{Text: "! held back from this apply: talk's image is newer than its pin"},
		{Note: true, Text: "secrets: smtp.password is owed", Detail: "paisans secrets set smtp.password < value"},
		{Text: "✗ garage is not running: start it first"},
	}
	if len(reports) != 4 || !reflect.DeepEqual(reports[:3], want) || !strings.Contains(reports[3].Text, "ssh could not connect") {
		t.Errorf("reports:\n%#v", reports)
	}
	if len(rec.Events) != 0 {
		t.Errorf("the check drew on the run's reporter:\n%s", rec.Lines())
	}
}

// A check's reports print under its status line: everything but a note as a
// detail of the line, which only -v shows, and a note after the line at
// every verbosity.
func TestCheckReportsShowUnderTheStep(t *testing.T) {
	fakeConverge(t, nil)
	saved := convergeCheck
	convergeCheck = func(_ ui.Reporter, args []string) (string, []checkReport, error) {
		if strings.Join(args[:3], " ") == "apply --site vm" {
			return "5 changes", []checkReport{{Text: "! held back from this apply"}, {Note: true, Text: "owed", Detail: "paisans secrets set x"}}, nil
		}
		return "", nil, nil
	}
	t.Cleanup(func() { convergeCheck = saved })
	rec := withRecorder(t, true)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	pending := slices.IndexFunc(rec.Events, func(e ui.Event) bool { return e.Kind == "pending" && e.Text == "vm" })
	inner := slices.IndexFunc(rec.Events, func(e ui.Event) bool {
		return e.Kind == "detail" && e.Extra == "vm" && e.Text == "○ apply --site vm: 5 changes"
	})
	detail := slices.IndexFunc(rec.Events, func(e ui.Event) bool {
		return e.Kind == "detail" && e.Extra == "vm" && e.Text == "  ! held back from this apply"
	})
	note := slices.IndexFunc(rec.Events, func(e ui.Event) bool { return e.Kind == "note" && e.Text == "owed" })
	if pending < 0 || inner < 0 || detail != inner+1 || note < 0 || detail > pending || note < pending {
		t.Errorf("pending %d, inner %d, detail %d, note %d:\n%s", pending, inner, detail, note, rec.Lines())
	}
}

// A secrets file that exists but does not read (no age key, say) stops the
// run at once, before any host is read: init cannot fix it, since init needs
// the same key.
func TestConvergeStopsOnASecretsFileThatDoesNotRead(t *testing.T) {
	fakeConverge(t, nil)
	read := false
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) {
		read = true
		return render.EtcdInitial{}, false, nil
	}
	unreadable := t.TempDir() // a directory: present, and not a secrets file
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", fixtureConfig(), "--secrets", unreadable}, strings.NewReader(""), io.Discard)
	})
	if err == nil || !strings.Contains(err.Error(), unreadable) {
		t.Errorf("err = %v, want it to name %s", err, unreadable)
	}
	if read {
		t.Error("a host was read with secrets that do not read")
	}
}

// A stop ends the run with a Problem naming the step. Its explanation is the
// step's own problem, hint and explanation, and then how to resume, so the
// reason shows without -v.
func TestConvergeStopIsAProblem(t *testing.T) {
	unreachable := &ui.Problem{Hint: "ubuntu@203.0.113.10 cannot be reached over ssh", Explain: "Check that the host is up.", Cause: apply.ErrUnreachable}
	waiting := &storageadd.Waiting{Stage: &storageadd.Stage{Number: 3, Name: "sync"}, Detail: "blocks are still moving"}
	for _, tc := range []struct {
		step    string
		err     error
		hint    string
		explain []string
		code    int
	}{
		{"apply --site vm", unreachable, "apply stopped at apply --site vm", []string{"ubuntu@203.0.113.10 cannot be reached over ssh.", "Check that the host is up.", "Run paisans apply --execute again to resume."}, 1},
		{"apply --site vm", errors.New("vm: boom"), "apply stopped at apply --site vm", []string{"vm: boom.", "Run paisans apply --execute again to resume."}, 1},
		{"apply --site vm", errors.New("apply: the rest of vm was applied, but talk was refused, and stays held back until its client at Pocket ID is fixed:\n  talk: no client"), "apply stopped at apply --site vm", []string{"apply: the rest of vm was applied, but talk was refused, and stays held back until its client at Pocket ID is fixed:", "  talk: no client", "Run paisans apply --execute again to resume."}, 1},
		{"storage add", waiting, "apply is waiting at storage add", []string{"storage add is waiting on Garage at stage 3 (sync): blocks are still moving.", "Nothing failed: Garage is still moving data. Run paisans apply --execute again later."}, 75},
	} {
		fakeConverge(t, map[string]error{tc.step: tc.err})
		err := converge(t, "--execute")
		var p *ui.Problem
		if !errors.As(err, &p) {
			t.Fatalf("%s: not a ui.Problem: %v", tc.step, err)
		}
		if p.Hint != tc.hint || p.Explain != strings.Join(tc.explain, "\n") {
			t.Errorf("%s: got %q\n%q", tc.step, p.Hint, p.Explain)
		}
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: the step's error is not wrapped", tc.step)
		}
		var b strings.Builder
		if code := reportError(&b, err, false); code != tc.code {
			t.Errorf("%s: exit %d, want %d", tc.step, code, tc.code)
		}
	}
}

// A hint is ended with a period, but one that ends with a colon introduces
// the lines after it and is kept as it is.
func TestSentencesEndAHintOnce(t *testing.T) {
	for in, want := range map[string]string{
		"vm: boom":                             "vm: boom.",
		"vm: boom.":                            "vm: boom.",
		"talk was refused:\n  talk: no client": "talk was refused:\n  talk: no client",
	} {
		if got := sentences(errors.New(in)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A dry run's failed step says what to do at every verbosity, not only its
// hint: the explanation follows the step's line.
func TestConvergeDryRunShowsAFailedStepsExplanation(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, map[string]any{
		"host prepare --site watch": &ui.Problem{Hint: "watch's etcd members differ from the running cluster's", Explain: "Run paisans site add for the site being added, then apply."},
	})
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	i := rec.Index("explain", "Run paisans site add for the site being added, then apply.")
	if i < 0 {
		t.Errorf("no explanation:\n%s", rec.Lines())
	}
	if f := rec.Index("fail", "watch"); f < 0 || f+1 != i {
		t.Errorf("the explanation does not follow the step's line:\n%s", rec.Lines())
	}
}

// The founding stop is expected, so it is no failure and no note: the
// install line lists it as its inner step's result, and its reason under it,
// without a doubled stop or a trailing blank.
func TestTheFoundingStopIsAnInnerResult(t *testing.T) {
	fakeConverge(t, map[string]error{"apply --site home-a": foundingWait("home-a", "home-b")})
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rec.Events, func(e ui.Event) bool {
		return e.Kind == "detail" && e.Extra == "home-a" && e.Text == "✓ apply --site home-a: paused until home-b runs etcd"
	})
	if i < 0 || rec.Events[i+1].Text != "  home-a is waiting on home-b to start etcd." {
		t.Errorf("inner result:\n%s", rec.Lines())
	}
	if rec.Has("note", "founding") || rec.Has("fail", "") || !rec.Has("done", "home-a") {
		t.Errorf("the founding stop read as a failure or a note:\n%s", rec.Lines())
	}
}

// An --ssh that is not a destination is said once, plainly.
func TestSSHFlagProblemSaysItOnce(t *testing.T) {
	err := sshFlagProblem("myalias", errors.New(`"myalias" is not user@host[:port]`))
	var p *ui.Problem
	if !errors.As(err, &p) || p.Hint != "--ssh is not a destination" {
		t.Errorf("got %#v", p)
	}
}

// fakeDeclaredOperators replaces making a declared operator: answer gives the
// outcome for each app and admin. It returns the calls, as "app/id", with
// "!" after one that executes.
func fakeDeclaredOperators(t *testing.T, answer func(app, id string, execute bool) (declaredOperator, error)) *[]string {
	t.Helper()
	var asked []string
	saved := convergeDeclaredOperator
	convergeDeclaredOperator = func(cfg *config.Config, app, id string, o convergeOptions, execute bool) (declaredOperator, error) {
		call := app + "/" + id
		if execute {
			call += "!"
		}
		asked = append(asked, call)
		d, err := answer(app, id, execute)
		d.App, d.ID, d.Email = app, id, cfg.Operators[id].Email
		return d, err
	}
	t.Cleanup(func() { convergeDeclaredOperator = saved })
	return &asked
}

// registered is an admin who has a passkey: nothing to do.
func registered(string, string, bool) (declaredOperator, error) { return declaredOperator{}, nil }

// unregistered is an admin with no account yet: created, linked, and emailed
// when emailed is set.
func unregistered(emailed bool) func(string, string, bool) (declaredOperator, error) {
	return func(_, id string, execute bool) (declaredOperator, error) {
		d := declaredOperator{Titles: []string{"create administrator " + id, "issue login link for " + id}}
		if execute {
			d.Created, d.Link, d.ExpiresIn, d.Emailed = true, "https://id.example.org/lc/tok-not-real-"+id, operatorLinkTTL, emailed
		}
		return d, nil
	}
}

// fakeEmailFailures replaces reading Pocket ID's log: it answers failed, by
// address, and returns the addresses asked about.
func fakeEmailFailures(t *testing.T, failed map[string]string, err error) *[]string {
	t.Helper()
	var asked []string
	saved := convergeEmailFailures
	convergeEmailFailures = func(_ *config.Config, _ string, _ convergeOptions, addresses []string) (map[string]string, error) {
		asked = append(asked, addresses...)
		return failed, err
	}
	t.Cleanup(func() { convergeEmailFailures = saved })
	return &asked
}

// recordLinks captures what is printed as login links.
func recordLinks(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	saved := loginLinkOut
	loginLinkOut = &b
	t.Cleanup(func() { loginLinkOut = saved })
	return &b
}

// A dry run in which a step that brings Pocket ID up is not up to date asks
// nothing about its admins: the app is not running yet.
func TestConvergeDryRunAsksNoAdminBeforePocketIDRuns(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, map[string]any{"apply --site home-b": "2 changes"})
	asked := fakeDeclaredOperators(t, unregistered(false))
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 || rec.Has("step", "alice on auth") {
		t.Errorf("asked %v:\n%s", *asked, rec.Lines())
	}
}

// Init with work is a step that brings Pocket ID up, so nothing is asked.
func TestConvergeDryRunAsksNoAdminWhileInitHasWork(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	asked := fakeDeclaredOperators(t, unregistered(false))
	saved := convergeRead
	convergeRead = func(c, s string) (*config.Config, convergeState, error) {
		cfg, st, err := readConvergeState(c, s)
		st.NeedsInit, st.InitWhy = true, "no mesh subnet"
		return cfg, st, err
	}
	t.Cleanup(func() { convergeRead = saved })
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 {
		t.Errorf("asked %v:\n%s", *asked, rec.Lines())
	}
}

// With Pocket ID up, a dry run plans each declared operator on a line of its
// own, pending with what would happen, and issues nothing.
func TestConvergeDryRunPlansEachDeclaredAdmin(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, map[string]any{"apply --site watch": "1 change"})
	asked := fakeDeclaredOperators(t, unregistered(false))
	links := recordLinks(t)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, []string{"auth/alice", "auth/bob"}) {
		t.Errorf("asked %v", *asked)
	}
	if !rec.Has("pending", "alice on auth") || !rec.Has("pending", "bob on auth") {
		t.Errorf("no pending line for alice:\n%s", rec.Lines())
	}
	if links.Len() != 0 {
		t.Errorf("a dry run printed links: %q", links.String())
	}
}

// An admin with a passkey is marked registered.
func TestConvergeDryRunMarksARegisteredAdmin(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	fakeDeclaredOperators(t, registered)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("done", "alice on auth") {
		t.Errorf("alice is not marked done:\n%s", rec.Lines())
	}
}

// A run makes each admin and prints each link once, after the result, with a
// blank line before the heading and after the list to set it apart from what
// is around it.
func TestConvergeRunPrintsEachLinkAfterTheResult(t *testing.T) {
	fakeConverge(t, nil)
	asked := fakeDeclaredOperators(t, unregistered(false))
	links := recordLinks(t)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*asked, []string{"auth/alice!", "auth/bob!"}) {
		t.Errorf("asked %v", *asked)
	}
	if !rec.Has("done", "alice on auth") || !rec.Has("result", "Deployment complete.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
	want := "\nLogin links, each valid for 24 hours and for one sign in. Treat each as a password:\n  alice  https://id.example.org/lc/tok-not-real-alice\n  bob    https://id.example.org/lc/tok-not-real-bob\n\n"
	if links.String() != want {
		t.Errorf("links %q\nwant %q", links.String(), want)
	}
	if strings.Contains(rec.Lines(), "tok-not-real") {
		t.Errorf("a link reached the report:\n%s", rec.Lines())
	}
}

// A registered admin gets no link and the run prints none.
func TestConvergeRunPrintsNoLinkForARegisteredAdmin(t *testing.T) {
	fakeConverge(t, nil)
	fakeDeclaredOperators(t, registered)
	links := recordLinks(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if links.Len() != 0 {
		t.Errorf("printed %q", links.String())
	}
}

// An emailed admin's line says sent when Pocket ID logged no failure, and
// failed with Pocket ID's error when it did. Neither fails the run.
func TestConvergeRunReportsEachEmail(t *testing.T) {
	for _, tc := range []struct {
		failed map[string]string
		mark   string
		result string
	}{
		{nil, "done", "sent"},
		{map[string]string{"alice@example.org": "dial tcp: i/o timeout"}, "fail", "failed: dial tcp: i/o timeout"},
	} {
		fakeConverge(t, nil)
		fakeDeclaredOperators(t, unregistered(true))
		asked := fakeEmailFailures(t, tc.failed, nil)
		recordLinks(t)
		rec := recordConverge(t)
		if err := converge(t, "--execute"); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(*asked, []string{"alice@example.org", "bob@example.org"}) {
			t.Errorf("asked about %v", *asked)
		}
		found := false
		for _, e := range rec.Events {
			if e.Kind == tc.mark && e.Text == "email alice a login link" && strings.Contains(e.Extra, tc.result) {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s line saying %q:\n%s", tc.mark, tc.result, rec.Lines())
		}
	}
}

// When every failed email failed the same way, each operator's line says
// failed and the cause, cut to what Pocket ID's error means, is said once.
func TestConvergeRunSaysASharedEmailFailureOnce(t *testing.T) {
	why := "failed to configure emailer: failed to init email sender 'smtp': invalid connection string: missing SMTP password; required format is 'smtp://<username>:<password>@<host>:<port>'"
	fakeConverge(t, nil)
	fakeDeclaredOperators(t, unregistered(true))
	fakeEmailFailures(t, map[string]string{"alice@example.org": why, "bob@example.org": why}, nil)
	recordLinks(t)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	lines := rec.Lines()
	for _, id := range []string{"alice", "bob"} {
		found := false
		for _, e := range rec.Events {
			if e.Kind == "fail" && e.Text == "email "+id+" a login link" && e.Extra == "failed" {
				found = true
			}
		}
		if !found {
			t.Errorf("no bare failed line for %s:\n%s", id, lines)
		}
	}
	if n := strings.Count(lines, "missing SMTP password"); n != 1 {
		t.Errorf("the cause is said %d times, want once:\n%s", n, lines)
	}
	for _, noise := range []string{"required format", "failed to configure emailer"} {
		if strings.Contains(lines, noise) {
			t.Errorf("output keeps %q:\n%s", noise, lines)
		}
	}
	if !strings.Contains(lines, "paisans secrets list") {
		t.Errorf("no fix for the missing password:\n%s", lines)
	}
}

// An admin who could not be made fails the run, and a link already issued is
// still printed.
func TestConvergeRunFailsOnAnAdminAndStillPrintsLinks(t *testing.T) {
	fakeConverge(t, nil)
	fakeDeclaredOperators(t, func(app, id string, execute bool) (declaredOperator, error) {
		if id == "bob" {
			return declaredOperator{}, errors.New("POST /api/users: 400 email already in use")
		}
		return unregistered(false)(app, id, execute)
	})
	links := recordLinks(t)
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("err = %v", err)
	}
	if rec.Has("result", "Deployment complete.") {
		t.Error("said complete")
	}
	if !strings.Contains(links.String(), "alice") {
		t.Errorf("links %q", links.String())
	}
}

// A run that stopped asks about no admin.
func TestConvergeStoppedRunAsksNoAdmin(t *testing.T) {
	fakeConverge(t, map[string]error{"apply --site vm": errors.New("vm: boom")})
	asked := fakeDeclaredOperators(t, unregistered(false))
	if err := converge(t, "--execute"); err == nil {
		t.Fatal("the run did not stop")
	}
	if len(*asked) != 0 {
		t.Errorf("asked %v", *asked)
	}
}

// apply asks for each declared operator as the table in the spec says: the key
// as the username, the name split at its first space, verified, a 24 hour
// link, emailed when the deployment has SMTP.
func TestOperatorRequest(t *testing.T) {
	cfg := fixture(t)
	cfg.Operators["alice"] = config.Operator{Name: "Alice van Example", Email: "alice@example.org"}
	req := operatorRequest(cfg, "auth", "alice")
	if req.Username != "alice" || req.Email != "alice@example.org" || req.FirstName != "Alice" || req.LastName != "van Example" ||
		!req.Declared || req.LinkTTL != 24*time.Hour || req.PublicURL != "https://"+cfg.Apps["auth"].Hostname {
		t.Errorf("request %+v", req)
	}
	if req.EmailLink != cfg.CanMail("auth") || !req.EmailLink {
		t.Errorf("EmailLink %t, CanMail %t; the fixture can mail", req.EmailLink, cfg.CanMail("auth"))
	}
	cfg.SMTP = config.SMTP{}
	cfg.Apps["auth"] = withoutSMTP(cfg.Apps["auth"])
	if operatorRequest(cfg, "auth", "alice").EmailLink {
		t.Error("emails without SMTP")
	}
}

func withoutSMTP(a config.App) config.App { a.SMTP = nil; return a }

// An operator joins operators, members, and every app's admin and member
// groups, and is a Pocket ID administrator only under management: pocket-id.
func TestOperatorRequestFollowsManagement(t *testing.T) {
	cfg := fixture(t)
	for management, member := range map[string]bool{"paisans": true, "pocket-id": false} {
		app := cfg.Apps["auth"]
		settings := maps.Clone(app.Settings)
		settings["management"] = management
		app.Settings = settings
		cfg.Apps["auth"] = app
		req := operatorRequest(cfg, "auth", "alice")
		if req.Member != member {
			t.Errorf("%s: Member %t, want %t", management, req.Member, member)
		}
		groups := req.Groups()
		for _, want := range append(append([]string{"operators", "members"}, adminGroups(cfg)...), memberGroups(cfg)...) {
			if !slices.Contains(groups, want) {
				t.Errorf("%s: groups %v lack %s", management, groups, want)
			}
		}
	}
}

// A declared operator joins every app's member group as well as its admin group,
// since an app whose member gate does not admit its administrator refuses
// their first login.
func TestOperatorRequestJoinsTheMemberGroups(t *testing.T) {
	cfg := fixture(t)
	cfg.Operators["alice"] = config.Operator{Name: "Alice Example", Email: "alice@example.org"}
	req := operatorRequest(cfg, "auth", "alice")
	if want := memberGroups(cfg); len(want) == 0 || !slices.Equal(req.MemberGroups, want) {
		t.Errorf("member groups %v, want %v", req.MemberGroups, want)
	}
	if !slices.Contains(req.MemberGroups, "members") {
		t.Errorf("member groups %v lack members", req.MemberGroups)
	}
}

// A failed check's advice follows its ✗ line directly, at every verbosity,
// with no heading of its own.
func TestAFailedChecksAdviceFollowsItsLine(t *testing.T) {
	fakeConverge(t, nil)
	saved := convergeCheck
	convergeCheck = func(_ ui.Reporter, args []string) (string, []checkReport, error) {
		if strings.Join(args[:4], " ") == "host prepare --site home-a" {
			return "", nil, &ui.Problem{Hint: "mesh subnet 10.44.0.0/24 is in use", Explain: "Remove it if not in use or run paisans init to pick a free subnet."}
		}
		return "", nil, nil
	}
	t.Cleanup(func() { convergeCheck = saved })
	rec := withRecorder(t, false)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	fail := slices.IndexFunc(rec.Events, func(e ui.Event) bool { return e.Kind == "fail" && e.Text == "home-a" })
	if fail < 0 || fail+1 >= len(rec.Events) || rec.Events[fail+1] != (ui.Event{Kind: "explain", Text: "Remove it if not in use or run paisans init to pick a free subnet."}) {
		t.Errorf("after the ✗ line:\n%s", rec.Lines())
	}
	if rec.Has("note", "can run") {
		t.Errorf("a heading before the advice:\n%s", rec.Lines())
	}
}

// With links on more than one app, each name says which, and the links
// start in one column however long the names are.
func TestPrintLoginLinksAlignsEachLink(t *testing.T) {
	links := recordLinks(t)
	printLoginLinks([]declaredOperator{
		{App: "auth", ID: "alice", Link: "https://id.example.org/lc/tok-a", ExpiresIn: operatorLinkTTL},
		{App: "auth-staff", ID: "bob", Link: "https://staff.example.org/lc/tok-b", ExpiresIn: operatorLinkTTL},
	})
	want := "\nLogin links, each valid for 24 hours and for one sign in. Treat each as a password:\n" +
		"  alice on auth      https://id.example.org/lc/tok-a\n" +
		"  bob on auth-staff  https://staff.example.org/lc/tok-b\n\n"
	if links.String() != want {
		t.Errorf("links %q\nwant %q", links.String(), want)
	}
}

// meshFile is site's WireGuard file as the step that applies it renders it,
// from the run's classification.
func meshFile(t *testing.T, sites *topology.Classification, site string) string {
	t.Helper()
	declared := fixture(t)
	secrets, err := config.LoadDeploymentSecrets(fixtureSecretsPath(), declared.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(sites.Effective(declared), secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if strings.HasPrefix(f.Path, site+"/") && strings.Contains(f.Path, "/psns-") && strings.HasSuffix(f.Path, ".conf") {
			return f.Content
		}
	}
	t.Fatalf("no WireGuard file for %s", site)
	return ""
}

// stepSites keeps the classification each step of the run sees.
func stepSites(t *testing.T) *[]*topology.Classification {
	t.Helper()
	var seen []*topology.Classification
	saved := convergeRun
	convergeRun = func(args []string) error {
		seen = append(seen, runSites)
		return saved(args)
	}
	t.Cleanup(func() { convergeRun = saved })
	return &seen
}

// With one data site pending, the run plans no step for it, renders every
// other site without it, hands each apply its pending list, and ends
// degraded, exit 0; with --require-all, exit 1. Garage has one of its two
// nodes, so storage is held back and no storage step runs.
func TestConvergeRunsOnTheSitesItHas(t *testing.T) {
	ran := fakeConverge(t, nil)
	seen := stepSites(t)
	classifyWith(t, classifyHosts([]string{"home-b"}))
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("%v: %s", err, problemText(err))
	}
	sameLines(t, *ran, []string{
		"host prepare --site home-a", "host prepare --site vm", "host prepare --site watch",
		"dns init",
		"apply --site vm", "apply --site home-a", "apply --site watch",
		"apply --site home-a", "apply --site vm", "apply --site watch",
	})
	for _, s := range *seen {
		if s == nil || !slices.Equal(s.Pending(), []string{"home-b"}) {
			t.Fatalf("a step did not see the run's classification: %+v", s)
		}
	}
	if conf := meshFile(t, (*seen)[0], "home-a"); strings.Contains(conf, "[Peer]\n# home-b\n") {
		t.Errorf("home-a peers with pending home-b:\n%s", conf)
	}
	if !rec.Has("section", "pending") || !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b).") {
		t.Errorf("no pending section or degraded end:\n%s", rec.Lines())
	}
	if runSites != nil {
		t.Error("the run left its classification behind")
	}

	*ran = nil
	err := converge(t, "--execute", "--require-all")
	var w *ui.Warning
	if err == nil || errors.As(err, &w) || !strings.Contains(problemText(err), "--require-all") {
		t.Errorf("--require-all: %v", err)
	}
}

// A down site keeps its place in the rendered mesh but none of its own
// steps run, and storage, one of whose nodes it is, waits.
func TestConvergeSkipsADownSitesSteps(t *testing.T) {
	ran := fakeConverge(t, nil)
	seen := stepSites(t)
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		if m == "home-b" {
			t.Errorf("down home-b's etcd record was read")
		}
		return initial("home-a", "home-b", "vm"), true, nil
	}
	classifyWith(t, classifyHosts([]string{"home-b"}, "home-a", "home-b", "vm", "watch"))
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("%v: %s", err, problemText(err))
	}
	sameLines(t, *ran, []string{
		"host prepare --site home-a", "host prepare --site vm", "host prepare --site watch",
		"dns init",
		"apply --site watch",
		"apply --site home-a", "apply --site vm", "apply --site watch",
	})
	if s := (*seen)[0]; s == nil || !slices.Equal(s.Down(), []string{"home-b"}) {
		t.Fatalf("classification %+v", s)
	}
	if conf := meshFile(t, (*seen)[0], "home-a"); !strings.Contains(conf, "[Peer]\n# home-b\n") {
		t.Errorf("home-a lost its peer, down home-b:\n%s", conf)
	}
	if !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b).") {
		t.Errorf("no degraded end:\n%s", rec.Lines())
	}
}

// A data site ready to join waits while a joined site is down: the join
// changes every joined site's mesh and HAProxy files, which the down one
// cannot take. Its site add is dropped and the site is left out of the run
// as a pending one is, so no other site is handed files naming a member
// the cluster has not admitted; the plan says why, and the run goes on and
// ends degraded.
func TestConvergeJoinWaitsWhileASiteIsDown(t *testing.T) {
	ran := fakeConverge(t, nil)
	seen := stepSites(t)
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) {
		if m == "home-b" {
			return render.EtcdInitial{}, false, nil
		}
		return initial("home-a", "vm"), true, nil
	}
	classifyWith(t, classifyHosts([]string{"watch"}, "home-a", "vm", "watch"))
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatalf("%v: %s\n%s", err, problemText(err), rec.Lines())
	}
	for _, line := range *ran {
		if strings.Contains(line, "home-b") || strings.HasPrefix(line, "site add") {
			t.Errorf("ran %s while watch is down", line)
		}
	}
	if !slices.Contains(*ran, "apply --site home-a") {
		t.Errorf("the run stopped short: %v", *ran)
	}
	if s := (*seen)[0]; s == nil || !slices.Equal(s.Pending(), []string{"home-b"}) || !slices.Equal(s.Down(), []string{"watch"}) {
		t.Fatalf("classification %+v", s)
	}
	if conf := meshFile(t, (*seen)[0], "home-a"); strings.Contains(conf, "[Peer]\n# home-b\n") {
		t.Errorf("home-a peers with home-b, whose join waits:\n%s", conf)
	}
	if !rec.Has("end", "site add home-b") && !strings.Contains(rec.Lines(), "site add home-b") {
		t.Errorf("no line for the join that waits:\n%s", rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "every joined site answers") {
		t.Errorf("the plan does not say why the join waits:\n%s", rec.Lines())
	}
	if !rec.Has("result", "Applied, degraded: 2 sites pending or down (home-b, watch).") {
		t.Errorf("no degraded end:\n%s", rec.Lines())
	}
}

// everySite is the fixture's sites, as a deployment record lists them once
// every one has joined.
var everySite = []string{"home-a", "home-b", "vm", "watch"}

// A Pocket ID on a down site is not asked to make its operators: the run
// warns that they wait, and ends degraded, exit 0.
func TestConvergeLeavesOperatorsOfADownPocketID(t *testing.T) {
	fakeConverge(t, nil)
	path := editedFixture(t, "hostname: id.example.org\n    placement: cluster", "hostname: id.example.org\n    placement: { pinned: home-b }")
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) {
		return initial("home-a", "home-b", "vm"), true, nil
	}
	asked := fakeDeclaredOperators(t, func(app, id string, execute bool) (declaredOperator, error) {
		if app == "auth" {
			return declaredOperator{}, errors.New("home-b: the host refused the login")
		}
		return declaredOperator{}, nil
	})
	classifyWith(t, classifyHosts([]string{"home-b"}, everySite...))
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatalf("%v: %s", err, problemText(err))
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "auth/") {
			t.Errorf("asked %s of down home-b", a)
		}
	}
	if !rec.Has("warn", "auth") || !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b).") {
		t.Errorf("no warning or degraded end:\n%s", rec.Lines())
	}
}

// The web server step does not reach a down monitor: its site files are
// neither written nor checked, a note says so, and the run ends degraded.
func TestConvergeLeavesTheWebServerOfADownSite(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeWebServerCheck(t, nil)
	reached := false
	convergeWebServerFiles = func(_ ui.Reporter, _ *config.Config, _ string, scope webServerScope) webServerFiles {
		if scope.covers("watch") {
			reached = true
			return webServerFiles{failed: []string{"site files on watch"}}
		}
		return webServerFiles{}
	}
	classifyWith(t, classifyHosts([]string{"watch"}, everySite...))
	rec := recordConverge(t)
	if err := convergeAt(t, externalFixture(t), "--execute"); err != nil {
		t.Fatalf("%v: %s", err, problemText(err))
	}
	if reached || slices.Contains(*checked, "health") {
		t.Errorf("the web server step reached down watch: files %v, checked %v", reached, *checked)
	}
	if !rec.Has("note", "watch") || !rec.Has("result", "Applied, degraded: 1 site pending or down (watch).") {
		t.Errorf("no note or degraded end:\n%s", rec.Lines())
	}
}

// A degraded run that ends on the operator's web server still says it is
// degraded, and --require-all still fails it; so does one that ends on an
// operator's error.
func TestEveryDegradedEndHonoursRequireAll(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, map[string][]webserver.Result{"health": {{Name: webserver.CheckDNS, Detail: "health.example.org does not resolve"}}})
	classifyWith(t, classifyHosts([]string{"home-b"}))
	rec := recordConverge(t)
	path := externalFixture(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if err != nil && !errors.As(err, &w) {
		t.Fatalf("not a warning: %v", err)
	}
	if !rec.Has("result", "Applied, degraded: 1 site pending or down (home-b).") {
		t.Errorf("no degraded end beside the web server's warning:\n%s", rec.Lines())
	}
	err = convergeAt(t, path, "--execute", "--require-all")
	if err == nil || errors.As(err, &w) || !strings.Contains(problemText(err), "--require-all") {
		t.Errorf("--require-all with the web server's warning: %v", err)
	}

	fakeWebServerCheck(t, nil)
	fakeDeclaredOperators(t, func(string, string, bool) (declaredOperator, error) {
		return declaredOperator{}, errors.New("pocket id did not answer")
	})
	err = convergeAt(t, path, "--execute", "--require-all")
	if err == nil || !strings.Contains(problemText(err), "--require-all") {
		t.Errorf("--require-all with an operator's error: %v: %s", err, problemText(err))
	}
}
