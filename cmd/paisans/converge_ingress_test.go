package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ingress"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// fakeIngressCheck replaces checking an app's ingress from this machine:
// an app in failing fails with those results on every attempt, and every
// other passes every check. It returns the apps checked, one entry per
// attempt.
func fakeIngressCheck(t *testing.T, failing map[string][]ingress.Result) *[]string {
	t.Helper()
	var checked []string
	saved, savedSleep := convergeIngressCheck, convergeSleep
	convergeIngressCheck = func(target ingress.Target) []ingress.Result {
		checked = append(checked, target.App)
		if results, ok := failing[target.App]; ok {
			return results
		}
		return []ingress.Result{
			{Name: ingress.CheckDNS, OK: true, Detail: target.Hostname + " resolves to 203.0.113.20"},
			{Name: ingress.CheckCertificate, OK: true, Detail: "https://" + target.Hostname + "/ answered 200"},
		}
	}
	convergeSleep = func(time.Duration) {}
	t.Cleanup(func() { convergeIngressCheck, convergeSleep = saved, savedSleep })
	fakeWebServer(t, caddyAutomatic)
	return &checked
}

// caddyAutomatic is a Caddy in a container that obtains its own
// certificates, as Detect finds it.
var caddyAutomatic = ingress.WebServer{Server: ingress.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Certs: ingress.CertsAutomatic}

// webServerAsks is each time fakeWebServer was asked: the app, and whether
// through sudo.
type webServerAsks []string

// fakeWebServer replaces reading the web server in front of an app's host:
// it finds ws.
func fakeWebServer(t *testing.T, ws ingress.WebServer) *webServerAsks {
	t.Helper()
	var asked webServerAsks
	saved := ingressWebServer
	ingressWebServer = func(_ *config.Config, target ingress.Target, sudo bool) ingress.WebServer {
		asked = append(asked, fmt.Sprintf("%s sudo=%v", target.App, sudo))
		return ws
	}
	t.Cleanup(func() { ingressWebServer = saved })
	return &asked
}

// externalWatch makes the fixture's monitor, watch, a site in ingress mode
// external, which serves status through the operator's own web server.
func externalWatch(t *testing.T) {
	t.Helper()
	saved := convergeRead
	convergeRead = func(c, s string) (*config.Config, convergeState, error) {
		cfg, st, err := readConvergeState(c, s)
		if cfg != nil {
			watch := cfg.Sites["watch"]
			watch.Ingress = &config.Ingress{Mode: config.IngressExternal, Listen: "127.0.0.1:8480"}
			cfg.Sites["watch"] = watch
		}
		return cfg, st, err
	}
	t.Cleanup(func() { convergeRead = saved })
}

// certificateFails is the status app's ingress with no route for its
// hostname at the operator's web server.
var certificateFails = map[string][]ingress.Result{"status": {
	{Name: ingress.CheckDNS, OK: true, Detail: "status.example.org resolves to 203.0.113.20"},
	{Name: ingress.CheckCertificate, Detail: "the certificate is not valid for status.example.org: it names watch.example.org"},
	{Name: ingress.CheckSignIn, OK: true, Detail: "https://status.example.org/login/oidc redirects"},
	{Name: ingress.CheckUpstream, OK: true, Detail: "203.0.113.20:8480 refused"},
}}

// With no app behind an external ingress there is nothing to check: no
// section, and the run is complete.
func TestConvergeChecksNoIngressWithoutAnExternalApp(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeIngressCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(*checked) != 0 || slices.Contains(sections(rec), "ingress") {
		t.Errorf("checked %v:\n%s", *checked, rec.Lines())
	}
	if !rec.Has("result", "Deployment complete.") {
		t.Errorf("no Deployment complete:\n%s", rec.Lines())
	}
}

// An app behind an external ingress is checked once every step has run, in
// a section of its own after the pocket id one, a line per app naming its
// hostname.
func TestConvergeChecksEachExternalIngress(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	checked := fakeIngressCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*checked, []string{"status"}) {
		t.Errorf("checked %v", *checked)
	}
	got := sections(rec)
	if i, j := slices.Index(got, "install"), slices.Index(got, "ingress"); i < 0 || j < i {
		t.Errorf("sections %v", got)
	}
	if i, j := slices.Index(got, "pocket id"), slices.Index(got, "ingress"); i >= 0 && j < i {
		t.Errorf("sections %v", got)
	}
	var shown []string
	for _, l := range lines(inSection(rec, "ingress")) {
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
// it, and the run does not end as complete: it ends with a problem naming
// the app.
func TestConvergeFailedIngressIsNotComplete(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	fakeIngressCheck(t, certificateFails)
	rec := recordConverge(t)
	err := converge(t, "--execute")
	if rec.Has("result", "Deployment complete.") {
		t.Errorf("said complete:\n%s", rec.Lines())
	}
	var p *ui.Problem
	if !errors.As(err, &p) || !strings.Contains(p.Hint, "status") || !strings.Contains(p.Hint, "ingress check") {
		t.Fatalf("err = %#v", err)
	}
	if !rec.Has("fail", "status") {
		t.Errorf("no failed line:\n%s", rec.Lines())
	}
	i := rec.Index("note", "status")
	if i < 0 {
		t.Fatalf("no warning:\n%s", rec.Lines())
	}
	note := rec.Events[i]
	for _, want := range []string{"certificate"} {
		if !strings.Contains(note.Text, want) {
			t.Errorf("hint %q lacks %q", note.Text, want)
		}
	}
	for _, want := range []string{
		"it names watch.example.org",
		"paisans ingress show --app status",
		"paisans ingress check --app status",
	} {
		if !strings.Contains(note.Extra, want) {
			t.Errorf("detail %q lacks %q", note.Extra, want)
		}
	}
	for _, passed := range []string{"resolves to", "redirects", "refused"} {
		if strings.Contains(note.Extra, passed) {
			t.Errorf("detail %q names a check that passed", note.Extra)
		}
	}
}

// A certificate or sign in failure can be an app or a certificate that has
// only just started, so the check is tried again a few times before it is
// failed. A DNS failure cannot, and is failed at once.
func TestConvergeTriesAnIngressAgainOnlyWhenItMayStillCome(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	checked := fakeIngressCheck(t, certificateFails)
	recordConverge(t)
	converge(t, "--execute")
	if len(*checked) != ingressAttempts {
		t.Errorf("certificate checked %d times, want %d", len(*checked), ingressAttempts)
	}

	dnsFails := map[string][]ingress.Result{"status": {
		{Name: ingress.CheckDNS, Detail: "status.example.org does not resolve"},
		{Name: ingress.CheckCertificate, Detail: "https://status.example.org/: no such host"},
	}}
	checked = fakeIngressCheck(t, dnsFails)
	converge(t, "--execute")
	if len(*checked) != 1 {
		t.Errorf("dns checked %d times, want 1", len(*checked))
	}
}

// A check that passes on a later attempt passes.
func TestConvergeIngressPassesOnALaterAttempt(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	attempts := 0
	saved, savedSleep := convergeIngressCheck, convergeSleep
	convergeIngressCheck = func(target ingress.Target) []ingress.Result {
		attempts++
		if attempts < 3 {
			return certificateFails["status"]
		}
		return []ingress.Result{{Name: ingress.CheckCertificate, OK: true, Detail: "answered 200"}}
	}
	convergeSleep = func(time.Duration) {}
	t.Cleanup(func() { convergeIngressCheck, convergeSleep = saved, savedSleep })
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || !rec.Has("done", "status") || !rec.Has("result", "Deployment complete.") {
		t.Errorf("attempts %d:\n%s", attempts, rec.Lines())
	}
}

// A dry run checks nothing: each app behind an external ingress is a line
// waiting on --execute.
func TestConvergeDryRunListsTheIngressCheck(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	externalWatch(t)
	checked := fakeIngressCheck(t, nil)
	rec := recordConverge(t)
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if len(*checked) != 0 {
		t.Errorf("a dry run checked %v", *checked)
	}
	got := lines(inSection(rec, "ingress"))
	if len(got) < 2 || got[0] != "step status" || !strings.HasPrefix(got[1], "waiting status: ") {
		t.Errorf("ingress section %q:\n%s", got, rec.Lines())
	}
}

// A failed check reads the web server in front of the app, with the run's
// sudo, and names it in the warning, so the
// advice says what to add the configuration to. A passing one reads nothing.
func TestConvergeFailedIngressNamesTheWebServer(t *testing.T) {
	fakeConverge(t, nil)
	externalWatch(t)
	fakeIngressCheck(t, nil)
	asked := fakeWebServer(t, caddyAutomatic)
	recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 {
		t.Errorf("a passing check read the web server: %v", *asked)
	}

	fakeIngressCheck(t, certificateFails)
	asked = fakeWebServer(t, caddyAutomatic)
	rec := recordConverge(t)
	// The run's --sudo reaches the read: here --sudo=false, as every
	// converge test runs, since a settled sudo would reach a real host.
	// TestIngressHostSharesTheRunsSudo shows a read with sudo shares the
	// run's answer.
	converge(t, "--execute")
	if !slices.Equal(*asked, webServerAsks{"status sudo=false"}) {
		t.Errorf("asked %v", *asked)
	}
	i := rec.Index("note", "status")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, caddyAutomatic.Summary()) {
		t.Errorf("the warning does not name the web server:\n%s", rec.Lines())
	}
}
