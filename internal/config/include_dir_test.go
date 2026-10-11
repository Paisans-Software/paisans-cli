package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

func setIncludeDir(t *testing.T, content, dir string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans.yaml")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	err := config.SetIncludeDir(path, "watch", dir)
	got, _ := os.ReadFile(path)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	return string(got), err
}

// In a block mapping the directory is a line of its own after the block's
// last, indented as its first, and every other byte is kept.
func TestSetIncludeDirInABlock(t *testing.T) {
	in := `sites:
  watch:
    roles: [monitor]   # the uptime monitor
    # behind the web server already on the machine
    ingress:
      mode: external
      listen: 127.0.0.1:8480   # loopback only

  # another site
  vm:
    roles: [gateway]
`
	want := strings.Replace(in, "      listen: 127.0.0.1:8480   # loopback only\n", "      listen: 127.0.0.1:8480   # loopback only\n      include_dir: /opt/caddy/paisans.d\n", 1)
	got, err := setIncludeDir(t, in, "/opt/caddy/paisans.d")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// In a flow mapping, as the README writes it, the key follows the last value.
func TestSetIncludeDirInAFlowMapping(t *testing.T) {
	in := "sites:\n  watch:\n    roles: [monitor]\n    ingress: { mode: external, listen: 127.0.0.1:8480 }   # behind nginx\n"
	want := "sites:\n  watch:\n    roles: [monitor]\n    ingress: { mode: external, listen: 127.0.0.1:8480, include_dir: /etc/nginx/paisans.d }   # behind nginx\n"
	got, err := setIncludeDir(t, in, "/etc/nginx/paisans.d")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A file whose last line has no newline still gets the key on a line of
// its own, and one already written is replaced in its own quoting.
func TestSetIncludeDirAtTheEndAndOverAnOldOne(t *testing.T) {
	got, err := setIncludeDir(t, "sites:\n  watch:\n    ingress:\n      mode: external", "/etc/apache2/paisans.d")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sites:\n  watch:\n    ingress:\n      mode: external\n      include_dir: /etc/apache2/paisans.d\n"; got != want {
		t.Errorf("got %q", got)
	}
	got, err = setIncludeDir(t, "sites:\n  watch:\n    ingress:\n      include_dir: \"/old\"  # was\n      mode: external\n", "/new/paisans.d")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sites:\n  watch:\n    ingress:\n      include_dir: \"/new/paisans.d\"  # was\n      mode: external\n"; got != want {
		t.Errorf("got %q", got)
	}
}

// No ingress block, or a directory that is not a plain absolute path, is
// refused and the file left alone.
func TestSetIncludeDirRefuses(t *testing.T) {
	in := "sites:\n  watch:\n    roles: [monitor]\n"
	if got, err := setIncludeDir(t, in, "/opt/caddy/paisans.d"); err == nil || got != in {
		t.Errorf("err %v, file %q", err, got)
	}
	in = "sites:\n  watch:\n    ingress:\n      mode: external\n"
	if got, err := setIncludeDir(t, in, "relative/paisans.d"); err == nil || got != in {
		t.Errorf("err %v, file %q", err, got)
	}
}

// include_dir reads as a path or false; true names nothing and is refused.
func TestIncludeDirReadsAPathOrFalse(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  config.IncludeDir
		fails bool
	}{
		{"/opt/caddy/paisans.d", config.IncludeDir{Path: "/opt/caddy/paisans.d"}, false},
		{"false", config.IncludeDir{Off: true}, false},
		{"true", config.IncludeDir{}, true},
		{"[a, b]", config.IncludeDir{}, true},
	} {
		cfg, err := loadIngress(t, tc.value)
		if (err != nil) != tc.fails {
			t.Errorf("%s: err %v", tc.value, err)
			continue
		}
		if err == nil && cfg.Sites["watch"].Ingress.IncludeDir != tc.want {
			t.Errorf("%s: got %+v", tc.value, cfg.Sites["watch"].Ingress.IncludeDir)
		}
	}
}

func TestIncludeDirProblem(t *testing.T) {
	for dir, bad := range map[string]bool{
		"/opt/caddy/paisans.d": false,
		"/etc/nginx/paisans.d": false,
		"paisans.d":            true,
		"/":                    true,
		"/etc/nginx/":          true,
		"/etc/../nginx":        true,
		"/etc/my dir":          true,
		"/etc/*":               true,
		"/etc/x;rm":            true,
	} {
		if got := config.IncludeDirProblem(dir) != ""; got != bad {
			t.Errorf("%q: problem %v, want %v", dir, got, bad)
		}
	}
}

func loadIngress(t *testing.T, includeDir string) (*config.Config, error) {
	t.Helper()
	var in config.Ingress
	if err := yaml.Unmarshal([]byte("mode: external\ninclude_dir: "+includeDir+"\n"), &in); err != nil {
		return nil, err
	}
	return &config.Config{Sites: map[string]config.Site{"watch": {Ingress: &in}}}, nil
}
