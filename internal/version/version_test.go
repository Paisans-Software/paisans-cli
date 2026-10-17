package version

import (
	"encoding/json"
	"errors"
	"runtime/debug"
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

// A build records its version, its commit, and whether the tree was
// dirty; a build with no commit recorded records its version alone.
func TestFromBuildInfo(t *testing.T) {
	for _, c := range []struct {
		name   string
		info   *debug.BuildInfo
		want   Build
		string string
	}{
		{"clean", vcs("false"), Build{"0.3.0", commit, false}, "0.3.0 (519299d5cb00)"},
		{"dirty", vcs("true"), Build{"0.3.0", commit, true}, "0.3.0 (519299d5cb00-dirty)"},
		{"no commit", &debug.BuildInfo{}, Build{Version: "0.3.0"}, "0.3.0"},
		{"no build information", nil, Build{Version: "0.3.0"}, "0.3.0"},
	} {
		got := FromBuildInfo("0.3.0", c.info)
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

func TestBuildProblems(t *testing.T) {
	for _, b := range []Build{
		{Version: "0.2", Commit: commit},
		{Version: "0.3.0", Commit: "519299d5cb00"},
		{Version: "0.3.0", Commit: "519299D5CB009DA4904CDF53C2025F1AD609F102"},
		{Version: "0.3.0", Modified: true},
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
