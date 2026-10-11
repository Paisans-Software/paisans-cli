package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

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
}

func (h *manifestHost) Describe() string { return "watch.example.org" }
func (h *manifestHost) Run(command string) (string, error) {
	return "", errors.New("manifestHost runs nothing: " + command)
}
func (h *manifestHost) RunInput(command, _ string) (string, error) { return h.Run(command) }
func (h *manifestHost) ReadFile(path string) (string, bool, error) {
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
		err = runApply(append([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--sudo=false"}, extra...))
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
	i := rec.Index("note", "status: caddy does not include /etc/caddy/paisans.d")
	if i < 0 {
		t.Fatalf("no warning:\n%s", rec.Lines())
	}
	for _, want := range []string{"    import /etc/caddy/paisans.d/*\n", "/opt/caddy/Caddyfile", "include_dir: false"} {
		if !strings.Contains(rec.Events[i].Extra, want) {
			t.Errorf("warning lacks %q:\n%s", want, rec.Events[i].Extra)
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
	i := rec.Index("note", "status: caddy includes /etc/caddy/paisans.d in a way apply does not write into")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "Replace it with:\n\n    import /etc/caddy/paisans.d/*") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if len(f.server.Files) != 0 {
		t.Errorf("wrote %v", f.server.Files)
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
	i := rec.Index("note", "status: no directory caddy reads to write its site file in")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "- /opt/caddy/paisans.d:/etc/caddy/paisans.d") {
		t.Fatalf("output:\n%s", rec.Lines())
	}
	if len(f.server.Files) != 0 {
		t.Errorf("wrote %v", f.server.Files)
	}
}

// A file the server refuses is removed again, nothing is reloaded, and
// the server's error is in the warning.
func TestApplyReportsTheServersRefusal(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	f.server.Validate = "Error: ambiguous site definition: status.example.org"
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err == nil {
		t.Fatal("complete")
	}
	if len(f.server.Files) != 0 || f.server.Ran("reload") {
		t.Errorf("files %v, asked %v", f.server.Files, f.server.Asked)
	}
	i := rec.Index("note", "watch: the web server refused the change to "+statusFile)
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "ambiguous site definition") {
		t.Fatalf("output:\n%s", rec.Lines())
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
