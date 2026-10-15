package main

import (
	"os"
	"strings"
	"sync"
	"testing"

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

// The first login as the deployment's default user writes the name into
// the site's ssh.user, and says so, so the file names who logs in. Only
// that site is written.
func TestTheFirstLoginRecordsTheDefaultUser(t *testing.T) {
	resetLogins(t)
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
	resetLogins(t)
	_, cfg := archFixture(t, twoDataSites)
	if l := siteTransport("home-a", cfg.Sites["home-a"], "").Login; l.Connected != nil {
		t.Error("a declared user's login records")
	}
	_, cfg = archFixture(t, twoDataSitesWithoutUsers)
	if l := siteTransport("home-b", cfg.Sites["home-b"], "paisans-f2a9@203.0.113.7").Login; l.Connected != nil {
		t.Error("an --ssh login records")
	}
}

// Sites logged in to at once, as doctor reaches them, each record their
// user: the writes take turns, and the file loads with both.
func TestLoginsAtOnceEachRecordTheirUser(t *testing.T) {
	resetLogins(t)
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

// A user written with no value cannot be filled in place. The login it
// follows has worked, so the command is told and goes on, and the file is
// left as it was.
func TestAUserThatCannotBeRecordedIsAWarning(t *testing.T) {
	resetLogins(t)
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
