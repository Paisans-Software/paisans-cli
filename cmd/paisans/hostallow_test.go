package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// allowFake is a host's /etc/paisans, in memory: it lists the sidecars and
// writes the list as the write's own command would, only over what was
// read.
type allowFake struct {
	files  map[string]string
	writes int
}

func (h *allowFake) Describe() string { return "ubuntu@home-a.example.org" }

func (h *allowFake) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

var (
	allowPayload = regexp.MustCompile(`printf %s '\\''([A-Za-z0-9+/=]+)'\\'' \| base64 -d`)
	allowSum     = regexp.MustCompile(`= '\\''([0-9a-f]{64})'\\'' \]`)
)

func (h *allowFake) Run(command string) (string, error) {
	if strings.HasPrefix(command, "for f in /etc/paisans/authorized_keys.ubuntu.paisans-*.owned;") {
		var paths []string
		for p := range h.files {
			if strings.HasPrefix(p, "/etc/paisans/authorized_keys.ubuntu.paisans-") {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		return strings.Join(paths, "\n"), nil
	}
	if !strings.Contains(command, "flock '/etc/paisans/authorized_keys.lock'") {
		return "", fmt.Errorf("not under the key lock: %s", command)
	}
	const path = "/etc/paisans/ssh-allow.ubuntu"
	cur, exists := h.files[path]
	if m := allowSum.FindStringSubmatch(command); m != nil {
		sum := sha256.Sum256([]byte(cur))
		if !exists || hex.EncodeToString(sum[:]) != m[1] {
			return hostprep.AllowChangedMarker + "\n", fmt.Errorf("exit status 1")
		}
	} else if exists {
		return hostprep.AllowChangedMarker + "\n", fmt.Errorf("exit status 1")
	}
	m := allowPayload.FindStringSubmatch(command)
	if m == nil {
		return "", fmt.Errorf("no payload: %s", command)
	}
	data, err := base64Decode(m[1])
	if err != nil {
		return "", err
	}
	h.files[path] = data
	h.writes++
	return "", nil
}

func fakeAllowHost(t *testing.T, h *allowFake) {
	t.Helper()
	saved := allowHost
	allowHost = func(string, config.Site, string, bool) hostprep.Reader { return h }
	t.Cleanup(func() { allowHost = saved })
}

const (
	allowPath = "/etc/paisans/ssh-allow.ubuntu"
	bobKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
	carolKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC carol@example.org"
	cloudKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMD cloud-init"
)

func fp(t *testing.T, line string) string {
	t.Helper()
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		t.Fatal(err)
	}
	return k.Fingerprint
}

// withBob is the fixture with bob declared as an admin.
func withBob(t *testing.T) string {
	t.Helper()
	data := strings.Replace(readFixture(t), "sites:\n", "  bob:\n    name: Bob Example\n    email: bob@example.org\n    keys: |\n      "+bobKey+"\nsites:\n", 1)
	return writeConfig(t, data)
}

func allowRun(t *testing.T, run func([]string, io.Reader, io.Writer) error, stdin io.Reader, args ...string) (*ui.Recorder, string, error) {
	t.Helper()
	rec := recordConverge(t)
	var out bytes.Buffer
	err := run(args, stdin, &out)
	return rec, out.String(), err
}

// A new list holds the admin's keys and every key a deployment's sidecar
// already claims for the user, listed apart, and no other user's; the dry
// run shows the file and writes nothing, and --execute asks for the site's
// name.
func TestHostAllowSeedsANewList(t *testing.T) {
	cfg := withBob(t)
	h := &allowFake{files: map[string]string{
		"/etc/paisans/authorized_keys.ubuntu.paisans-f2a9.owned": "added " + fp(t, carolKey) + " carol\n",
		"/etc/paisans/authorized_keys.root.paisans-0c1d.owned":   "added " + fp(t, cloudKey) + " cloud\n",
	}}
	fakeAllowHost(t, h)
	rec, _, err := allowRun(t, runHostAllow, noTerminalStdin(), "home-a", "bob", "--config", cfg)
	if err != nil || h.writes != 0 {
		t.Fatalf("dry run: %v, %d writes\n%s", err, h.writes, rec.Lines())
	}
	if !rec.Has("pending", "allow "+fp(t, bobKey)+" (bob bob@example.org)") || !rec.Has("pending", "keep "+fp(t, carolKey)+" (claimed by paisans-f2a9 for carol)") {
		t.Errorf("the plan:\n%s", rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "explain: "+fp(t, bobKey)+" bob bob@example.org") {
		t.Errorf("the file as it would be is not shown:\n%s", rec.Lines())
	}
	_, out, err := allowRun(t, runHostAllow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "Type home-a to proceed: ") {
		t.Errorf("asked %q", out)
	}
	list := hostprep.ParseAllow(h.files[allowPath])
	if len(list) != 2 || !hostprep.Allows(list, fp(t, bobKey)) || !hostprep.Allows(list, fp(t, carolKey)) {
		t.Errorf("list:\n%s", h.files[allowPath])
	}
}

// An existing list gains the admin's keys and keeps every line it had; a
// key already listed is not listed twice.
func TestHostAllowAddsToAList(t *testing.T) {
	cfg := withBob(t)
	h := &allowFake{files: map[string]string{allowPath: fp(t, carolKey) + " carol\n"}}
	fakeAllowHost(t, h)
	if _, _, err := allowRun(t, runHostAllow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute"); err != nil {
		t.Fatal(err)
	}
	list := hostprep.ParseAllow(h.files[allowPath])
	if len(list) != 2 || list[0].Fingerprint != fp(t, carolKey) {
		t.Errorf("list:\n%s", h.files[allowPath])
	}
	rec, _, err := allowRun(t, runHostAllow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute")
	if err != nil || h.writes != 1 || !rec.Has("done", "allow "+fp(t, bobKey)+" (bob bob@example.org)") {
		t.Errorf("again: %v, %d writes\n%s", err, h.writes, rec.Lines())
	}
}

// Writing the list needs the site's name typed at a terminal; no flag
// answers it.
func TestHostAllowNeedsATerminal(t *testing.T) {
	cfg := withBob(t)
	h := &allowFake{files: map[string]string{}}
	fakeAllowHost(t, h)
	for name, stdin := range map[string]func(t *testing.T) io.Reader{
		"no terminal": func(*testing.T) io.Reader { return noTerminalStdin() },
		"wrong name":  func(t *testing.T) io.Reader { return answering(t, "home-b\n") },
	} {
		if _, _, err := allowRun(t, runHostAllow, stdin(t), "home-a", "bob", "--config", cfg, "--execute"); err == nil || h.writes != 0 {
			t.Errorf("%s: %v, %d writes", name, err, h.writes)
		}
	}
}

// disallow takes the admin's keys, or a fingerprint, off the list; taking
// the last leaves an empty file, which allows nothing, rather than no file,
// which would allow everything. It says a key already authorized stays.
func TestHostDisallowLeavesAnEmptyList(t *testing.T) {
	cfg := withBob(t)
	h := &allowFake{files: map[string]string{allowPath: fp(t, bobKey) + " bob\n" + fp(t, carolKey) + " carol\n"}}
	fakeAllowHost(t, h)
	rec, _, err := allowRun(t, runHostDisallow, answering(t, "home-a\n"), "home-a", "bob", fp(t, carolKey), "--config", cfg, "--execute")
	if err != nil {
		t.Fatal(err)
	}
	content, ok := h.files[allowPath]
	if !ok || len(hostprep.ParseAllow(content)) != 0 {
		t.Errorf("list %v:\n%s", ok, content)
	}
	if !rec.Has("note", "a key already in ubuntu's authorized_keys keeps working") || !rec.Has("note", "the list will be empty, which allows no key") {
		t.Errorf("notes:\n%s", rec.Lines())
	}
}

// With no list there is nothing to take off, and nothing is written: a
// list made by disallow would refuse every key.
func TestHostDisallowWithoutAListDoesNothing(t *testing.T) {
	cfg := withBob(t)
	h := &allowFake{files: map[string]string{}}
	fakeAllowHost(t, h)
	if _, _, err := allowRun(t, runHostDisallow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute"); err != nil || h.writes != 0 {
		t.Errorf("%v, %d writes", err, h.writes)
	}
	if _, ok := h.files[allowPath]; ok {
		t.Error("a list was made")
	}
}

// A comment on the host's list or in a sidecar is the host's text, not the
// toolkit's, and is printed with any control character shown as an escape.
func TestHostAllowShowsHostTextEscaped(t *testing.T) {
	cfg := withBob(t)
	esc := "\x1b[1A\x1b[2Kallow"
	h := &allowFake{files: map[string]string{
		"/etc/paisans/authorized_keys.ubuntu.paisans-f2a9.owned": "added " + fp(t, carolKey) + " carol" + esc + "\n",
	}}
	fakeAllowHost(t, h)
	rec, _, err := allowRun(t, runHostAllow, noTerminalStdin(), "home-a", "bob", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(rec.Lines(), 0x1b) || !strings.Contains(rec.Lines(), `\x1b[1A`) {
		t.Errorf("host allow printed host text raw:\n%q", rec.Lines())
	}
	h = &allowFake{files: map[string]string{allowPath: fp(t, bobKey) + " bob" + esc + "\n" + fp(t, carolKey) + " carol" + esc + "\n"}}
	fakeAllowHost(t, h)
	rec, _, err = allowRun(t, runHostDisallow, noTerminalStdin(), "home-a", "bob", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(rec.Lines(), 0x1b) || !strings.Contains(rec.Lines(), `\x1b[1A`) {
		t.Errorf("host disallow printed host text raw:\n%q", rec.Lines())
	}
}

// A list the owner has written comments into keeps them, and every line
// the command does not change, as written; a line neither command can read
// is refused, named, and nothing is written.
func TestHostAllowKeepsTheOwnersLines(t *testing.T) {
	cfg := withBob(t)
	owned := "# the owner's note\n" + fp(t, carolKey) + "\tcarol, checked by phone\n"
	h := &allowFake{files: map[string]string{allowPath: owned}}
	fakeAllowHost(t, h)
	if _, _, err := allowRun(t, runHostAllow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute"); err != nil {
		t.Fatal(err)
	}
	if got := h.files[allowPath]; !strings.HasPrefix(got, owned) || !strings.Contains(got, fp(t, bobKey)+" bob bob@example.org\n") {
		t.Errorf("allow rewrote the owner's lines:\n%s", got)
	}
	if _, _, err := allowRun(t, runHostDisallow, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute"); err != nil {
		t.Fatal(err)
	}
	if got := h.files[allowPath]; got != owned {
		t.Errorf("disallow rewrote the owner's lines:\n%s", got)
	}

	for name, run := range map[string]func([]string, io.Reader, io.Writer) error{"allow": runHostAllow, "disallow": runHostDisallow} {
		broken := owned + "SHA256:short bob\n"
		h := &allowFake{files: map[string]string{allowPath: broken}}
		fakeAllowHost(t, h)
		_, _, err := allowRun(t, run, answering(t, "home-a\n"), "home-a", "bob", "--config", cfg, "--execute")
		if err == nil || !strings.Contains(problemText(err), "line 3") || !strings.Contains(problemText(err), "SHA256:short bob") || h.writes != 0 {
			t.Errorf("%s: %v, %d writes", name, err, h.writes)
		}
	}
}

func readFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func base64Decode(s string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	return string(data), err
}
