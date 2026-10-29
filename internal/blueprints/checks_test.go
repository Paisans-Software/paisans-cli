package blueprints

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/version"
)

func TestAListSettingTakesNonEmptyStrings(t *testing.T) {
	s := SettingSpec{Type: "list"}
	if p := s.Problem([]any{"admins", "members"}); p != "" {
		t.Errorf("a list of names: %s", p)
	}
	for _, bad := range []any{"admins", []any{"admins", ""}, []any{"admins", 3}} {
		if s.Problem(bad) == "" {
			t.Errorf("%#v passed as a list of strings", bad)
		}
	}
}

// requires_toolkit is met by a version at least the one it names, and by no
// other: a build counts as the version its commit holds, with no exemption
// for a build between releases.
func TestMeets(t *testing.T) {
	for _, c := range []struct {
		req, have string
		ok        bool
	}{
		{"", "0.0.0", true},
		{">= 0.1", "0.1.0", true},
		{">= 0.1", "0.2.3", true},
		{">=0.9.1", "1.0.0", true},
		{">= 0.1", "0.0.0", false},
		{">= 0.1.1", "0.1.0", false},
		{">= 1.0", "0.9.9", false},
		{">= 0.2", "0.3.0-dev", true},
		{">= 0.3", "0.3.0-dev", false},
		{">= 0.3.0", "0.3.0-dev", false},
		{">= 0.3", "0.3.0-rc.1", false},
		{">= 0.2", "0.3.0-rc.1", true},
		{">= 0.3.0-rc.1", "0.3.0-rc.1", true},
		{">= 0.3.0-rc.2", "0.3.0-rc.1", false},
		{">= 0.3.0-rc.1", "0.3.0", true},
		// A build of develop meets every pre-release of its coming release,
		// and never the release itself or the next one's pre-releases.
		{">= 0.3.0-rc.1", "0.3.0-dev", true},
		{">= 0.3.0-rc.14", "0.3.0-dev", true},
		{">= 0.4.0-rc.1", "0.3.0-dev", false},
		{">= 0.3.1-rc.1", "0.3.0-dev", false},
		{">= 0.3-rc.1", "0.3.0", false},
		{">= 0.3.x", "0.3.0", false},
	} {
		err := meets(c.req, c.have)
		if (err == nil) != c.ok {
			t.Errorf("%q against %s: got %v, want met %v", c.req, c.have, err, c.ok)
		}
	}
	if err := requirement(">= 0.1"); err != nil {
		t.Errorf("this build does not meet the first release: %v", err)
	}
	if err := requirement(">= " + strings.TrimSuffix(version.Version, "-dev")); (err == nil) == strings.HasSuffix(version.Version, "-dev") {
		t.Errorf("this build, %s, against its own release: %v", version.Version, err)
	}
}
