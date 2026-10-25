package draft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
)

const sha40 = "3f9c2a1e3f9c2a1e3f9c2a1e3f9c2a1e3f9c2a1e"

func TestMarkerRoundTrip(t *testing.T) {
	in := []byte("sites: {}\n")
	for _, sha := range []string{sha40, strings.Repeat("ab", 32)} {
		m := Marker{Revision: 10, SHA: sha}
		out := WithMarker(in, m)
		want := "# paisans: drafted from revision 10 (" + sha + ")\nsites: {}\n"
		if string(out) != want {
			t.Fatalf("got %q, want %q", out, want)
		}
		got, ok := ReadMarker(out)
		if !ok || got != m {
			t.Fatalf("ReadMarker = %v, %v; want %v", got, ok, m)
		}
	}
}

func TestWithMarkerReplaces(t *testing.T) {
	in := []byte("sites: {}\n")
	out := WithMarker(WithMarker(in, Marker{1, sha40}), Marker{2, sha40})
	if n := strings.Count(string(out), "# paisans: drafted from"); n != 1 {
		t.Fatalf("%d marker lines in %q", n, out)
	}
	if m, _ := ReadMarker(out); m.Revision != 2 {
		t.Fatalf("revision = %d", m.Revision)
	}
	if string(StripMarker(out)) != string(in) {
		t.Fatalf("body changed: %q", out)
	}
}

func TestStripMarker(t *testing.T) {
	in := []byte("sites: {}\nother: 1\n")
	if got := StripMarker(WithMarker(in, Marker{3, sha40})); string(got) != string(in) {
		t.Fatalf("got %q", got)
	}
	if got := StripMarker(in); string(got) != string(in) {
		t.Fatalf("unmarked changed: %q", got)
	}
	if got := StripMarker(nil); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestReadMarkerRejects(t *testing.T) {
	for name, in := range map[string]string{
		"not line 1": "sites: {}\n# paisans: drafted from revision 1 (" + sha40 + ")\n",
		"short sha":  "# paisans: drafted from revision 1 (3f9c2a1e)\n",
		"non-hex":    "# paisans: drafted from revision 1 (" + strings.Repeat("z", 40) + ")\n",
		"uppercase":  "# paisans: drafted from revision 1 (" + strings.ToUpper(sha40) + ")\n",
		"revision 0": "# paisans: drafted from revision 0 (" + sha40 + ")\n",
		"negative":   "# paisans: drafted from revision -2 (" + sha40 + ")\n",
		"not number": "# paisans: drafted from revision x (" + sha40 + ")\n",
		"empty":      "",
	} {
		if _, ok := ReadMarker([]byte(in)); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadMarkerCRLF(t *testing.T) {
	if _, ok := ReadMarker([]byte("# paisans: drafted from revision 4 (" + sha40 + ")\r\nsites: {}\r\n")); !ok {
		t.Fatal("CRLF marker rejected")
	}
}

func TestDraftWriteFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	d := Draft{Dir: dir}
	if d.Has() {
		t.Fatal("Has before write")
	}
	f := configstore.Files{Paisans: []byte("sites: {}\n"), Secrets: []byte("sops:\n  version: 3.9.0\n"), SOPS: []byte("creation_rules: []\n")}
	if err := d.Write(f, Marker{7, sha40}); err != nil {
		t.Fatal(err)
	}
	if !d.Has() {
		t.Fatal("Has after write")
	}
	for _, n := range []string{config.FileName, "secrets.enc.yaml", ".sops.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "secrets.enc.yaml")); fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets mode %v", fi.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(dir, ".paisans")); err == nil {
		t.Error(".paisans created")
	}
	got, err := d.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := ReadMarker(got.Paisans); !ok || m.Revision != 7 || m.SHA != sha40 {
		t.Errorf("marker = %v, %v", m, ok)
	}
	if string(StripMarker(got.Paisans)) != "sites: {}\n" || string(got.SOPS) != string(f.SOPS) {
		t.Errorf("files differ: %+v", got)
	}
}

// Mark replaces the line, or adds it, and changes nothing else: the rest of
// paisans-stack.yaml, its mode, and the other two files.
func TestDraftMark(t *testing.T) {
	d := Draft{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(d.Dir, config.FileName), []byte("sites: {}\n# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Dir, configstore.FileSecrets), []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Marker{{Revision: 2, SHA: sha40}, {Revision: 3, SHA: strings.Repeat("c", 40)}} {
		if err := d.Mark(m); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(filepath.Join(d.Dir, config.FileName))
		if got, ok := ReadMarker(body); !ok || got != m || string(StripMarker(body)) != "sites: {}\n# mine\n" {
			t.Fatalf("got %q", body)
		}
	}
	if info, _ := os.Stat(filepath.Join(d.Dir, config.FileName)); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(d.Dir, configstore.FileSecrets)); string(b) != "s\n" {
		t.Errorf("secrets %q", b)
	}
	if _, err := os.Stat(filepath.Join(d.Dir, configstore.FileSOPS)); !os.IsNotExist(err) {
		t.Errorf(".sops.yaml written: %v", err)
	}
}

// A CRLF marker line is stripped whole, its \r with it, and the rest is
// kept byte for byte.
func TestStripMarkerCRLFExact(t *testing.T) {
	rest := "sites: {}\r\nother: 1\r\n"
	in := []byte("# paisans: drafted from revision 4 (" + sha40 + ")\r\n" + rest)
	if got := StripMarker(in); string(got) != rest {
		t.Fatalf("got %q, want %q", got, rest)
	}
}

// A first line that is nearly a marker is no marker: it is kept, and
// WithMarker puts the marker above it rather than replacing it.
func TestNearMissMarkerIsKept(t *testing.T) {
	for name, line := range map[string]string{
		"trailing space":   "# paisans: drafted from revision 4 (" + sha40 + ") ",
		"no space after #": "#paisans: drafted from revision 4 (" + sha40 + ")",
		"other words":      "# paisans: copied from revision 4 (" + sha40 + ")",
		"byte order mark":  "\ufeff# paisans: drafted from revision 4 (" + sha40 + ")",
	} {
		in := []byte(line + "\nsites: {}\n")
		if _, ok := ReadMarker(in); ok {
			t.Errorf("%s: read as a marker", name)
		}
		if got := StripMarker(in); string(got) != string(in) {
			t.Errorf("%s: stripped to %q", name, got)
		}
		if got := WithMarker(in, Marker{5, sha40}); !strings.HasSuffix(string(got), string(in)) {
			t.Errorf("%s: replaced: %q", name, got)
		}
	}
}

// Mark writes through a paisans-stack.yaml that is a symbolic link: the
// file it names gets the line, and the link stays a link.
func TestDraftMarkThroughSymlink(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	target := filepath.Join(elsewhere, "real.yaml")
	if err := os.WriteFile(target, []byte("sites: {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, config.FileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (Draft{Dir: dir}).Mark(Marker{2, sha40}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	body, _ := os.ReadFile(target)
	if m, ok := ReadMarker(body); !ok || m.Revision != 2 || string(StripMarker(body)) != "sites: {}\n" {
		t.Fatalf("the file the link names is %q", body)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

// A file that cannot be checked counts as there, so a folder Paisans cannot
// read is never taken for one without a draft.
func TestHasCountsAFileThatCannotBeChecked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	d := Draft{Dir: dir}
	if !d.Has() || !d.HasAny() {
		t.Fatal("a folder that cannot be read reads as holding no draft")
	}
	if (Draft{Dir: t.TempDir()}).HasAny() {
		t.Fatal("an empty folder holds a draft")
	}
}
