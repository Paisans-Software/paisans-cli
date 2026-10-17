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
		{"0.3.0-dev", "0.3.0-rc.1", -1},
		{"0.3.0-rc.1", "0.3.0-rc.2", -1},
		{"0.3.0-rc.2", "0.3.0-rc.10", -1},
		{"0.3.0-rc.10", "0.3.0", -1},
		{"0.3.0-rc.1", "0.2.0", 1},
		{"0.3.0-rc", "0.3.0-rc.1", -1},
		{"0.3.0-1", "0.3.0-alpha", -1},
		{"0.3.0-rc.1", "0.3.0-rc.1", 0},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%s, %s) = %d, %v, want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "0.2", "v0.2.0", "0.2.0 (519299d5cb00)", "0.2.x", "0.-1.0", "0.02.0", "0.2.0.1", "+1.0.0", "1.2.3 ", "-dev", "1.2.3-", "1.2.3-rc..1", "1.2.3-rc.01", "1.2.3+build", "1.2.3-rc.1+build", "1.2.3-r c"} {
		if _, err := Compare(bad, "0.2.0"); err == nil {
			t.Errorf("Compare(%q) ordered it", bad)
		}
	}
}

// The constant is a version Compare orders, so every record a build
// writes can be ordered.
// The chain a release passes through orders as each step is cut.
func TestAReleaseChainOrders(t *testing.T) {
	chain := []string{"0.2.0", "0.3.0-dev", "0.3.0-rc.1", "0.3.0-rc.2", "0.3.0-rc.10", "0.3.0", "0.3.1-dev", "0.4.0-dev"}
	for i := 1; i < len(chain); i++ {
		if c, err := Compare(chain[i-1], chain[i]); err != nil || c >= 0 {
			t.Errorf("%s against %s: %d, %v", chain[i-1], chain[i], c, err)
		}
	}
	for _, v := range []string{"0.3.0-rc.1", "1.2.3-rc1", "1.2.3-DEV"} {
		if !Valid(v) {
			t.Errorf("%s is not valid", v)
		}
	}
}

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
		{"ldflags, a release tag", "v0.3.0-rc.1", nil, Build{"0.3.0", "v0.3.0-rc.1", false}, "0.3.0 (v0.3.0-rc.1)"},
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
	// No source names a commit: a devel build outside git, or no build
	// information at all. Such a build names none, and writes nothing.
	for name, info := range map[string]*debug.BuildInfo{"a devel build": module("(devel)"), "no build information": nil} {
		got := FromBuildInfo("0.3.0", "", info)
		if got != (Build{Version: "0.3.0"}) || got.String() != "0.3.0" || len(got.Problems()) == 0 {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// A test binary, which go test builds without VCS information, names
// TestCommit, so the records tests write are sound; it can write them.
func TestCurrentIsComplete(t *testing.T) {
	if b := Current(); b.Commit == "" || len(b.Problems()) > 0 || Writable() != nil {
		t.Errorf("%+v: %v", b, b.Problems())
	}
}

// A commit set with -ldflags -X that no record may hold is refused, naming
// the variable; a full commit or a release tag is not.
func TestCheckLinked(t *testing.T) {
	saved := buildCommit
	t.Cleanup(func() { buildCommit = saved })
	for _, bad := range []string{"519299d", "519299d5cb00", "519299D5CB009DA4904CDF53C2025F1AD609F102", "main", "0.3.0"} {
		buildCommit = bad
		if err := CheckLinked(); err == nil || !strings.Contains(err.Error(), CommitVar) || !strings.Contains(err.Error(), bad) {
			t.Errorf("%q: got %v", bad, err)
		}
	}
	// A release tag is this source's own, or refused naming both.
	for _, other := range []string{"v0.2.0", "v99.0.0-rc.1"} {
		buildCommit = other
		if err := CheckLinked(); err == nil || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "v"+Version) {
			t.Errorf("%q: got %v", other, err)
		}
	}
	for _, good := range []string{"", commit, "v" + Version} {
		buildCommit = good
		if err := CheckLinked(); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestBuildProblems(t *testing.T) {
	for _, b := range []Build{
		{Version: "0.2", Commit: commit},
		{Version: "0.3.0", Commit: ""},
		{Version: "0.3.0", Commit: "519299d5cb0"},
		{Version: "0.3.0", Commit: "519299D5CB009DA4904CDF53C2025F1AD609F102"},
		{Version: "0.3.0", Commit: "main"},
		{Version: "0.3.0", Commit: "unknown"},
		{Version: "0.3.0", Commit: "v0.3"},
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
		`{"toolkit_version":"","toolkit_commit":"v0.3.0","toolkit_modified":false}`:          `toolkit_version: "" is not a version`,
		`{"toolkit_version":"0.3.0 (x)","toolkit_commit":"v0.3.0","toolkit_modified":false}`: `toolkit_version: "0.3.0 (x)" is not a version`,
		`{"toolkit_version":null,"toolkit_commit":"v0.3.0","toolkit_modified":false}`:        "toolkit_version: null",
		`{"toolkit_version":"0.3.0","toolkit_commit":null,"toolkit_modified":false}`:         "toolkit_commit: null",
		`{"toolkit_version":"0.3.0","toolkit_commit":"","toolkit_modified":false}`:           `toolkit_commit: "" is not`,
		`{"toolkit_version":"0.3.0","toolkit_commit":"abc","toolkit_modified":false}`:        `toolkit_commit: "abc" is not`,
		`{"toolkit_version":"0.3.0","toolkit_commit":7,"toolkit_modified":false}`:            "toolkit_commit: 7 is not a string",
		`{"toolkit_version":"0.3.0","toolkit_commit":"v0.3.0","toolkit_modified":"yes"}`:     `toolkit_modified: "yes" is not true or false`,
		`{"toolkit_version":"0.3.0","toolkit_commit":"v0.3.0","toolkit_modified":true}`:      `toolkit_modified: true, and toolkit_commit "v0.3.0" is no tree`,
		`{"toolkit_version":"0.3.0","toolkit_modified":true}`:                                "toolkit_commit missing",
	} {
		var nb *NoBuildError
		err := RequireBuild([]byte(bad))
		if !errors.As(err, &nb) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", bad, err, want)
		}
	}
}

// A version this paisans cannot order, beside an otherwise sound build, is
// a newer paisans' record, refused with the upgrade remedy and the build
// read.
func TestCheckBuildNewer(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"toolkit_version":"1.0","toolkit_commit":"`+commit+`","toolkit_modified":false}`), &obj); err != nil {
		t.Fatal(err)
	}
	b, err := CheckBuild(obj)
	var newer *NewerError
	if !errors.As(err, &newer) || !IsNewer(err) || b.Version != "1.0" || !strings.Contains(err.Error(), "Upgrade paisans") {
		t.Errorf("got %+v, %v", b, err)
	}
	if err := RequireBuild([]byte(`{"toolkit_version":"1.0","toolkit_commit":"main","toolkit_modified":false}`)); IsNewer(err) {
		t.Errorf("a bad commit beside it is no newer build: %v", err)
	}
}

// A build that names no commit refuses to write, saying how to build one
// that does; a build that names one writes.
func TestWritable(t *testing.T) {
	err := FromBuildInfo("0.3.0", "", nil).Writable()
	if err == nil || !strings.Contains(err.Error(), "git checkout") || !strings.Contains(err.Error(), "-X "+CommitVar+"=") {
		t.Errorf("got %v", err)
	}
	if err := FromBuildInfo("0.3.0", "v0.3.0", nil).Writable(); err != nil {
		t.Error(err)
	}
}
