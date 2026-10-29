package apply_test

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/version"
)

const manifestAt = "/srv/paisans/f2a9/.paisans-manifest.json"

// appliedBy is a host the fixture was applied to, its manifest then
// rewritten to say build applied it.
func appliedBy(t *testing.T, build version.Build) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	recordBuild(t, host, build)
	return host
}

// recordBuild rewrites the host's manifest to say build wrote it.
func recordBuild(t *testing.T, host *fakeHost, build version.Build) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(host.files[manifestAt]), &m); err != nil {
		t.Fatal(err)
	}
	m["toolkit_version"], m["toolkit_commit"], m["toolkit_modified"] = build.Version, build.Commit, build.Modified
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	host.files[manifestAt] = string(data)
}

// A site a newer release last applied is refused, naming the site and the
// release to upgrade to. The same release from another commit, an older
// release and a host never applied all pass.
func TestCheckToolkit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name  string
		build *version.Build
		newer bool
	}{
		{"newer release", &version.Build{Version: "99.0.0", Commit: commit}, true},
		{"same release, another commit", &version.Build{Version: version.Version, Commit: commit, Modified: true}, false},
		{"older release", &version.Build{Version: "0.0.1", Commit: commit}, false},
		{"never applied", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newHost()
			if tc.build != nil {
				host = appliedBy(t, *tc.build)
			}
			err := apply.CheckToolkit("home-a", host, plan(t).Deployment)
			var n *apply.NewerToolkitError
			if got := errors.As(err, &n); got != tc.newer {
				t.Fatalf("refused as newer: %v, want %v (err %v)", got, tc.newer, err)
			}
			if !tc.newer {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			for _, want := range []string{"home-a", "99.0.0", "Upgrade paisans to 99.0.0 or later", "Nothing was changed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%v", want, err)
				}
			}
		})
	}
}

// An apply by a newer paisans between this one's plan and its execute is
// refused before this one writes anything.
func TestExecuteRefusesANewerApplySinceThePlan(t *testing.T) {
	host := appliedBy(t, version.Current())
	moved := planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["blog"]
		app.Hostname = "words.example.org"
		cfg.Apps["blog"] = app
	})
	p, err := apply.Build("home-a", moved, acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Writes()) == 0 {
		t.Fatal("the changed plan writes nothing, so the test proves nothing")
	}
	recordBuild(t, host, version.Build{Version: "99.0.0", Commit: strings.Repeat("b", 40)})
	host.commands = nil
	before := maps.Clone(host.files)

	var n *apply.NewerToolkitError
	if err := apply.Execute(p, host); !errors.As(err, &n) {
		t.Fatalf("Execute = %v, want a refusal for the newer paisans", err)
	}
	if len(host.commands) != 0 {
		t.Errorf("a refused apply still ran %v", host.commands)
	}
	if !maps.Equal(host.files, before) {
		t.Error("a refused apply still wrote files")
	}
}

// A build of develop counts as every pre-release of its own release, and
// as nothing newer.
func TestNewerThan(t *testing.T) {
	for _, tc := range []struct {
		recorded, have string
		newer          bool
	}{
		{"0.3.1", "0.3.0", true},
		{"0.3.0", "0.3.0-rc.14", true},
		{"0.3.0-rc.14", "0.3.0-rc.13", true},
		{"0.3.0-rc.14", "0.3.0-dev", false},
		{"0.3.0", "0.3.0-dev", true},
		{"0.4.0-rc.1", "0.3.0-dev", true},
		{"0.3.0-dev", "0.3.0-dev", false},
		{"0.3.0-dev", "0.3.0-rc.14", false},
		{"0.2.0", "0.3.0", false},
	} {
		if got := apply.NewerThan(tc.recorded, tc.have); got != tc.newer {
			t.Errorf("newerThan(%s, %s) = %v, want %v", tc.recorded, tc.have, got, tc.newer)
		}
	}
}
