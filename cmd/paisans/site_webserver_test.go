package main

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// siteApply is how apply --site --execute ends once site's stacks are up,
// on the configuration at path: through rec, or, with inside, as a step of
// apply without --site does.
func siteApply(t *testing.T, path, site string, inside bool) (*ui.Recorder, error) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	var r ui.Reporter = rec
	if inside {
		r = checkReporter{rec, rec, &[]checkReport{}}
	}
	return rec, siteApplied(r, cfg, path, site, "", false, 4, nil)
}

// apply --site on a monitor with managed_by: external writes its app's
// site file, reloads the server in its container, records include_dir in
// paisans-stack.yaml and checks the app once, then ends as applied.
func TestApplySiteWritesTheSiteFileAndReloads(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "{\n\temail ops@example.org\n}\nimport /etc/caddy/paisans.d/*\n")
	rec, err := siteApply(t, path, "watch", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.server.Files[healthFile], "reverse_proxy 127.0.0.1:8480") || !f.server.Ran("caddy validate") || !f.server.Ran("caddy reload") {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	if got := f.manifest.record(t); len(got) != 1 || got[0].Path != healthFile {
		t.Errorf("record %+v", got)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "include_dir: /opt/caddy/paisans.d\n") {
		t.Errorf("paisans-stack.yaml:\n%s", data)
	}
	if !slices.Equal(*checked, []string{"health"}) {
		t.Errorf("checked %v", *checked)
	}
	got := lines(inSection(rec, "web server"))
	want := []string{
		"step site file for health",
		"progress read the web server on watch: site file for health",
		"progress write paisans-f2a9-health.caddy: site file for health",
		"done site file for health: " + healthFile + " written, caddy reloaded",
		"note watch: include_dir set to /opt/caddy/paisans.d in paisans-stack.yaml: Apply derived it beside caddy's configuration, /opt/caddy/Caddyfile, which includes it. Change it there to use another directory, or set it to false to have apply write nothing for your web server.",
		"step health",
		"progress reach health.example.org: health",
		"done health: health.example.org",
		"result Applied 4 files to watch.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("web server section:\n%s", strings.Join(got, "\n"))
	}
}

// With no line including the directory, apply --site writes nothing, says
// the line to add, and ends on the full apply's warning: every step ran,
// and the exit status is 0. The check would only restate the site file's
// warning, so it is not run.
func TestApplySiteWarnsWhereTheServerDoesNotIncludeTheDirectory(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "blog.example.org {\n\treverse_proxy 127.0.0.1:2368\n}\n")
	rec, err := siteApply(t, path, "watch", false)
	var w *ui.Warning
	if !errors.As(err, &w) || w.Hint != "Deployment complete, but 1 app failed its web server check: health" {
		t.Fatalf("err %v", err)
	}
	if reportError(io.Discard, err, false) != 0 {
		t.Errorf("a warning exits non-zero")
	}
	if len(f.server.Files) != 0 || f.manifest.writes != 0 {
		t.Errorf("wrote %v", f.server.Files)
	}
	i := rec.Index("warned", "site file for health")
	if i < 0 || rec.Events[i].Extra != "caddy does not include /etc/caddy/paisans.d" || !strings.Contains(rec.Events[i+1].Text, "    import /etc/caddy/paisans.d/*\n") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if got := lines(inSection(rec, "web server")); slices.Contains(got, "step health") || len(*checked) != 0 {
		t.Errorf("checked %v:\n%s", *checked, rec.Lines())
	}
	if rec.Has("result", "Applied") {
		t.Errorf("ended as applied:\n%s", rec.Lines())
	}
}

// What apply needs on the monitor and cannot read there is a failure, not
// the operator's web server: ✗, and the exit status is 1.
func TestApplySiteFailsWhereTheMonitorCannotBeRead(t *testing.T) {
	fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	f.manifest.unreachable = true
	rec, err := siteApply(t, path, "watch", false)
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Fatalf("err %v", err)
	}
	if hint, _ := ui.Describe(err); hint != "Deployment not complete: 1 web server step failed: site files on watch" {
		t.Errorf("hint %q", hint)
	}
	if reportError(io.Discard, err, false) != 1 {
		t.Errorf("a failure exits 0")
	}
	if !rec.Has("fail", "site files on watch") || rec.Has("result", "Applied") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// apply --site on a site without managed_by: external reaches no web
// server, and checks nothing.
func TestApplySiteWithoutAnExternalWebServerRunsNoWebServerStep(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	rec, err := siteApply(t, path, "home-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.server.Asked) != 0 || len(f.sudo) != 0 || len(*checked) != 0 || rec.Has("section", "web server") {
		t.Errorf("asked %v, checked %v:\n%s", f.server.Asked, *checked, rec.Lines())
	}
	if !rec.Has("result", "Applied 4 files to home-a.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// As a step of apply without --site, apply --site leaves the web server
// to the run, which does it once at its end.
func TestApplySiteInsideTheFullApplyLeavesTheWebServerToIt(t *testing.T) {
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	rec, err := siteApply(t, path, "watch", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.server.Asked) != 0 || len(f.sudo) != 0 || len(*checked) != 0 || rec.Has("section", "web server") {
		t.Errorf("asked %v, checked %v:\n%s", f.server.Asked, *checked, rec.Lines())
	}
}

// A full apply writes the site file and checks the app once, at its end,
// although the apply --site it runs for the monitor ends as that command
// does.
func TestApplyRunsTheWebServerStepOnce(t *testing.T) {
	fakeConverge(t, nil)
	checked := fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	fake := convergeRun
	convergeRun = func(args []string) error {
		if args[0] == "apply" && slices.Contains(args, "watch") && slices.Contains(args, "--execute") {
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			return siteApplied(reporterOverride, cfg, path, "watch", "", false, 4, nil)
		}
		return fake(args)
	}
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	steps, reloads := 0, 0
	for _, e := range rec.Events {
		if e.Kind == "step" && e.Text == "site file for health" {
			steps++
		}
	}
	for _, c := range f.server.Asked {
		if strings.Contains(c, "caddy reload") {
			reloads++
		}
	}
	if steps != 1 || reloads != 1 || !slices.Equal(*checked, []string{"health"}) {
		t.Errorf("site file steps %d, asked %v, checked %v:\n%s", steps, f.server.Asked, *checked, rec.Lines())
	}
}

// apply --site's dry run lists the apps its web server step checks with
// --execute, as the full apply's does; a site without managed_by:
// external lists none, and nor does the dry run of a step of the full
// apply.
func TestApplySiteDryRunListsTheWebServerStep(t *testing.T) {
	quietSSH(t)
	path := externalFixture(t)
	for _, tc := range []struct {
		site   string
		inside bool
		want   bool
	}{
		{"watch", false, true},
		{"home-a", false, false},
		{"watch", true, false},
	} {
		rec := &ui.Recorder{}
		saved := reporterOverride
		reporterOverride = rec
		if tc.inside {
			reporterOverride = checkReporter{rec, rec, &[]checkReport{}}
		}
		captureStdout(t, func() {
			runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--sudo=false", "--site", tc.site}, strings.NewReader(""), io.Discard)
		})
		reporterOverride = saved
		got := lines(inSection(rec, "web server"))
		if tc.want && !slices.Equal(got, []string{"step health", "waiting health: checked with --execute", "result Nothing changed. Re-run with --execute to apply; it asks you to type approve for the review above."}) {
			t.Errorf("%s: web server section:\n%s\n\n%s", tc.site, strings.Join(got, "\n"), rec.Lines())
		}
		if !tc.want && rec.Has("section", "web server") {
			t.Errorf("%s (inside %v): a web server section:\n%s", tc.site, tc.inside, rec.Lines())
		}
	}
}

// apply --site --ssh reaches the monitor's web server and the record of
// its site files through the destination it was given, with the sudo
// answer the rest of the apply uses, so the step asks for no password of
// its own.
func TestApplySiteWebServerStepReachesTheSiteAsTheApplyDid(t *testing.T) {
	cfg := fixture(t)
	scope := webServerScope{site: "watch", ssh: "root@203.0.113.9"}
	applied := siteTransport("watch", cfg.Sites["watch"], scope.ssh, true)
	record, ok := scope.recordHost(cfg, "watch", true).(apply.SSHTransport)
	if !ok || record.Describe() != applied.Describe() || record.Auth != applied.Auth {
		t.Errorf("record through %#v", record)
	}
	h := scope.serverHost(cfg, "watch", true)
	plain, _ := h.Plain.(apply.SSHTransport)
	sudo, _ := h.Sudo.(apply.SSHTransport)
	if plain.Describe() != applied.Describe() || plain.Sudo || sudo.Describe() != applied.Describe() || sudo.Auth != applied.Auth {
		t.Errorf("web server through %#v, %#v", h.Plain, h.Sudo)
	}
}
