package version

import (
	"encoding/json"
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"0.2.0", "0.3.0", -1},
		{"0.10.0", "0.9.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.2.1", "0.2.0", 1},
		{"0.3.0-dev", "0.3.0-dev", 0},
		{"0.3.0-dev", "0.2.0", 1},
		{"0.3.0-dev", "0.2.9", 1},
		{"0.3.0-dev", "0.3.0", -1},
		{"0.3.0", "0.3.0-dev", 1},
		{"0.3.0-dev", "0.3.1", -1},
		{"0.4.0-dev", "0.3.0-dev", 1},
		{"1.0.0-dev", "0.99.0", 1},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%s, %s) = %d, %v, want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "0.2", "v0.2.0", "0.2.0 (519299d5cb00)", "0.2.x", "0.-1.0", "0.02.0", "0.2.0.1", "+1.0.0", "1.2.3-rc1", "1.2.3 ", "1.2.3-dev-dev", "1.2.3-DEV", "-dev"} {
		if _, err := Compare(bad, "0.2.0"); err == nil {
			t.Errorf("Compare(%q) ordered it", bad)
		}
	}
}

// The constant is a version Compare orders, so every record a build
// writes can be ordered.
func TestTheVersionConstantIsOrderable(t *testing.T) {
	if !Valid(Version) {
		t.Errorf("Version %q cannot be ordered", Version)
	}
}

const commit = "519299d5cb009da4904cdf53c2025f1ad609f102"

func vcs(modified string) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: commit}, {Key: "vcs.modified", Value: modified}}}
}

// A build records its version, its commit from the first source that
// gives one, and whether the tree was dirty.
func TestFromBuildInfo(t *testing.T) {
	module := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/paisans-software/paisans-cli", Version: v}}
	}
	other := "4b4846760000000000000000000000000000beef"
	for _, c := range []struct {
		name     string
		ldCommit string
		info     *debug.BuildInfo
		want     Build
		string   string
	}{
		{"clean", "", vcs("false"), Build{"0.3.0", commit, false}, "0.3.0 (519299d5cb00)"},
		{"dirty", "", vcs("true"), Build{"0.3.0", commit, true}, "0.3.0 (519299d5cb00-dirty)"},
		{"ldflags, from a tarball", commit, nil, Build{"0.3.0", commit, false}, "0.3.0 (519299d5cb00)"},
		{"ldflags, in its own dirty checkout", commit, vcs("true"), Build{"0.3.0", commit, true}, "0.3.0 (519299d5cb00-dirty)"},
		{"ldflags, beside another checkout", other, vcs("true"), Build{"0.3.0", other, false}, "0.3.0 (4b4846760000)"},
		{"module tag", "", module("v0.3.0-rc.1"), Build{"0.3.0", "v0.3.0-rc.1", false}, "0.3.0 (v0.3.0-rc.1)"},
		{"module pseudo-version", "", module("v0.0.0-20261017120000-519299d5cb00"), Build{"0.3.0", "519299d5cb00", false}, "0.3.0 (519299d5cb00)"},
		{"module pseudo-version after a pre-release", "", module("v0.3.0-rc.1.0.20261017120000-519299d5cb00"), Build{"0.3.0", "519299d5cb00", false}, "0.3.0 (519299d5cb00)"},
		{"module pseudo-version after a release", "", module("v0.3.1-0.20261017120000-519299d5cb00+dirty"), Build{"0.3.0", "519299d5cb00", false}, "0.3.0 (519299d5cb00)"},
		{"a devel build with no commit", "", module("(devel)"), Build{"0.3.0", Unknown, false}, "0.3.0 (unknown)"},
		{"no build information", "", nil, Build{"0.3.0", Unknown, false}, "0.3.0 (unknown)"},
	} {
		got := FromBuildInfo("0.3.0", c.ldCommit, c.info)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.String() != c.string {
			t.Errorf("%s: String() = %q, want %q", c.name, got.String(), c.string)
		}
		if p := got.Problems(); len(p) > 0 {
			t.Errorf("%s: %v", c.name, p)
		}
	}
}

// This binary's build is complete whatever built it: its commit is never
// empty.
func TestCurrentIsComplete(t *testing.T) {
	if b := Current(); b.Commit == "" || len(b.Problems()) > 0 {
		t.Errorf("%+v: %v", b, b.Problems())
	}
}

func TestBuildProblems(t *testing.T) {
	for _, b := range []Build{
		{Version: "0.2", Commit: commit},
		{Version: "0.3.0", Commit: ""},
		{Version: "0.3.0", Commit: "519299d5cb0"},
		{Version: "0.3.0", Commit: "519299D5CB009DA4904CDF53C2025F1AD609F102"},
		{Version: "0.3.0", Commit: "main"},
		{Version: "0.3.0", Commit: Unknown, Modified: true},
		{Version: "0.3.0", Commit: "v0.3.0", Modified: true},
	} {
		if len(b.Problems()) == 0 {
			t.Errorf("%+v passed", b)
		}
	}
}

// The build's keys sit in the record beside its own, and a record without
// every one of them is refused with each missing key named.
func TestRequireBuild(t *testing.T) {
	type record struct {
		Name string `json:"name"`
		Build
	}
	data, err := json.Marshal(record{Name: "x", Build: Build{"0.3.0-dev", commit, true}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"x","toolkit_version":"0.3.0-dev","toolkit_commit":"` + commit + `","toolkit_modified":true}`; string(data) != want {
		t.Errorf("got %s", data)
	}
	var back record
	if err := json.Unmarshal(data, &back); err != nil || back.Build != (Build{"0.3.0-dev", commit, true}) {
		t.Errorf("round trip: %+v %v", back, err)
	}
	if err := RequireBuild(data); err != nil {
		t.Error(err)
	}
	var nb *NoBuildError
	if err := RequireBuild([]byte(`{"name":"x","toolkit_version":"0.2.0"}`)); !errors.As(err, &nb) || len(nb.Missing) != 2 || nb.Missing[0] != "toolkit_commit" {
		t.Errorf("got %v", err)
	}
}

// CheckBuild refuses a build key that is missing, null, of another type, or
// holding a value paisans never records, and reads a sound one.
func TestCheckBuild(t *testing.T) {
	good := `{"toolkit_version":"0.3.0-dev","toolkit_commit":"` + commit + `","toolkit_modified":true}`
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(good), &obj); err != nil {
		t.Fatal(err)
	}
	if b, err := CheckBuild(obj); err != nil || b != (Build{"0.3.0-dev", commit, true}) {
		t.Errorf("got %+v, %v", b, err)
	}
	for bad, want := range map[string]string{
		`{"toolkit_version":"","toolkit_commit":"unknown","toolkit_modified":false}`:      `toolkit_version: "" is not a version`,
		`{"toolkit_version":"next","toolkit_commit":"unknown","toolkit_modified":false}`:  `toolkit_version: "next" is not a version`,
		`{"toolkit_version":null,"toolkit_commit":"unknown","toolkit_modified":false}`:    "toolkit_version: null",
		`{"toolkit_version":"0.3.0","toolkit_commit":null,"toolkit_modified":false}`:      "toolkit_commit: null",
		`{"toolkit_version":"0.3.0","toolkit_commit":"","toolkit_modified":false}`:        `toolkit_commit: "" is not`,
		`{"toolkit_version":"0.3.0","toolkit_commit":"abc","toolkit_modified":false}`:     `toolkit_commit: "abc" is not`,
		`{"toolkit_version":"0.3.0","toolkit_commit":7,"toolkit_modified":false}`:         "toolkit_commit: 7 is not a string",
		`{"toolkit_version":"0.3.0","toolkit_commit":"unknown","toolkit_modified":"yes"}`: `toolkit_modified: "yes" is not true or false`,
		`{"toolkit_version":"0.3.0","toolkit_commit":"unknown","toolkit_modified":true}`:  `toolkit_modified: true, and toolkit_commit "unknown" is no tree`,
		`{"toolkit_version":"0.3.0","toolkit_modified":true}`:                             "toolkit_commit missing",
	} {
		var nb *NoBuildError
		err := RequireBuild([]byte(bad))
		if !errors.As(err, &nb) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", bad, err, want)
		}
	}
}
