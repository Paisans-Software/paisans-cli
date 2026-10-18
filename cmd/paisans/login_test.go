package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// twoDataSitesWithoutUsers is twoDataSites with both sites leaving ssh.user
// out, so each is reached as paisans-f2a9.
var twoDataSitesWithoutUsers = strings.ReplaceAll(twoDataSites, "user: ubuntu, ", "")

// loginNotes routes the login notes to a recorder for the test.
func loginNotes(t *testing.T) *ui.Recorder {
	t.Helper()
	rec := &ui.Recorder{}
	saved := loginReporter
	routeLoginNotes(rec)
	t.Cleanup(func() { loginReporter = saved })
	return rec
}

// executing makes the test's logins those of a command run with --execute,
// which write the user they record.
func executing(t *testing.T) {
	t.Helper()
	resetLogins(t)
	recordLogins = true
}

// The first login as the deployment's default user, in a run with
// --execute, writes the name into the site's ssh.user, and says so, so the
// file names who logs in. Only that site is written.
func TestTheFirstLoginRecordsTheDefaultUser(t *testing.T) {
	executing(t)
	rec := loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	tr := siteTransport("home-a", cfg.Sites["home-a"], "")
	if tr.Login == nil || tr.Login.Connected == nil {
		t.Fatal("the default user's login records nothing")
	}
	tr.Login.Connected()
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reread.Sites["home-a"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("home-a's ssh.user is %q, want paisans-f2a9", got)
	}
	if got := reread.Sites["home-b"].SSH.User; got != "" {
		t.Errorf("home-b's ssh.user is %q, want it left out", got)
	}
	if !rec.Has("note", "home-a: ssh.user paisans-f2a9 recorded in paisans-stack.yaml") {
		t.Errorf("no note: %+v", rec.Events)
	}
}

// A declared user is recorded already, and a destination typed with --ssh
// is not the site's section, so neither writes anything.
func TestOnlyTheDefaultUsersLoginIsRecorded(t *testing.T) {
	executing(t)
	rec := loginNotes(t)
	path, cfg := archFixture(t, twoDataSites)
	before, _ := os.ReadFile(path)
	siteTransport("home-a", cfg.Sites["home-a"], "").Login.Connected()
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a declared user's login changed the file:\n%s", after)
	}
	path, cfg = archFixture(t, twoDataSitesWithoutUsers)
	before, _ = os.ReadFile(path)
	siteTransport("home-b", cfg.Sites["home-b"], "paisans-f2a9@203.0.113.7").Login.Connected()
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("an --ssh login changed the file:\n%s", after)
	}
	if len(rec.Events) != 0 {
		t.Errorf("a login that records nothing made a note: %+v", rec.Events)
	}
}

// Sites logged in to at once in a run with --execute each record their
// user: the writes take turns, and the file loads with both.
func TestLoginsAtOnceEachRecordTheirUser(t *testing.T) {
	executing(t)
	loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	var wg sync.WaitGroup
	for _, name := range []string{"home-a", "home-b"} {
		connected := siteTransport(name, cfg.Sites[name], "").Login.Connected
		wg.Add(1)
		go func() {
			defer wg.Done()
			connected()
		}()
	}
	wg.Wait()
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home-a", "home-b"} {
		if got := reread.Sites[name].SSH.User; got != "paisans-f2a9" {
			t.Errorf("%s's ssh.user is %q", name, got)
		}
	}
}

// sharedDestination is twoDataSitesWithoutUsers with both sites at one
// host, so both are reached as paisans-f2a9@home-a.local and share a Login.
var sharedDestination = strings.Replace(twoDataSitesWithoutUsers, "host: home-b.local", "host: home-a.local", 1)

// Two sites at one destination share its Login, and its first login records
// the user of each, whether the second site's transport was built before
// that login or after it.
func TestSitesSharingADestinationEachRecordTheirUser(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			executing(t)
			loginNotes(t)
			path, cfg := archFixture(t, sharedDestination)
			a := siteTransport("home-a", cfg.Sites["home-a"], "")
			if when == "before" {
				siteTransport("home-b", cfg.Sites["home-b"], "")
			}
			a.Login.Connected()
			if when == "after" {
				if b := siteTransport("home-b", cfg.Sites["home-b"], ""); b.Login != a.Login {
					t.Fatal("the two sites do not share a Login")
				}
			}
			reread, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"home-a", "home-b"} {
				if got := reread.Sites[name].SSH.User; got != "paisans-f2a9" {
					t.Errorf("%s's ssh.user is %q, want paisans-f2a9", name, got)
				}
			}
		})
	}
}

// A site from a configuration loaded without a file has nowhere to record
// its user: its login records nothing and is no error, and it does not stop
// a file-loaded transport to the same destination from recording.
func TestASiteWithoutAFileRecordsNothing(t *testing.T) {
	executing(t)
	rec := loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	unfiled, err := config.LoadBytes([]byte(twoDataSitesWithoutUsers))
	if err != nil {
		t.Fatal(err)
	}
	first := siteTransport("home-a", unfiled.Sites["home-a"], "")
	filed := siteTransport("home-a", cfg.Sites["home-a"], "")
	if first.Login != filed.Login {
		t.Fatal("the two transports do not share a Login")
	}
	first.Login.Connected()
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reread.Sites["home-a"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("home-a's ssh.user is %q, want paisans-f2a9", got)
	}
	if n := len(rec.Events); n != 1 || rec.Index("note", "home-a: ssh.user paisans-f2a9 recorded in paisans-stack.yaml") != 0 {
		t.Errorf("want one note, the file-loaded site's: %+v", rec.Events)
	}
}

// A user written with no value cannot be filled in place. The login it
// follows has worked, so the command is told and goes on, and the file is
// left as it was.
func TestAUserThatCannotBeRecordedIsAWarning(t *testing.T) {
	executing(t)
	rec := loginNotes(t)
	body := strings.Replace(twoDataSitesWithoutUsers, "ssh: {host: home-a.local, keys: [alice]}", "ssh: {host: home-a.local, user: , keys: [alice]}", 1)
	path, cfg := archFixture(t, body)
	before, _ := os.ReadFile(path)
	siteTransport("home-a", cfg.Sites["home-a"], "").Login.Connected()
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("the file changed:\n%s", after)
	}
	i := rec.Index("note", "home-a: ssh.user paisans-f2a9 could not be recorded in paisans-stack.yaml")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "written with no value") {
		t.Errorf("no warning: %+v", rec.Events)
	}
}

// A run without --execute writes nothing: it says what --execute records,
// as an architecture read does, and goes on as the user the file already
// means.
func TestALoginWithoutExecuteRecordsNothing(t *testing.T) {
	resetLogins(t)
	rec := loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	before, _ := os.ReadFile(path)
	siteTransport("home-a", cfg.Sites["home-a"], "").Login.Connected()
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("the file changed:\n%s", after)
	}
	if !rec.Has("note", "home-a: ssh.user paisans-f2a9, --execute will record it in paisans-stack.yaml") {
		t.Errorf("no note of what --execute records: %+v", rec.Events)
	}
}

// Any command run with --execute records, whatever it changes, and a run
// without it does not.
func TestOnlyARunWithExecuteRecords(t *testing.T) {
	for _, c := range []struct {
		name    string
		execute bool
		want    bool
	}{
		{"apply", true, true}, {"apply", false, false}, {"preflight", false, false},
		{"doctor", true, true}, {"doctor", false, false}, {"deployment pull", true, true},
		{"deployment recover", true, true}, {"secrets prune", true, true}, {"secrets prune", false, false},
	} {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		if c.name != "preflight" {
			fs.Bool("execute", false, "")
		}
		var args []string
		if c.execute {
			args = []string{"--execute"}
		}
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if got := executes(fs); got != c.want {
			t.Errorf("%s execute=%v records %v, want %v", c.name, c.execute, got, c.want)
		}
	}
}

// fixtureWithoutUsers is a copy of the fixture configuration with no site
// naming its ssh user, so each is reached as the deployment's default.
func fixtureWithoutUsers(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), "      user: ubuntu\n", ""))
	if strings.Contains(string(body), "user:") {
		t.Fatal("the fixture's user lines changed shape; the copy still names one")
	}
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A step that has finished leaves its reporter behind: a login first made
// after it says its note on the run's reporter, not the finished step's.
func TestALoginAfterAStepIsToldOnTheRunsReporter(t *testing.T) {
	for _, how := range []string{"quiet", "check"} {
		t.Run(how, func(t *testing.T) {
			executing(t)
			saved := loginReporter
			t.Cleanup(func() { loginReporter = saved })
			_, cfg := archFixture(t, twoDataSitesWithoutUsers)
			// The step's command points the notes at its own reporter,
			// as commonFlags does.
			step := func([]string) error { routeLoginNotes(reporterOverride); return nil }
			run := &ui.Recorder{}
			if how == "quiet" {
				savedRun := convergeRun
				convergeRun = step
				t.Cleanup(func() { convergeRun = savedRun })
				_, _, _ = convergeQuiet(run, run.Step("a step"), []string{"deployment", "apply"}, false)
			} else {
				_, _, _ = checkStep(run, []string{"deployment", "apply"}, step)
			}
			siteTransport("home-a", cfg.Sites["home-a"], "").Login.Connected()
			if !run.Has("note", "home-a: ssh.user paisans-f2a9 recorded in paisans-stack.yaml") {
				t.Errorf("the note is not on the run's reporter:\n%s", run.Lines())
			}
		})
	}
}

// archReadLogsIn makes a whole apply's architecture read reach each host as
// the run does, so it is the site's first login, as it is outside a test.
// fakeConverge answers it without logging in, and then the first login is
// an apply step's, after the publish.
func archReadLogsIn(t *testing.T) {
	t.Helper()
	saved := convergeHost
	convergeHost = func(name string, site config.Site) apply.Transport { return siteTransport(name, site, "") }
	t.Cleanup(func() { convergeHost = saved })
}

// An apply with --execute logs in to every site, and so records its user,
// before it gathers the version it publishes: the published bytes carry
// every user, they are the file as it ends, and no step finds it changed
// after the publish.
func TestAnApplyRecordsEveryUserBeforeItPublishes(t *testing.T) {
	path := fixtureWithoutUsers(t)
	fakeConverge(t, nil)
	archReadLogsIn(t)
	rec := recordConverge(t)
	gathered := gatherFiles(t)
	failingSSH(t)
	loginNotes(t)
	fake := convergeRun
	var stepErrs []error
	convergeRun = func(args []string) error {
		if isApply(args) {
			if err := runStep(args); err != nil {
				stepErrs = append(stepErrs, err)
			}
			return nil
		}
		return fake(args)
	}
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--execute"}, noTerminalStdin(), io.Discard)
	})
	if err != nil {
		t.Fatalf("%v %s\n%s", err, problemText(err), rec.Lines())
	}
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "user: paisans-"); n != 4 {
		t.Fatalf("%d sites record their user:\n%s\n%s", n, body, rec.Lines())
	}
	if *gathered == nil || string((*gathered).paisans) != string(body) {
		t.Fatalf("the version published is not the file as it ends:\n%s", rec.Lines())
	}
	if len(stepErrs) == 0 {
		t.Fatal("no apply step ran")
	}
	for _, e := range stepErrs {
		if strings.Contains(e.Error()+problemText(e), "changed after the run published it") {
			t.Errorf("a step found the file changed after the publish: %v", problemText(e))
		}
	}
}

// deployment apply --site without --execute leaves the file as it was, and previews
// the version from the file on disk.
func TestADryRunApplySiteLeavesTheFile(t *testing.T) {
	path := fixtureWithoutUsers(t)
	before, _ := os.ReadFile(path)
	gathered := gatherFiles(t)
	failingSSH(t)
	rec := withRecorder(t, false)
	captureStdout(t, func() {
		_ = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--site", "vm"}, noTerminalStdin(), io.Discard)
	})
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("a dry run changed the file:\n%s", after)
	}
	if *gathered == nil || string((*gathered).paisans) != string(after) {
		t.Fatal("the version previewed is not the file on disk")
	}
	if !rec.Has("note", ": ssh.user paisans-") {
		t.Errorf("no note of what --execute records:\n%s", rec.Lines())
	}
}

// A dry run of the whole apply leaves the file as it was and ends saying
// nothing changed, which is true.
func TestADryRunApplyLeavesTheFile(t *testing.T) {
	path := fixtureWithoutUsers(t)
	before, _ := os.ReadFile(path)
	fakeConverge(t, nil)
	archReadLogsIn(t)
	rec := recordConverge(t)
	failingSSH(t)
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath()}, noTerminalStdin(), io.Discard)
	})
	if err != nil {
		t.Fatalf("%v %s\n%s", err, problemText(err), rec.Lines())
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("a dry run changed the file:\n%s", after)
	}
	if i := rec.Index("result", ""); i < 0 || !strings.HasPrefix(rec.Events[i].Text, "Nothing changed. ") {
		t.Errorf("the ending is not Nothing changed:\n%s", rec.Lines())
	}
}

// doctor without --execute records no user, and with it records the user
// of the site it logged in to, as any run with --execute does.
func TestDoctorRecordsOnlyWithExecute(t *testing.T) {
	for _, execute := range []bool{false, true} {
		path := fixtureWithoutUsers(t)
		before, _ := os.ReadFile(path)
		failingSSH(t)
		withRecorder(t, false)
		args := []string{"--config", path, "--site", "vm"}
		if execute {
			args = append(args, "--execute")
		}
		_ = runDoctor(args)
		after, _ := os.ReadFile(path)
		if changed := string(after) != string(before); changed != execute {
			t.Errorf("doctor execute=%v changed the file %v:\n%s", execute, changed, after)
		}
	}
}

// A command that takes its name after the flags reads the flags after it
// too: --execute there still makes it a run that records the user. Each
// run here is refused before it reaches a host, after its flags are read.
func TestExecuteAfterTheNameRecords(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(path string) error
	}{
		{"site remove", func(path string) error {
			return runSiteRemove([]string{"--config", path, "--secrets", fixtureSecretsPath(), "home-b", "--execute"}, strings.NewReader(""), io.Discard)
		}},
		{"app remove", func(path string) error {
			return runAppRemove([]string{"--config", path, "--secrets", fixtureSecretsPath(), "talk", "--execute"}, strings.NewReader(""), io.Discard)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := fixtureWithoutUsers(t)
			resetLogins(t)
			withRecorder(t, false)
			_ = c.run(path)
			if !recordLogins {
				t.Error("--execute after the name is not a run that records the user")
			}
		})
	}
}
