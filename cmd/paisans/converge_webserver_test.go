package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// fakeWebServerCheck replaces checking an app's web server from this machine:
// an app in failing fails with those results, and every other passes every
// check. It returns the apps checked, one entry per check.
func fakeWebServerCheck(t *testing.T, failing map[string][]webserver.Result) *[]string {
	t.Helper()
	var checked []string
	saved := convergeWebServerCheck
	convergeWebServerCheck = func(target webserver.Target) []webserver.Result {
		checked = append(checked, target.App)
		if results, ok := failing[target.App]; ok {
			return results
		}
		return []webserver.Result{
			{Name: webserver.CheckDNS, OK: true, Detail: target.Hostname + " resolves to 203.0.113.20"},
			{Name: webserver.CheckCertificate, OK: true, Detail: "https://" + target.Hostname + "/ answered 200"},
		}
	}
	// Writing the site files reaches the monitor's host; here there is
	// nothing to write. fakeWebServerFiles says otherwise.
	savedFiles := convergeWebServerFiles
	convergeWebServerFiles = func(ui.Reporter, *config.Config, string, bool) webServerFiles { return webServerFiles{} }
	t.Cleanup(func() { convergeWebServerCheck, convergeWebServerFiles = saved, savedFiles })
	fakeWebServer(t, caddyAutomatic)
	return &checked
}

// caddyAutomatic is a Caddy in a container that obtains its own
// certificates, as Detect finds it.
var caddyAutomatic = webserver.WebServer{Server: webserver.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Certs: webserver.CertsAutomatic}

// webServerAsks is each time fakeWebServer was asked: the app, and whether
// through sudo.
type webServerAsks []string

// fakeWebServer replaces reading the web server in front of an app's host:
// it finds ws.
func fakeWebServer(t *testing.T, ws webserver.WebServer) *webServerAsks {
	t.Helper()
	var asked webServerAsks
	saved := findWebServer
	findWebServer = func(_ *config.Config, target webserver.Target, sudo bool) webserver.WebServer {
		asked = append(asked, fmt.Sprintf("%s sudo=%v", target.App, sudo))
		return ws
	}
	t.Cleanup(func() { findWebServer = saved })
	return &asked
}

// externalWatch makes the fixture's monitor, watch, a site with managed_by:
// external, which serves status through the operator's own web server.
func externalWatch(t *testing.T) {
	t.Helper()
	saved := convergeRead
	convergeRead = func(c, s string) (*config.Config, convergeState, error) {
		cfg, st, err := readConvergeState(c, s)
		if cfg != nil {
			watch := cfg.Sites["watch"]
			watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
			cfg.Sites["watch"] = watch
		}
		return cfg, st, err
	}
	t.Cleanup(func() { convergeRead = saved })
}

// certificateFails is the status app's web server with no route for its
// hostname at the operator's web server.
var certificateFails = map[string][]webserver.Result{"status": {
	{Name: webserver.CheckDNS, OK: true, Detail: "status.example.org resolves to 203.0.113.20"},
	{Name: webserver.CheckCertificate, Detail: "the certificate is not valid for status.example.org: it names watch.example.org"},
	{Name: webserver.CheckSignIn, OK: true, Detail: "https://status.example.org/login/oidc redirects"},
	{Name: webserver.CheckUpstream, OK: true, Detail: "203.0.113.20:8480 refused"},
}}

// With no app behind its own web server there is nothing to check: no
// section, and the run is complete.
func TestConvergeChecksNoWebServerWithoutAnExternalApp(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeWebServerCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(*checked) != 0 || slices.Contains(sections(rec), "web server") {
		t.Errorf("checked %v:\n%s", *checked, rec.Lines())
	}
	if !rec.Has("result", "Deployment complete.") {
		t.Errorf("no Deployment complete:\n%s", rec.Lines())
	}
}

// An app behind its own web server is checked once every step has run, in
// a section of its own after the pocket id one, a line per app naming its
// hostname.
func TestConvergeChecksEachExternalWebServer(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	checked := fakeWebServerCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*checked, []string{"status"}) {
		t.Errorf("checked %v", *checked)
	}
	got := sections(rec)
	if i, j := slices.Index(got, "install"), slices.Index(got, "web server"); i < 0 || j < i {
		t.Errorf("sections %v", got)
	}
	if i, j := slices.Index(got, "pocket id"), slices.Index(got, "web server"); i >= 0 && j < i {
		t.Errorf("sections %v", got)
	}
	var shown []string
	for _, l := range lines(inSection(rec, "web server")) {
		if !strings.HasPrefix(l, "progress ") {
			shown = append(shown, l)
		}
	}
	sameLines(t, shown, []string{
		"step status",
		"done status: status.example.org",
		"result Deployment complete.",
	})
}

// A failed check is a warning naming the app, what failed and how to fix
// it, and the run does not end as complete: it ends with a warning naming
// the app, and exits 0, since every step ran and the web server is the
// operator's.
func TestConvergeFailedWebServerIsNotComplete(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	fakeWebServerCheck(t, certificateFails)
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if rec.Has("result", "Deployment complete.") {
		t.Errorf("said complete:\n%s", rec.Lines())
	}
	var w *ui.Warning
	if !errors.As(err, &w) || !strings.Contains(w.Hint, "status") || !strings.Contains(w.Hint, "web server check") {
		t.Fatalf("err = %#v", err)
	}
	var b strings.Builder
	if code := reportError(&b, err, false); code != 0 || !strings.HasPrefix(b.String(), "WARN Deployment complete, but 1 app failed its web server check: status") {
		t.Errorf("exit %d:\n%s", code, b.String())
	}
	i := rec.Index("warned", "status")
	if i < 0 || rec.Events[i+1].Kind != "explain" || rec.Has("fail", "status") || rec.Has("note", "status") {
		t.Fatalf("not one warning line:\n%s", rec.Lines())
	}
	explain := rec.Events[i+1].Text
	for _, want := range []string{
		"certificate: the certificate is not valid for status.example.org: it names watch.example.org",
		"paisans web-server show --app status",
		"paisans web-server check --app status",
	} {
		if !strings.Contains(explain, want) {
			t.Errorf("detail %q lacks %q", explain, want)
		}
	}
	for _, passed := range []string{"resolves to", "redirects", "refused"} {
		if strings.Contains(explain, passed) {
			t.Errorf("detail %q names a check that passed", explain)
		}
	}
}

// A failed web server check alongside a failure of apply's own ends as
// that failure: the failure's mark and exit 1.
func TestConvergeFailedWebServerAndAdminFails(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	fakeWebServerCheck(t, certificateFails)
	fakeDeclaredAdmins(t, func(string, string, bool) (declaredAdmin, error) {
		return declaredAdmin{}, errors.New("POST /api/users: 400 email already in use")
	})
	recordLinks(t)
	recordConverge(t)
	err := converge(t, "--execute")
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Fatalf("err = %#v", err)
	}
	var b strings.Builder
	if code := reportError(&b, err, false); code != 1 || !strings.HasPrefix(b.String(), "FAIL ") {
		t.Errorf("exit %d:\n%s", code, b.String())
	}
}

// The web server in front of an app is the operator's, so its check runs
// once, whatever fails: waiting on it is not apply's to do.
func TestConvergeChecksAWebServerOnce(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	checked := fakeWebServerCheck(t, certificateFails)
	recordConverge(t)
	converge(t, "--execute")
	if !slices.Equal(*checked, []string{"status"}) {
		t.Errorf("checked %v, want once", *checked)
	}
}

// A dry run checks nothing: each app behind its own web server is a line
// waiting on --execute.
func TestConvergeDryRunListsTheWebServerCheck(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	externalWatch(t)
	checked := fakeWebServerCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*checked) != 0 {
		t.Errorf("a dry run checked %v", *checked)
	}
	got := lines(inSection(rec, "web server"))
	if len(got) < 2 || got[0] != "step status" || !strings.HasPrefix(got[1], "waiting status: ") {
		t.Errorf("web server section %q:\n%s", got, rec.Lines())
	}
}

// A failed check reads the web server in front of the app, with the run's
// sudo, and names it in the warning, so the
// advice says what to add the configuration to. A passing one reads nothing.
func TestConvergeFailedWebServerNamesTheWebServer(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	fakeWebServerCheck(t, nil)
	asked := fakeWebServer(t, caddyAutomatic)
	recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 {
		t.Errorf("a passing check read the web server: %v", *asked)
	}

	fakeWebServerCheck(t, certificateFails)
	asked = fakeWebServer(t, caddyAutomatic)
	rec := recordConverge(t)
	// The run's --sudo reaches the read: here --sudo=false, as every
	// converge test runs, since a settled sudo would reach a real host.
	// TestWebServerHostSharesTheRunsSudo shows a read with sudo shares the
	// run's answer.
	converge(t, "--execute")
	if !slices.Equal(*asked, webServerAsks{"status sudo=false"}) {
		t.Errorf("asked %v", *asked)
	}
	i := rec.Index("warned", "status")
	if i < 0 || !strings.Contains(rec.Events[i+1].Text, caddyAutomatic.Summary()) {
		t.Errorf("the warning does not name the web server:\n%s", rec.Lines())
	}
}
