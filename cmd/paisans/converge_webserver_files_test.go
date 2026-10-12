package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
	"github.com/paisans-software/paisans-stack/internal/webserver/webservertest"
)

// manifestHost is a monitor's host as far as its manifest goes: a file
// read and written whole, and nothing else.
type manifestHost struct {
	files  map[string]string
	writes int
	// unreachable fails every read as ssh does for a host it cannot reach.
	unreachable bool
}

func (h *manifestHost) Describe() string { return "watch.example.org" }
func (h *manifestHost) Run(command string) (string, error) {
	return "", errors.New("manifestHost runs nothing: " + command)
}
func (h *manifestHost) RunInput(command, _ string) (string, error) { return h.Run(command) }
func (h *manifestHost) ReadFile(path string) (string, bool, error) {
	if h.unreachable {
		return "", false, errors.New("watch.example.org: ssh: connect to host watch.example.org port 22: Connection timed out")
	}
	content, ok := h.files[path]
	return content, ok, nil
}
func (h *manifestHost) WriteFile(path, content string, _ uint32) error {
	h.files[path] = content
	h.writes++
	return nil
}

func (h *manifestHost) record(t *testing.T) []render.ManifestWebServer {
	t.Helper()
	var m render.Manifest
	if content, ok := h.files["/srv/paisans/f2a9/.paisans-manifest.json"]; ok {
		if err := json.Unmarshal([]byte(content), &m); err != nil {
			t.Fatal(err)
		}
	}
	return m.WebServer
}

// filesFake is the monitor's host for writing its site file: the web
// server on it, its manifest, and the sudo each was reached with.
type filesFake struct {
	server   *webservertest.Server
	manifest *manifestHost
	sudo     []string
}

// fakeWebServerFiles writes the site files for real, on a fake monitor host
// running ws, whose configuration reads conf.
func fakeWebServerFiles(t *testing.T, ws webserver.WebServer, conf string) *filesFake {
	t.Helper()
	f := &filesFake{server: webservertest.New(conf), manifest: &manifestHost{files: map[string]string{}}}
	savedFiles, savedHost, savedConverge := convergeWebServerFiles, webServerFilesHost, convergeHost
	convergeWebServerFiles = writeWebServerFiles
	webServerFilesHost = func(_ *config.Config, site string, sudo bool) webserver.Host {
		f.sudo = append(f.sudo, fmt.Sprintf("%s sudo=%v", site, sudo))
		h := webserver.Host{Plain: f.server.Plain()}
		if sudo {
			h.Sudo = f.server.Sudo()
		}
		return h
	}
	convergeHost = func(name string, site config.Site, sudo bool) apply.Transport {
		if name == "watch" {
			return f.manifest
		}
		return savedConverge(name, site, sudo)
	}
	t.Cleanup(func() {
		convergeWebServerFiles, webServerFilesHost, convergeHost = savedFiles, savedHost, savedConverge
	})
	fakeWebServer(t, ws)
	return f
}

// mountedCaddy is a Caddy in a container with its configuration directory
// mounted whole.
var mountedCaddy = webserver.WebServer{
	Server: webserver.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Mounts: []webserver.Mount{{Source: "/opt/caddy", Destination: "/etc/caddy"}},
	Certs:  webserver.CertsAutomatic,
}

const statusFile = "/opt/caddy/paisans.d/paisans-f2a9-status.caddy"

// convergeAt is converge on the configuration at path.
func convergeAt(t *testing.T, path string, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--sudo=false"}, extra...), strings.NewReader(""), io.Discard)
	})
	return err
}

// With the directory included by a wildcard, apply writes the app's site
// file into it, validates and reloads the server, records the file in the
// manifest, and writes the directory it derived into paisans.yaml with a
// warning saying so. The next run finds it unchanged and reloads nothing.
func TestApplyWritesTheSiteFileWhereTheServerIncludesIt(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	before, _ := os.ReadFile(path)
	f := fakeWebServerFiles(t, mountedCaddy, "{\n\temail ops@example.org\n}\nimport /etc/caddy/paisans.d/*\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	want := strings.Replace(string(before), "      listen: 127.0.0.1:8480\n", "      listen: 127.0.0.1:8480\n      include_dir: /opt/caddy/paisans.d\n", 1)
	if string(after) != want {
		t.Errorf("paisans.yaml:\n%s", after)
	}
	if i := rec.Index("note", "watch: include_dir set to /opt/caddy/paisans.d in paisans.yaml"); i < 0 {
		t.Errorf("no warning for the yaml edit:\n%s", rec.Lines())
	}
	if !strings.Contains(f.server.Files[statusFile], "reverse_proxy 127.0.0.1:8480") {
		t.Errorf("files %v", f.server.Files)
	}
	if !f.server.Ran("caddy validate") || !f.server.Ran("caddy reload") {
		t.Errorf("asked %v", f.server.Asked)
	}
	if got := f.manifest.record(t); len(got) != 1 || got[0].Path != statusFile || got[0].Container != "caddy-1" {
		t.Errorf("record %+v", got)
	}
	if i := rec.Index("done", "site file for status"); i < 0 || rec.Events[i].Extra != statusFile+" written, caddy reloaded" || !rec.Has("result", "Deployment complete.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
	got := lines(inSection(rec, "web server"))
	if i, j := slices.Index(got, "step site file for status"), slices.Index(got, "step status"); i < 0 || j < i {
		t.Errorf("the file is not written ahead of the check:\n%s", strings.Join(got, "\n"))
	}

	f.server.Asked = nil
	rec = recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if f.server.Ran("printf") || f.server.Ran("reload") || rec.Has("note", "include_dir set") {
		t.Errorf("a second run changed something: %v\n%s", f.server.Asked, rec.Lines())
	}
	if again, _ := os.ReadFile(path); string(again) != want {
		t.Errorf("paisans.yaml changed again:\n%s", again)
	}
}

// With no line including the directory, nothing is written, nothing goes
// into paisans.yaml, the warning carries the one line to add, and the run
// does not end as complete.
func TestApplyWritesNothingWhereTheServerDoesNotIncludeIt(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	before, _ := os.ReadFile(path)
	f := fakeWebServerFiles(t, mountedCaddy, "blog.example.org {\n\treverse_proxy 127.0.0.1:2368\n}\n")
	rec := recordConverge(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if !errors.As(err, &w) || !strings.Contains(w.Hint, "status") {
		t.Fatalf("err %v", err)
	}
	if rec.Has("result", "Deployment complete.") {
		t.Errorf("said complete:\n%s", rec.Lines())
	}
	if len(f.server.Files) != 0 || f.server.Ran("printf") || f.server.Ran("reload") || f.manifest.writes != 0 {
		t.Errorf("wrote %v, asked %v", f.server.Files, f.server.Asked)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("paisans.yaml was edited:\n%s", after)
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || rec.Events[i].Extra != "caddy does not include /etc/caddy/paisans.d" || rec.Events[i+1].Kind != "explain" {
		t.Fatalf("no warning:\n%s", rec.Lines())
	}
	for _, want := range []string{"    import /etc/caddy/paisans.d/*\n", "/opt/caddy/Caddyfile", "include_dir: false", "after the global options block if it has one", "    {\n        email admin@example.org\n    }\n\n    import /etc/caddy/paisans.d/*\n\n    example.org {\n"} {
		if !strings.Contains(rec.Events[i+1].Text, want) {
			t.Errorf("warning lacks %q:\n%s", want, rec.Events[i+1].Text)
		}
	}
}

// A literal include is refused with the wildcard line to use instead.
func TestApplyRefusesALiteralInclude(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/paisans-f2a9-status.caddy\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err == nil {
		t.Fatal("complete")
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || rec.Events[i].Extra != "caddy includes /etc/caddy/paisans.d by a path, not a wildcard" || !strings.Contains(rec.Events[i+1].Text, "Replace it with:\n\n    import /etc/caddy/paisans.d/*") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if len(f.server.Files) != 0 {
		t.Errorf("wrote %v", f.server.Files)
	}
}

// An import above Caddy's global options block validates while the
// directory is empty, and fails once a site file is in it: the site comes
// ahead of the options. Apply writes nothing, and says to move the line,
// for a Caddy in a container and one on the host itself.
func TestApplyRefusesAnImportAboveTheGlobalOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		ws   webserver.WebServer
		conf string
	}{
		{"docker", mountedCaddy, "/opt/caddy/Caddyfile"},
		{"bare metal", bareCaddy, "/etc/caddy/Caddyfile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeConverge(t, nil)
			path := externalFixture(t)
			before, _ := os.ReadFile(path)
			f := fakeWebServerFiles(t, tc.ws, "# sites from paisans\nimport /etc/caddy/paisans.d/*\n\n{\n\temail ops@example.org\n}\n\nblog.example.org {\n\treverse_proxy 127.0.0.1:2368\n}\n")
			rec := recordConverge(t)
			var w *ui.Warning
			if err := convergeAt(t, path, "--execute"); !errors.As(err, &w) {
				t.Fatalf("err %v", err)
			}
			if len(f.server.Files) != 0 || f.server.Ran("printf") || f.server.Ran("validate") || f.server.Ran("reload") {
				t.Errorf("wrote %v, asked %v", f.server.Files, f.server.Asked)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Errorf("paisans.yaml was edited:\n%s", after)
			}
			i := rec.Index("warned", "site file for status")
			if i < 0 || rec.Events[i].Extra != "caddy imports /etc/caddy/paisans.d above its global options block" || rec.Events[i+1].Kind != "explain" {
				t.Fatalf("output:\n%s", rec.Lines())
			}
			for _, want := range []string{
				"Nothing was written. " + tc.conf + " has:\n\n    import /etc/caddy/paisans.d/*\n",
				"server block without any key is global configuration, and if used, it must be first",
				"Move the import line to after the global options block:\n\n    {\n        email admin@example.org\n    }\n\n    import /etc/caddy/paisans.d/*\n\n    example.org {\n",
				"then run paisans apply --execute again.",
			} {
				if !strings.Contains(rec.Events[i+1].Text, want) {
					t.Errorf("warning lacks %q:\n%s", want, rec.Events[i+1].Text)
				}
			}
		})
	}
}

// An import inside a block, a site or a snippet, puts the site files'
// site blocks inside another: apply writes nothing and says to move it to
// the top level.
func TestApplyRefusesAnImportInsideABlock(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "{\n\temail ops@example.org\n}\n\nblog.example.org {\n\timport /etc/caddy/paisans.d/*\n\treverse_proxy 127.0.0.1:2368\n}\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err == nil {
		t.Fatal("complete")
	}
	if len(f.server.Files) != 0 || f.server.Ran("validate") {
		t.Errorf("wrote %v, asked %v", f.server.Files, f.server.Asked)
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || rec.Events[i].Extra != "caddy imports /etc/caddy/paisans.d inside a block" {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	for _, want := range []string{
		"inside a block",
		"Move the import line out of every block, to the top level of the Caddyfile, after the global options block if it has one:\n\n    {\n",
	} {
		if !strings.Contains(rec.Events[i+1].Text, want) {
			t.Errorf("warning lacks %q:\n%s", want, rec.Events[i+1].Text)
		}
	}
}

// include_dir: false writes nothing and asks the web server nothing: the
// operator adds the snippet themselves, and the check is as before.
func TestApplyWithIncludeDirFalseWritesNothing(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "      listen: 127.0.0.1:8480\n", "      listen: 127.0.0.1:8480\n      include_dir: false\n", 1)), 0o600)
	before, _ := os.ReadFile(path)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(f.server.Asked) != 0 || len(f.sudo) != 0 {
		t.Errorf("the web server was asked %v", f.server.Asked)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("paisans.yaml was edited")
	}
	if !rec.Has("result", "Deployment complete.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// include_dir: false with files recorded from before removes them, and
// only them.
func TestApplyWithIncludeDirFalseRemovesWhatItWrote(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	f.server.Files["/opt/caddy/paisans.d/blog.caddy"] = "blog.example.org {\n}\n"
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "include_dir: /opt/caddy/paisans.d", "include_dir: false", 1)), 0o600)
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.server.Files[statusFile]; ok {
		t.Errorf("the file stayed:\n%s", rec.Lines())
	}
	if _, ok := f.server.Files["/opt/caddy/paisans.d/blog.caddy"]; !ok {
		t.Error("somebody else's file went")
	}
	if got := f.manifest.record(t); len(got) != 0 {
		t.Errorf("record %+v", got)
	}
}

// A container mounting its Caddyfile alone has nowhere to write: nothing
// is recorded, and the warning names the mount to add.
func TestApplyAdvisesTheMountForASingleFileMount(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	before, _ := os.ReadFile(path)
	ws := mountedCaddy
	ws.Mounts = []webserver.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}}
	f := fakeWebServerFiles(t, ws, "import /etc/caddy/paisans.d/*\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err == nil {
		t.Fatal("complete")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("paisans.yaml was edited:\n%s", after)
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || rec.Events[i].Extra != "no directory caddy reads to write in" || !strings.Contains(rec.Events[i+1].Text, "- /opt/caddy/paisans.d:/etc/caddy/paisans.d") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if len(f.server.Files) != 0 {
		t.Errorf("wrote %v", f.server.Files)
	}
}

// A file the server refuses is removed again, nothing is reloaded, and
// the server's error is on the step's warning. The run ends on a warning.
func TestApplyReportsTheServersRefusal(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	f.server.Validate = "Error: ambiguous site definition: status.example.org"
	rec := recordConverge(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if !errors.As(err, &w) {
		t.Fatalf("err %v", err)
	}
	if len(f.server.Files) != 0 || f.server.Ran("reload") || f.server.Ran("restart") {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || rec.Events[i].Extra != "caddy refused "+statusFile || !strings.Contains(rec.Events[i+1].Text, "ambiguous site definition") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
}

// A Docker server whose graceful reload fails is restarted, and the step
// says so.
func TestApplyRestartsAWebServerWhoseReloadFails(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	f.server.Reload = "Error: sending configuration to instance: connection refused"
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !f.server.Ran("docker restart 'caddy-1'") || f.server.Files[statusFile] == "" {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	if i := rec.Index("done", "site file for status"); i < 0 || rec.Events[i].Extra != statusFile+" written, container caddy-1 restarted: its reload failed" {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// bareCaddy is a Caddy on the host itself, run by systemd.
var bareCaddy = webserver.WebServer{
	Server: webserver.ServerCaddy, Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Certs: webserver.CertsAutomatic,
}

// A web server on the host itself is validated with the file and not
// reloaded: the step warns with the command that reloads it, the check
// failing until then is left to --verbose and says no more than that it
// follows, and the run ends on a warning.
func TestApplyLeavesTheReloadOfABareMetalServer(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, bareCaddy, "import /etc/caddy/paisans.d/*\n")
	f.server.Units = []string{"caddy"}
	rec := recordConverge(t)
	rec.Verbose_ = true
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if !errors.As(err, &w) {
		t.Fatalf("err %v", err)
	}
	const file = "/etc/caddy/paisans.d/paisans-f2a9-status.caddy"
	if f.server.Files[file] == "" || !f.server.Ran("caddy validate") || f.server.Ran("caddy reload") || f.server.Ran("systemctl reload") || f.server.Ran("restart") {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	var shown []string
	for _, l := range lines(inSection(rec, "web server")) {
		if !strings.HasPrefix(l, "progress ") && !strings.HasPrefix(l, "note watch: include_dir set") {
			shown = append(shown, l)
		}
	}
	sameLines(t, shown, []string{
		"step site file for status",
		"warned site file for status: " + file + " written, caddy not reloaded",
		"explain caddy's configuration is valid with it, and caddy runs on the host itself, so apply leaves its reload to you:\n\n    sudo systemctl reload caddy\n\nthen run paisans web-server check --app status.",
		"step status",
		"warned status: status.example.org",
		"explain certificate: the certificate is not valid for status.example.org: it names watch.example.org, expected until caddy is reloaded, see above.",
	})
}

// tlsInternal is the status app's checks against a Caddy with no
// certificate for its hostname: each HTTPS request fails the same way.
var tlsInternal = map[string][]webserver.Result{"status": {
	{Name: webserver.CheckDNS, OK: true, Detail: "status.example.org resolves to 203.0.113.20"},
	{Name: webserver.CheckCertificate, Detail: `https://status.example.org/healthz: Get "https://status.example.org/healthz": remote error: tls: internal error`},
	{Name: webserver.CheckSignIn, Detail: `https://status.example.org/login/oidc: Get "https://status.example.org/login/oidc": remote error: tls: internal error`},
	{Name: webserver.CheckUpstream, OK: true, Detail: "203.0.113.20:8480 refused"},
}}

// Where the site file step warned with its own instructions, the check
// that fails because of it only restates them: its line is left out, and
// the run still ends on a warning naming the app. With --verbose it is
// there, one line naming what failed, the same error said once, and that it
// follows from the step above.
func TestApplyGivesOneSetOfInstructionsPerProblem(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		fakeConverge(t, nil)
		fakeWebServerCheck(t, tlsInternal)
		path := externalFixture(t)
		ws := mountedCaddy
		ws.Mounts = []webserver.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}}
		fakeWebServerFiles(t, ws, "import /etc/caddy/paisans.d/*\n")
		rec := recordConverge(t)
		rec.Verbose_ = verbose
		err := convergeAt(t, path, "--execute")
		var w *ui.Warning
		if !errors.As(err, &w) || w.Hint != "Deployment complete, but 1 app failed its web server check: status" {
			t.Fatalf("verbose=%v: err %v", verbose, err)
		}
		var b strings.Builder
		if code := reportError(&b, err, false); code != 0 {
			t.Errorf("exit %d:\n%s", code, b.String())
		}
		var shown []string
		for _, l := range lines(inSection(rec, "web server")) {
			if !strings.HasPrefix(l, "progress ") {
				shown = append(shown, l)
			}
		}
		want := []string{
			"step site file for status",
			"warned site file for status: no directory caddy reads to write in",
		}
		if verbose {
			want = append(want,
				"step status",
				"warned status: status.example.org",
				"explain healthz and login/oidc: tls: internal error (caddy has no certificate for this name), expected until its site file is in place, see above.",
			)
		}
		if len(shown) != len(want)+1 {
			t.Fatalf("verbose=%v: section:\n%s", verbose, strings.Join(shown, "\n"))
		}
		sameLines(t, append(shown[:2:2], shown[3:]...), want)
		if !strings.HasPrefix(shown[2], "explain The container caddy-1 mounts /opt/caddy/Caddyfile alone") || strings.Count(shown[2], "paisans web-server show --app status") != 1 {
			t.Errorf("the site file's instructions:\n%s", shown[2])
		}
	}
}

// composeCaddy is a Caddy in a container compose started from /opt/x,
// mounting its Caddyfile alone.
func composeCaddy(t *testing.T, compose string) *filesFake {
	t.Helper()
	ws := mountedCaddy
	ws.Config = "/opt/x/Caddyfile"
	ws.Mounts = []webserver.Mount{{Source: "/opt/x/Caddyfile", Destination: "/etc/caddy/Caddyfile"}}
	f := fakeWebServerFiles(t, ws, "import /etc/caddy/paisans.d/*\n")
	f.server.Labels = `{"com.docker.compose.project":"x","com.docker.compose.service":"web","com.docker.compose.project.working_dir":"/opt/x","com.docker.compose.project.config_files":"/opt/x/compose.yaml"}`
	f.server.Files["/opt/x/compose.yaml"] = compose
	return f
}

// The site file step reads the compose file that started the server's
// container and says what is wrong with it: here a mount declared on the
// server's service, for a container created before it. Only where nothing
// is declared does it offer include_dir: false as well. Nothing on the
// host is changed.
func TestApplyDiagnosesTheMountFromTheComposeFile(t *testing.T) {
	for _, tc := range []struct {
		name, compose, want string
		optOut              bool
	}{
		{"declared", "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n      - ./paisans.d:/etc/caddy/paisans.d\n", "declares /opt/x/paisans.d:/etc/caddy/paisans.d for web, but the running container caddy-1 was created before it", false},
		{"another service", "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n  monitor:\n    volumes:\n      - /opt/x/paisans.d:/etc/caddy/paisans\n", "for monitor, but caddy runs in web's container caddy-1", false},
		{"another path", "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n      - ./paisans.d:/etc/caddy/paisans\n", "change /etc/caddy/paisans to /etc/caddy/paisans.d", false},
		{"not declared", "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n", "Add this line to the volumes of web in /opt/x/compose.yaml", true},
	} {
		fakeConverge(t, nil)
		path := externalFixture(t)
		f := composeCaddy(t, tc.compose)
		rec := recordConverge(t)
		if err := convergeAt(t, path, "--execute"); err == nil {
			t.Fatalf("%s: complete", tc.name)
		}
		i := rec.Index("warned", "site file for status")
		if i < 0 || rec.Events[i].Extra != "no directory caddy reads to write in" {
			t.Fatalf("%s: output:\n%s", tc.name, rec.Lines())
		}
		text := rec.Events[i+1].Text
		if !strings.Contains(text, tc.want) || !strings.Contains(text, "cd /opt/x && docker compose up -d web") {
			t.Errorf("%s: instructions:\n%s", tc.name, text)
		}
		if strings.Contains(text, "include_dir: false") != tc.optOut {
			t.Errorf("%s: include_dir: false offered %v:\n%s", tc.name, !tc.optOut, text)
		}
		for _, c := range f.server.Asked {
			if strings.Contains(c, "compose up") || strings.Contains(c, "restart") || strings.Contains(c, "printf") || strings.Contains(c, "rm -f") {
				t.Errorf("%s: changed the host: %s", tc.name, c)
			}
		}
	}
}

// A site file already in place on a Docker server whose check fails is
// validated and the server reloaded, to take in an edit of the operator's,
// and the app checked once more.
func TestApplyReloadsAndChecksAgainWhenTheCheckFails(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	checks := 0
	saved := convergeWebServerCheck
	convergeWebServerCheck = func(target webserver.Target) []webserver.Result {
		checks++
		if checks == 1 {
			return tlsInternal["status"]
		}
		return []webserver.Result{{Name: webserver.CheckCertificate, OK: true, Detail: "https://status.example.org/healthz answered 200"}}
	}
	t.Cleanup(func() { convergeWebServerCheck = saved })
	f.server.Asked = nil
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if checks != 2 || f.server.Ran("printf") || !f.server.Ran("caddy validate") || !f.server.Ran("caddy reload") {
		t.Errorf("checked %d times, asked %v", checks, f.server.Asked)
	}
	if !rec.Has("done", "status") || !rec.Has("result", "Deployment complete.") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// A site file just written and its Caddy just reloaded may leave Caddy still
// obtaining the certificate, so the check waits for it, saying so. A site
// file in place and unchanged is checked once, since this run changed
// nothing to wait on, and only after the reload is waited on.
func TestApplyWaitsForTheCertificateOfAReloadedServer(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	waits := recordWaits(t)
	checks := 0
	saved := convergeWebServerCheck
	convergeWebServerCheck = func(target webserver.Target) []webserver.Result {
		checks++
		if checks < 3 {
			return tlsInternal["status"]
		}
		return []webserver.Result{{Name: webserver.CheckCertificate, OK: true, Detail: "https://status.example.org/healthz answered 200"}}
	}
	t.Cleanup(func() { convergeWebServerCheck = saved })
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if checks != 3 || len(*waits) != 2 || !rec.Has("progress", "waiting for caddy's certificate") {
		t.Errorf("checked %d times, waited %v:\n%s", checks, *waits, rec.Lines())
	}

	checks, *waits = 0, nil
	convergeWebServerCheck = func(target webserver.Target) []webserver.Result {
		checks++
		return tlsInternal["status"]
	}
	f.server.Asked = nil
	rec = recordConverge(t)
	convergeAt(t, path, "--execute")
	if !f.server.Ran("caddy reload") || checks != len(*waits)+2 || total(*waits) != time.Minute {
		t.Errorf("checked %d times, waited %v, asked %v", checks, *waits, f.server.Asked)
	}
	if !rec.Has("warned", "status") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// A host that cannot be reached for the record of its site files is a
// failure, not a warning: the step is marked failed with the error, and the
// run ends failed, exit 1.
func TestApplyFailsWhereTheSiteFilesRecordCannotBeRead(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	f.manifest.unreachable = true
	rec := recordConverge(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if err == nil || errors.As(err, &w) {
		t.Fatalf("err %#v", err)
	}
	var b strings.Builder
	if code := reportError(&b, err, false); code != 1 || !strings.HasPrefix(b.String(), "FAIL ") {
		t.Errorf("exit %d:\n%s", code, b.String())
	}
	i := rec.Index("fail", "site files on watch")
	if i < 0 || rec.Events[i+1].Kind != "explain" || !strings.Contains(rec.Events[i+1].Text, "Connection timed out") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// The site files are written with the run's sudo: the host they are
// written through is the one webServerHost builds, which shares the answer
// the run settled (TestWebServerHostSharesTheRunsSudo).
func TestApplyWritesTheSiteFileWithTheRunsSudo(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.sudo, []string{"watch sudo=false"}) {
		t.Errorf("reached %v", f.sudo)
	}
	h := webServerFilesHost
	webServerFilesHost = webServerHost
	defer func() { webServerFilesHost = h }()
	got := webServerFilesHost(fixture(t), "watch", true)
	if s, ok := got.Sudo.(apply.SSHTransport); !ok || s.Auth != sudoAuthFor(s.Describe()) {
		t.Errorf("sudo %#v", got.Sudo)
	}
}

// A hostname renamed in paisans.yaml rewrites the app's file in place: the
// old name is served no more once the server reloads.
func TestApplyRewritesTheSiteFileForARenamedHostname(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "hostname: status.example.org", "hostname: uptime.example.org", 1)), 0o600)
	f.server.Asked = nil
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	content := f.server.Files[statusFile]
	if !strings.Contains(content, "uptime.example.org {") || strings.Contains(content, "status.example.org") || !f.server.Ran("caddy reload") {
		t.Errorf("file:\n%s\n%s", content, rec.Lines())
	}
	if got := f.manifest.record(t); len(got) != 1 {
		t.Errorf("record %+v", got)
	}
}

// A global acme_dns reaches the site's own issuer too: the file is written,
// valid and harmless, and the step warns with the line to rewrite, as any
// other problem with the operator's web server does.
func TestApplyWarnsOfAGlobalACMEDNS(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, acmeDNSCaddy, "{\n\tacme_dns cloudflare {$CF_API_TOKEN}\n}\nimport /etc/caddy/paisans.d/*\n")
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err == nil || !strings.Contains(err.Error(), "the web server needs you: status") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(f.server.Files[statusFile], "issuer acme") || !f.server.Ran("caddy reload") {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	i := rec.Index("warned", "site file for status")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "acme_dns") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if !rec.Has("explain", "cert_issuer acme {") {
		t.Errorf("no rewrite:\n%s", rec.Lines())
	}
}
