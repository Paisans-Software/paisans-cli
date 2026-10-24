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
