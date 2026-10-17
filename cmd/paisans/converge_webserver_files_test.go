package main

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// manifestHost is a monitor's host as far as the reference files go: files
// read and written whole, the commands that make their directory and remove
// one, and nothing else.
type manifestHost struct {
	files map[string]string
	// written is each path written, in order, and ran each command.
	written, ran []string
	writes       int
	// unreachable fails every read as ssh does for a host it cannot reach.
	unreachable bool
}

func (h *manifestHost) Describe() string { return "watch.example.org" }
func (h *manifestHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	switch {
	case command == hostarch.Command:
		return "amd64\n", nil
	case strings.HasPrefix(command, "install -d -m 755 '"):
		return "", nil
	case strings.HasPrefix(command, "rm -f -- '"):
		delete(h.files, strings.Trim(strings.TrimPrefix(command, "rm -f -- "), "'"))
		return "", nil
	}
	return "", errors.New("manifestHost runs nothing else: " + command)
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
	h.written = append(h.written, path)
	h.writes++
	return nil
}

const manifestFile = "/srv/paisans/f2a9/.paisans-manifest.json"

func (h *manifestHost) record(t *testing.T) []render.ManifestWebServer {
	t.Helper()
	var m render.Manifest
	if content, ok := h.files[manifestFile]; ok {
		if err := json.Unmarshal([]byte(content), &m); err != nil {
			t.Fatal(err)
		}
	}
	return m.WebServer
}

// fakeWebServerFiles writes the reference files for real, on a fake monitor
// host serving the admission record admit writes for the configuration at
// path, whose web server is a Caddy on the host.
func fakeWebServerFiles(t *testing.T, path string) *manifestHost {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &manifestHost{files: map[string]string{admittedPath(cfg): admittedRecord(t, cfg, "watch")}}
	savedFiles, savedConverge := convergeWebServerFiles, convergeHost
	convergeWebServerFiles = writeWebServerFiles
	convergeHost = func(name string, site config.Site) apply.Transport {
		if name == "watch" {
			return h
		}
		return savedConverge(name, site)
	}
	t.Cleanup(func() { convergeWebServerFiles, convergeHost = savedFiles, savedConverge })
	return h
}

const healthReference = "/srv/paisans/f2a9/web-server/health.caddy"

// convergeAt is converge on the configuration at path.
func convergeAt(t *testing.T, path string, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", path, "--secrets", fixtureSecretsPath()}, extra...), strings.NewReader(""), io.Discard)
	})
	return err
}

// touchesTheOwnersServer reports a command that reaches the owner's web
// server or its configuration.
func touchesTheOwnersServer(t *testing.T, ran []string) {
	t.Helper()
	for _, c := range ran {
		for _, s := range []string{"caddy reload", "nginx -s", "apachectl", "docker exec", "docker restart", "/etc/caddy"} {
			if strings.Contains(c, s) {
				t.Errorf("ran against the owner's web server: %s", c)
			}
		}
	}
}

// apply writes the reference snippet from the admission record, in the
// deployment's root, and touches nothing of the owner's web server.
func TestApplyWritesTheReferenceSnippetFromTheRecord(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if !slices.Equal(h.written, []string{healthReference, manifestFile}) {
		t.Errorf("wrote %v", h.written)
	}
	if c := h.files[healthReference]; !strings.Contains(c, "health.example.org {") || !strings.Contains(c, "reverse_proxy 127.0.0.1:8480") || !strings.Contains(c, "for the host's owner") {
		t.Errorf("reference file:\n%s", c)
	}
	touchesTheOwnersServer(t, h.ran)
	got := h.record(t)
	if len(got) != 1 || got[0].Path != healthReference || got[0].Hostname != "health.example.org" || got[0].SHA256 != sha(h.files[healthReference]) || got[0].Server != "caddy" {
		t.Errorf("record %+v", got)
	}
	if !rec.Has("done", "reference file for health") {
		t.Errorf("output:\n%s", rec.Lines())
	}

	// Run again, nothing changed: nothing is written.
	h.written = nil
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(h.written) != 0 {
		t.Errorf("wrote again %v", h.written)
	}
}

// Until the owner's copy serves the app, the step ends on a warning naming
// web-server show for the owner.
func TestApplyWarnsUntilTheOwnersCopyServes(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	rec := recordConverge(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if !errors.As(err, &w) {
		t.Fatalf("err %#v\n%s", err, rec.Lines())
	}
	if hint, explain := ui.Describe(err); !strings.Contains(hint+explain, "paisans web-server show --app health") {
		t.Errorf("warning %q %q", hint, explain)
	}
	if i := rec.Index("warned", "health"); i < 0 || !strings.Contains(rec.Events[i+1].Text, "The host's owner installs what paisans web-server show --app health prints") {
		t.Errorf("output:\n%s", rec.Lines())
	}
	touchesTheOwnersServer(t, h.ran)
}

// When apply rewrites a reference file, the warning says the owner's copy
// needs replacing.
func TestAChangedReferenceSaysTheOwnersCopyNeedsReplacing(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	stale, _ := json.Marshal(render.Manifest{Version: 1, Build: version.Current(), WebServer: []render.ManifestWebServer{{App: "health", Hostname: "health.example.org", Path: healthReference, SHA256: sha("an older snippet"), Server: "caddy"}}})
	h.files[manifestFile] = string(stale)
	h.files[healthReference] = "an older snippet"
	rec := recordConverge(t)
	err := convergeAt(t, path, "--execute")
	var w *ui.Warning
	if !errors.As(err, &w) {
		t.Fatalf("err %#v\n%s", err, rec.Lines())
	}
	if hint, explain := ui.Describe(err); !strings.Contains(hint+explain, "the host's owner replaces their copy") {
		t.Errorf("warning %q %q", hint, explain)
	}
	if i := rec.Index("warned", "health"); i < 0 || !strings.Contains(rec.Events[i+1].Text, "apply rewrote "+healthReference+", so the host's owner replaces their copy") {
		t.Errorf("output:\n%s", rec.Lines())
	}
	if !slices.Equal(h.written, []string{healthReference, manifestFile}) || h.files[healthReference] == "an older snippet" {
		t.Errorf("wrote %v", h.written)
	}
	touchesTheOwnersServer(t, h.ran)
}

// A reference file no longer wanted, its app gone from paisans-stack.yaml,
// is removed and its entry dropped, and the step says which block the
// owner removes.
func TestApplyRemovesAReferenceFileNoLongerWanted(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	gone := "/srv/paisans/f2a9/web-server/blog.caddy"
	m, _ := json.Marshal(render.Manifest{Version: 1, Build: version.Current(), WebServer: []render.ManifestWebServer{{App: "blog", Hostname: "blog.example.org", Path: gone, SHA256: sha("x"), Server: "caddy"}}})
	h.files[manifestFile] = string(m)
	h.files[gone] = "x"
	rec := recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if _, ok := h.files[gone]; ok {
		t.Error("the reference file stayed")
	}
	if got := h.record(t); len(got) != 1 || got[0].Path != healthReference {
		t.Errorf("record %+v", got)
	}
	if i := rec.Index("done", "remove reference file "+gone); i < 0 || !strings.Contains(rec.Events[i].Extra, "the host's owner removes the block for blog.example.org") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// A host that cannot be reached for the record of its reference files is a
// failure, not a warning: the step is marked failed with the error, and the
// run ends failed, exit 1.
func TestApplyFailsWhereTheReferenceRecordCannotBeRead(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, nil)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	h.unreachable = true
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
	i := rec.Index("fail", "reference files on watch")
	if i < 0 || rec.Events[i+1].Kind != "explain" || !strings.Contains(rec.Events[i+1].Text, "Connection timed out") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// A reference file deleted or edited on the host is written again: apply
// compares the file itself, not only the manifest. The owner's copy still
// matches what apply writes, so it is not called a rewrite.
func TestApplyRestoresADeletedOrEditedReferenceFile(t *testing.T) {
	fakeConverge(t, nil)
	fakeWebServerCheck(t, certificateFails)
	path := externalFixture(t)
	h := fakeWebServerFiles(t, path)
	recordConverge(t)
	convergeAt(t, path, "--execute")
	want := h.files[healthReference]
	for name, change := range map[string]func(){
		"deleted": func() { delete(h.files, healthReference) },
		"edited":  func() { h.files[healthReference] = "somebody else's" },
	} {
		change()
		h.written = nil
		rec := recordConverge(t)
		err := convergeAt(t, path, "--execute")
		if h.files[healthReference] != want || !slices.Contains(h.written, healthReference) {
			t.Errorf("%s: not restored, wrote %v", name, h.written)
		}
		if hint, explain := ui.Describe(err); strings.Contains(hint+explain, "replaces their copy") {
			t.Errorf("%s: called a rewrite: %s\n%s", name, explain, rec.Lines())
		}
	}
}

// A manifest entry that is not a reference file of this deployment's, by a
// path with .., an absolute path elsewhere, or a clean path in the root but
// outside web-server/, is never deleted: apply reports it and keeps it.
func TestApplyKeepsAForeignReferenceEntry(t *testing.T) {
	for _, bad := range []string{"/srv/paisans/f2a9/web-server/../../../etc/passwd", "/etc/passwd", "/srv/paisans/f2a9/health/compose.yaml"} {
		fakeConverge(t, nil)
		fakeWebServerCheck(t, nil)
		path := externalFixture(t)
		h := fakeWebServerFiles(t, path)
		m, _ := json.Marshal(render.Manifest{Version: 1, Build: version.Current(), WebServer: []render.ManifestWebServer{{App: "blog", Path: bad, SHA256: sha("x"), Server: "caddy"}}})
		h.files[manifestFile] = string(m)
		h.files[bad] = "x"
		rec := recordConverge(t)
		if err := convergeAt(t, path, "--execute"); err != nil {
			t.Fatalf("%s: %v\n%s", bad, err, rec.Lines())
		}
		if _, ok := h.files[bad]; !ok {
			t.Errorf("%s was deleted", bad)
		}
		for _, c := range h.ran {
			if strings.Contains(c, bad) {
				t.Errorf("ran %s", c)
			}
		}
		if !rec.Has("warn", "not a reference file of this deployment's: "+bad) {
			t.Errorf("%s: not reported:\n%s", bad, rec.Lines())
		}
		if got := h.record(t); len(got) != 2 {
			t.Errorf("%s: record %+v", bad, got)
		}
	}
}
